# Boxli 全面审计（audit-full）

本报告汇总在非 root 沙箱中完成的静态审计、安全审计、并发/错误注入测试与
覆盖率情况，以及由此修复的 bug。

## 1. 静态审计

用 `go vet ./internal/... ./hub/...`（干净）与 `staticcheck ./...` 扫描全仓库。

- staticcheck 仅报**死代码 / 风格**（U1000/S1003/SA4010 等），无未处理 error、
  goroutine 泄漏、资源未关闭或并发数据竞争类告警。已修复/核实的关键项：
  - `internal/network/util_test.go`：无意义的自我比较（`a != a`）→ 改为断言具体值。
  - 死代码（`notImplemented`、`manifestsDir`、`errNotCgroupV2`、`writeDevices`
    等）保留但无危害，未逐一清理以避免触碰非关键模块。
- 并发审查：rtnetlink 单例套接字操作由 `sockMu` 串行化、`sockOnce` 初始化；
  `-race -count=5` 关键包全绿，无 data race。

## 2. 修复的 bug（本次审计新发现）

### fix(netlink): 单条 ACK 未被视为请求完成 → 命令 ops 一直超时 EAGAIN

**根因（重要）**：`recvLoop` 读到 `NLMSG_ERROR(code=0)` 的 ACK 时 `continue`
继续等待下一条消息。对只回单个 ACK 的命令（`NewLink`/`NewVeth`/`AddAddr`/
`DelLink`）来说，永远等不到终止信号 → `Recvfrom` 等满 `SO_RCVTIMEO` 返回
EAGAIN。**即便 Bug 7 的 veth peer 属性修好，只要走这条 "仅 ACK" 分支，任何
命令创建仍会超时失败**。

**修复**：`recvLoop` 在遇到 code=0 ACK 时立即 `return (nil, nil)`（dump 仍由
`NLMSG_DONE` 终止）。并把收发循环抽成 `recvLoop(fd,seq,type)` 以便用 mock
验证。

commit `1cadc97`；配套测试（mock socket、AF_UNIX socketpair）：
- `TestRecvLoopAck`：单 ACK 应立即成功（关键回归）
- `TestRecvLoopEAGAIN`：空非阻塞 socket → 明确"等待应答超时"错误
- `TestRecvLoopOpError`：EPERM → 包装 `*OpError`
- `TestRecvLoopSeqMismatch`：异 seq 应答被跳过

### 其余（延续 0.5.1/0.5.2 的记录）

- nft `replace` 语法错 → `add rule`（0.5.1）
- `netlink.LinkByName` NUL 匹配 bug（0.5.1）
- veth peer 应为 `IFLA_IFNAME` 属性（0.5.2）
- shim/ps 状态机 `starting→running→exited`（0.5.2）
- stop/rm Ctrl+C 可取消 + netlink 接收超时（0.5.1）

## 3. 安全审计

- **命令注入**：`exec.Command("nft", args...)` 直接传 argv、无 shell；容器命令经
  `syscall.Exec` 传递。未见 `sh -c` 注入向量。
- **路径穿越**：build 的 COPY 目标经符号链接逃逸防护；卷目标强制绝对路径且拒绝
  `..`；镜像层解包有 `SafeArchivePath`。
- **TOCTOU**：store/卷/网络持久化均为"同目录临时文件 + rename"原子替换；build
  的 COPY 源用 `EvalSymlinks` 校验仍在上下文内。
- **敏感信息日志**：日志只含 PID/容器 ID/错误信息，未发现密码/令牌明文日志
  （hub 登录成功仅回显用户名，令牌只落 0600 凭证文件）。
- **未实现资源能力**（`--storage`/`--gpu`/`--npu`/`--network-bandwidth`）：
  CLI 层显式拒绝、`resource.write*` 返回 `ErrUnsupported`，不做静默成功。

## 4. 错误注入测试

- netlink：EAGAIN、EPERM、ACK、seq 不匹配（`internal/network/netlink/error_test.go`）。
- resource：未实现路径 `writeNetworkBandwidth/writeStorageAndDevices` 请求时报
  `ErrUnsupported`（`pure_test.go`）。
- 上述用 mock socketpair / 纯函数注入，无需 root。

## 5. 并发测试

`go test ./internal/{network,engine,shim,cli,store,resource,...} -race -count=5`
全绿，无 data race。

## 6. 单元测试覆盖率

| 包 | 覆盖率 | 达标(≥70%)? | 说明 |
| --- | --- | --- | --- |
| engine | 73.3% | ✅ | 补 BuildRootfs/netEnvFor |
| store | 71.7% | ✅ | |
| image | 71.1% | ✅ | |
| service | 77.2% | ✅ | |
| build | 77.6% | ✅ | |
| compose / dev | 84.3% / 86.7% | ✅ | |
| storage | 69.2% | ≈ | |
| hub | 66.9% | 需 root 前 | 多为 HTTP 端到端 |
| shim | 62.2% | 需 root | 主循环是真实启动 |
| network | 55.2% | 需 root | veth/nft/wire 需特权 |
| netlink | 53.1% | 需 root | recvLoop 已 mock 提测 |
| boot | 48.6% | 需 root | 多为拉起/信号 |
| resource | 47.8% | 需 root | cgroup 写入 |
| cli | 43.0% | 需 root | 印刷输出命令多 |
| runtime | 24.9% | 需 root | fork + setns + pivot_root |
| doctor / scaffold | 0% | 工具性 | lint/诊断，未覆盖 |

