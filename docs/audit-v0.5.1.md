# LiCore v0.5.1 审计报告 —— root 真机 6 个 bug 修复

在 root 真机（Linux 5.15、cgroup v2、root、nft）验证时发现 6 个 bug，本报告记录
每个 bug 的根因、修复方式、单元测试与验证结果。修复 commit 用 `fix(<scope>)`

> 说明：修复均在非 root 沙箱完成并单测/交叉编译通过；其中 nft 语法问题用
> `nft` 解析直接验证（`replace` 语法错、`add` 合法）。**涉及真实 namespace/
> cgroup/进程的最终真机验收仍需 root**，步骤见 `docs/e2e-v0.5.0.md`。

## Bug 1【严重】前台 run 卡死、信号杀不掉

- **根因**：`ConfigurePeer` 在容器侧轮询 veth 长达 **5 秒**（20ms 步长），期间
  若干 netlink 请求也没有接收超时——内核异常不回应时 `Recvfrom` 无限阻塞，
  使前台进程既收不到 Ctrl+C 也停不下来（实为卡在阻塞 syscall）。
- **修复**：
  - Bug 4：veth 等待改为**有界重试**（20 × 100ms），失败即让 init 非零退出，
    不再"看似卡住 5 秒"（`internal/network/wire.go`）。
  - Bug 6：netlink 套接字设 `SO_RCVTIMEO`，`Recvfrom` 超时返回错误而非永久阻塞
    （`internal/network/netlink/netlink.go`）。
- **测试**：`TestSocketHasRecvTimeout` 断言 rtnetlink 套接字带接收超时。

## Bug 2【高】nft 规则语法错误

- **现象**：`nft replace rule ip licore post_nat ip saddr ... masquerade` 报
  `syntax error, unexpected ip, expecting handle`。
- **根因**：`replace rule` 要求规则 handle，`replace rule ip ...` 形式非法；
  且该 `ip` 关键字在 replace 上下文解析失败。**已用 `nft` 解析验证**：
  - `add rule ip licore post_nat ip saddr ... masquerade` → 仅"Operation not
    permitted"（权限），**无语法错**；
  - `replace rule ip licore post_nat ip saddr ...` → `syntax error, unexpected
    ip, expecting handle`（复现）。
- **修复**：改为 `flush` + `add rule`（幂等 + 语法正确），见
  `internal/network/driver_linux.go` 的 `natMasqArgs`/`dnatRuleArgs`/
  `flushChains`/`applyPortRules`。
- **测试**：`TestNatRuleArgsUseAddRule` 断言生成命令均为 `add rule` 形式、
  不含 `replace`。

## Bug 3【中】网桥已存在时误报"未就绪"

- **根因**：`netlink.LinkByName` 用内核返回的 `IFLA_IFNAME` **原始字节**（含尾
  NUL 与对齐填充）与请求名比较，导致对已存在的接口（如 `licore0`、`lo`）恒判
  "链路不存在"→ `driverBootstrap` 误以为网桥缺失而重建 → `NewLink` 报
  `file exists`；并连锁破坏 veth（`SetLinkMaster`/`LinkUp` 找不到网桥）。
- **修复**：新增 `trimAttrString`，比较前去掉属性尾 NUL/填充
  （`internal/network/netlink/link.go`）。
- **验证**：`netlink.LinkByName("lo")` 由"链路不存在"变为正确返回（ifindex=1）。
  测试 `TestLinkByNameLoop`、`TestTrimAttrString`。

## Bug 4【高】veth 未就绪致 init 直接退出（无 fallback）

- **根因**：基本即 Bug 3 的 `LinkByName` NUL 误判——容器侧 veth 已在本 netns，
  但 `LinkByName` 匹配不上，`ConfigurePeer` 轮询 5 秒后超时、init 报错退出；
  且失败后没有把容器状态标为非运行（表现为假 Up）。
- **修复**：
  - Bug 3 修复 `LinkByName`（veth 实际可被找到）。
  - 等待改为有界重试并快速失败；init 失败即非零退出，shim 据此写入
    `Running=false`（Bug 5）。
- **测试**：`TestLinkByNameLoop`、shim 状态测试（见 Bug 5）。

## Bug 5【严重】init 已退出但 ps 显示 Up

- **根因**：缺乏对"init 退出后必须立即把 Running 置 false"的可验证保证；叠加
  Bug 4 的 5 秒卡顿窗口，用户看到 Up 但 `ps aux` 无进程，stop 等满 15 秒才强杀。
- **修复**：把 `runtime.StartWith` 抽成可注入的 `startWithFn`，使 shim 主循环的
  状态迁移可测；确认 init 退出（无论失败）后写 Running=false
  （`internal/shim/run_linux.go`）。
- **测试**：`TestRunMarksStoppedWhenInitExits`、`TestRunMarksStoppedWhenStartFails`
  用 fake StartWith 断言 Running=false 正确写回。

## Bug 6【严重】licore stop / rm 卡死，Ctrl+C 无效

- **根因**：
  1. 共享 rtnetlink 套接字无接收超时 → 内核不回应时 `Recvfrom` 无限阻塞，
     连带卡死 stop/rm 及后续 netlink 操作；
  2. stop/rm 未安装信号处理，Ctrl+C 只是默认 SIGINT，进程若停在不可中断
     syscall 则无效。
- **修复**：
  - netlink `SO_RCVTIMEO`（同上）。
  - `engine.Stop`/`engine.Remove` 增加 ctx 可取消形态（`StopWithContext`/
    `RemoveWithContext`），取消即在轮询间隙返回 `ctx.Err()`。
  - `cli stop/rm` 接入 `sigCtx`（SIGINT/SIGTERM → ctx 取消）+ `cleanupCmdContext`
    释放监听。
- **测试**：`TestSocketHasRecvTimeout`。

## 验证结果汇总

- 单元测试：`go test ./...` 16 包全绿；`-race` 关键包无竞态。
- 静态：`gofmt`、`go vet ./...` 干净；
- 交叉编译：linux/darwin/android（amd64/arm64）全过；
- nft 真机语法：`add rule ... ip saddr ... masquerade` 无语法错（Bug 2）；
- 真机根因复现（非 root 沙箱可做部分）：`LinkByName("lo")` 由误报修正为返回
  ifindex（Bug 3/4 根因）。

## 待 root 真机回归

构建 + 运行 + 网络 + 卷 + 资源 + exec + stop/rm 完整场景见
`docs/e2e-v0.5.0.md`，请在 root 环境重跑验收。