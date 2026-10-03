# LiCore 审计报告 v0.6.0

范围：在 v0.6.0 真机验证 + 测试基础上，做静态审计 / 安全审计 / 覆盖率 / 修复清单。

## 1. 静态审计

### 工具
- `go vet -all ./...`：干净（0 告警）。
- `staticcheck ./...`：仅**死代码**（U1000）与风格建议（S1003/SA4004/SA4010），无未处理 error / 并发 / 资源泄漏告警。
- `gosec ./...`：47 项告警，经逐条甄别**全部为误报或低危**（见下）。

### 逐项排查
- **未处理 error**：无。忽略点均显式 `_ =`。
- **裸 panic**：无业务裸 panic（仅 Go runtime fatal 正常路径）。
- **未关闭 file/conn**：openpty/文件/tar 均 defer close 或显式关闭；netlink socket 单例常驻（按设计）。
- **goroutine 泄漏**：shim/engine/CLI 的 goroutine 均有终止条件（Watched/StopCh/EOF）。`-race -count=5` 关键包全绿。
- **并发访问 map/slice 无锁**：netlink seq 计数器、shim 状态写均加锁/单写方；store 名字锁 O_EXCL 原子（本次新增）。
- **defer 在循环内 / time.After 循环**：未发现。
- **context 未传递/未取消**：hub/network/shutdown 均用 context；`signal.NotifyContext` 统一。
- **整数溢出 / 除零 / 边界**：gosec G115 的 `byte(v>>8)` 是**有意的字节序打包**（非溢出）；`int64(st.Rdev)` 取设备号安全。
- **硬编码路径/端口/超时**：预设 `licore0`/`172.18.0.0/16` 已做冲突避让（pickFreeSubnet）；SO_RCVTIMEO/宽限等有常量集中。

## 2. 安全审计

| 项 | 结论 |
| --- | --- |
| 命令注入 | ✅ 安全：`exec.Command("nft", args...)`/`exec.CommandContext`/`syscall.Exec` 全部 argv 传参、不经过 shell（gosec G204 系误报）|
| 路径穿越 | ✅ 安全：hub `validDigest` 严格 `[0-9a-f]{64}`，blob/tag 路径不可能含 `..`/`/`（gosec G703 误报）；容器挂载目标经 `safeContainerTarget` 校验绝对、无逃逸 |
| 符号链接逃逸 | ✅ MergeLayers/SafeArchivePath 有防护（沿用既有实现并测试）|
| 不安全临时文件 | ✅ 一律 `os.CreateTemp` 或同目录 `.tmp`+rename 原子替换 |
| TOCTOU | ⚠️ 已在 2 处发现并修：store 容器名唯一性（原扫描竞态→O_EXCL 锁）；`:ro` 卷 bind 后未 remount 只读 |
| cgroup/namespace 权限 | ✅ exec 非 root 报 ErrNotRoot；容器 rootless 判定按 euid |
| capabilities 剥离 | ⚠️ licore 不主动 drop caps（非 OCI 目标）；文档已注明 |
| Hub 鉴权 / JWT / digest | ✅ 无 token 401、登录签发 JWT、blob 摘要写入时实算比对；弱校验未现 |
| 敏感信息写日志 | ✅ 只记 PID/ID/错误；hub 令牌只落 0600 凭证文件，不打印 |

## 3. 覆盖率（目标 runtime/storage/shim/network/engine/hub ≥ 70%）

| 包 | 覆盖率 | 达标 |
| --- | --- | --- |
| engine | 73.3% | ✅ |
| storage | 70.5% | ✅ |
| hub | 70.5% | ✅ |
| store | 70.9% | ✅ |
| image | 71.1% | ✅ |
| service | 77.2% | ✅ |
| build | 77.6% | ✅ |
| compose / dev | 84.3% / 85.9% | ✅ |
| shim | 62.2% | ⚠️ 特权（Reexec/fork）|
| network | 60.1% | ⚠️ 特权（nft/veth）|
| netlink | 62.2% | ⚠️ 特权（rtnetlink）|
| runtime | 28.9% | ⚠️ 特权（fork/setns/pivot_root）|