**结构性限制**：runtime/shim/boot/network/netlink/resource/cli 的覆盖率缺口集中
在**特权系统调用路径**（fork+namespace、setns、pivot_root、veth/nft、cgroup 写入、
进程信号），这些在非 root 沙箱无法执行。已对纯逻辑/可 mock 部分补测
（netlink recvLoop/mock、engine BuildRootfs、resource 纯函数与拒绝路径）。

## 7. 遗留

- 特权 e2e 需以 root 运行 `docs/verify-root.sh`（见其日志）。
- doctor/scaffold 为工具性命令，可按需补测。
### fix(netlink): LinkUp/LinkDown hit the nlmsghdr seq, not ifinfomsg (EAGAIN)

**现象**：root 真机上 `CAP_NET_ADMIN` 有效、二进制也已重编最新，但
`上线网桥 boxli0: rtnetlink add-link 等待应答超时 ... resource temporarily
unavailable` 仍复现。

**根因**：`setIFFBuf` 把 `IFF_UP` 写进了 `buf[8:12]/[12:16]`——那是
16 字节 `nlmsghdr` 的 **seq/pid**，不是随后 16 字节 `struct ifinfomsg` 的
`ifi_flags/ifi_change`（应在 `buf[24:32]`）。于是 `LinkUp`/`LinkDown` 发出去
的请求 seq 被覆盖、且 `ifi_change=0` → 内核丢弃、不回 ACK → `recvLoop`
超时 EAGAIN。这解释了为何即便有 CAP_NET_ADMIN、最新二进制也仍 EAGAIN。

**修复**：`setIFFBuf` 改写到 `header+8 / header+12`（`buf[24/28]`）。回归测试
`TestSetIFFBufWritesIfinfomsg`、`TestLinkUpRequestLayout` 断言 seq 不被改、
flags/change 命中 ifinfomsg。

commit `779cbf5`

### fix(netlink): SetLinkMaster enslaved the bridge to itself (EBUSY)

**现象**：修掉 setIFFBuf 后 veth 走到 SetLinkMaster 仍失败，新错误
`挂接网桥侧 veth 到 boxli0: rtnetlink add-link: device or resource busy`。

**根因**：`SetLinkMaster(name, master)` 把 `LinkByName(master)`（网桥 boxli0）
的 ifindex 放进了 RTM_NEWLINK 的 `ifinfomsg.ifindex`，即把请求主体误设成了
**网桥**，再叠加 `IFLA_MASTER=网桥` —— 变成"把网桥挂到网桥自己" → EBUSY。
RTM_NEWLINK 主体必须是被挂接的链路（hostVeth），`IFLA_MASTER` 才指向网桥。

**修复**：`buildSetLinkMasterReq` 用 slave 的 ifindex/IFLA_IFNAME 作主体、master
作 IFLA_MASTER。回归测试断言 ifinfomsg.ifindex=slave、IFLA_MASTER=master。

commit `4b3a833`

### fix(netlink): AddRoute built struct rtmsg wrong (rtm_type=RTN_UNSPEC → EINVAL)

**现象**：veth 创建 + 挂网桥成功后，容器里 `添加默认路由 via 172.18.0.1:
rtnetlink add-route: invalid argument`。

**根因**：`AddRoute`/`DelRoute` 用 `PutU32(b[4:8], RT_TABLE_MAIN)` 构建
`struct rtmsg`，把 `rtm_type`（byte7）盖成 0=RTN_UNSPEC，再把 RTN_UNICAST
错写进 `rtm_flags`。内核拒收 → EINVAL。

**修复**：`buildRouteMsg` 逐字节设 rtm_table=main、rtm_protocol=boot、
rtm_scope=universe、rtm_type=unicast，flags 置 0。测试断言语义。

commit `8ec16e9`

### fix(netlink): AddAddr put prefix in ifa_flags, not ifa_prefixlen (ENETUNREACH)

**现象**：AddRoute 修好后，AddRoute 改为报 `network is unreachable`。根因在
AddAddr：把 prefix 写进 `struct ifaddrmsg` 的 `ifa_flags`（byte2）而非
`ifa_prefixlen`（byte1），容器 veth 的 IP 变成 /0、无连本网段路由 → 随后的
默认路由网关不可达。

**修复**：`buildAddrMsg` 设 ifa_prefixlen=b1、flags=0、scope=universe。测试断言
ifaddrmsg 字节布局。commit `6709883`

### fix(network): create /etc before writing resolv.conf/hosts (scratch images)

**现象**：网络路由/IP 修好后，init 报 `写 /etc/resolv.conf: no such file or
directory` 退出(1)。demo 是 `FROM scratch`，没有 /etc；`ConfigurePeer` 写
resolv.conf/hosts 前未建 /etc。这也连锁导致此前 curl 空、exec 的 setns 看到
已死 init。

**修复**：`writeDNSFiles`（拆出 `writeDNSFilesTo(root,..)`）先 `MkdirAll
<root>/etc` 再写。单测在临时目录验证。commit `5a70737`

### fix(network): prune stale endpoints whose container is gone (dead DNAT)

**现象**：容器已 Up 且存活，但 curl 仍空。nft 里出现**两条** dport 18080 DNAT
（172.18.0.3 与历史残留的 172.18.0.4）。`ApplyNAT` 为网络文件里**所有**还登记的
端点都加 DNAT；孤儿端点指向已删除容器（死 IP），若其规则先匹配，会把到活容器的
连接丢包。

**修复**：新增 `Manager.PruneEndpoints(name, alive)`——删掉 store 中已不存在的
容器的端点并 detach 其 veth；`wireNetworkBeforeStart` 在分配新容器 IP 前调用。
测试覆盖。commit `53b3341`