**结构性限制**：未达 70 的包缺口全在**需 root 的特权 syscall/进程路径**
（nft/veth、fork+namespace、pivot_root、rtnetlink），非 root 沙箱无法执行；
已对纯逻辑/mock 可测部分补足，特权路径以真机（本报告 + audit 既往）保证。

## 4. 修复的 bug 清单（v0.6.0 新增）

| 严重度 | 位置 | 根因 | 修复 | commit |
| --- | --- | --- | --- | --- |
| 高 | internal/resource | cgroup v2 未在 licore subtree_control enable cpu/memory/pids → 资源限制 EPERM 落空 | `enableControllers()` | `8f04018` |
| 高 | internal/runtime/exec | 纯 Go `setns(CLONE_NEWNS)` 进 mount ns 恒 EINVAL（Go #9091）| cgo fork 单线程子进程 setns+exec（可选组件）| `39ccd18` |
| 高 | internal/runtime/init | `:ro` 卷单次 bind 只读被内核忽略 | bind 后再 remount MS_REMOUNT\|BIND\|RDONLY | `d241797` |
| 中 | internal/store,engine | 并发同名 run 名字唯一性 TOCTOU + rm 泄漏名字锁 | O_EXCL 名字锁；rm 走 RemoveContainer 释放 | `649510d` |
| 低 | internal/runtime | exec 改造后旧 setns 死代码残留 | 清理 | `6e81c59` |
| 中 | internal/build | `maxBuildLayerBytes = 1<<32` 为无类型常量，32 位平台 `int` 溢出 → `GOOS=linux GOARCH=386` 编译失败 | 显式声明 `int64` | `8d302b4` |
| 低 | internal/dev（测试） | 去抖测试写入串可能跨越「轮询→去抖到期→再轮询」边界，合法产生第二批 → 偶发误报 | 写入前对齐轮询边界 + 缩短写入间隔 | `de44547` |

## 5. 遗留

- 端口映射 host→容器：受宿主 netfilter 限制（rootless-docker FORWARD DROP + ufw），需标准 root 主机验证；verify-root.sh 已做 Docker 对照与降级。
- 覆盖率未达 70 的包为特权路径，需 root 单测/真机覆盖。
- `execns`（cgo）arm64 交叉编译需 arm64 C 工具链（CI 负责）；`-tags nocgo_exec`
  下 linux/amd64、linux/arm64、linux/386、linux/arm、linux/riscv64、darwin（amd64/arm64）、
  android/arm64 均可编。`android/amd64`、`android/arm` 受 Go 工具链限制必须启用 cgo 外部链接，非本仓库缺陷。
- capabilities 剥离：licore 非 OCI，不实现 cap drop（文档声明）。

## 6. 验证

- `go test ./... -count=1`：16 包全绿（连跑 3 次无失败）。
- `go vet -all ./...`、`gofmt -l .`：干净。
- `-race -count=5` 关键包：无 data race。`internal/dev` 去抖用例连跑 40 次、
  并在 8 路 CPU 争用下连跑 15 次全绿（修复前曾在整包 `./...` 运行中偶发失败）。
- 交叉编译：cgo linux/amd64 + nocgo_exec（linux/amd64、linux/arm64、linux/386、
  linux/arm、linux/riscv64、darwin/amd64、darwin/arm64、android/arm64）全部通过。
  `android/amd64` 需 cgo 外部链接（Go 工具链限制，非本仓库缺陷），
  `cgo linux/arm64` 需 arm64 C 工具链。
- 真机复验（root，独立 `LICORE_HOME`）：`memory.max=67108864`、`cpu.max=50000 100000`、
  `pids.max=32` 实际写入生效；`exec` 报告容器 hostname（证明 setns 成功）；
  并发 3 次同名 `run` 恰好 1 个成功、2 个被拒且名字锁在 `rm` 后释放；
  `:ro` 卷写入被 DENIED。复验后宿主无 licore 残留（无 nft 表 / 无 veth / 无网桥），
  Docker 的 3 个容器与 `ip_forward` 未受影响。
- 真机 A–J：见 test-report-v0.6.0.md。