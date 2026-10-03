# LiCore v0.5.2 审计报告 —— veth netlink 创建失败 / ps Up 时序

在 root 真机（Linux 5.15、cgroup v2）复验时发现两个新问题，本报告记录根因与修复。

## Bug 7【严重】veth 创建 rtnetlink EAGAIN

### 现象

```
licore init: 配置容器网络失败: 容器侧 veth vpe0fb957f 未就绪（20 次重试后）: 链路不存在
licore shim: shim: 启动容器 0fb957f00f74 失败: 装配容器网络失败:
  创建 veth 对 veth0fb957f/vpe0fb957f: 接收 rtnetlink 响应: resource temporarily unavailable
```

`resource temporarily unavailable` = EAGAIN = netlink `Recvfrom` 因 `SO_RCVTIMEO`
到期没收到内核任何响应。

### 根因

`NewVeth` 构造 `VETH_INFO_PEER` 时把 peer 名拼成**裸字符串**：

```go
peerHdr := append(ifInfoMsg(), cstr(peer)...)   // ifinfomsg + "vpe...\0"
```

但内核 `veth_newlink` 用 `nla_parse_nested_deprecated` 解析 `VETH_INFO_PEER`，
期望值是 `struct ifinfomsg`（16 字节）+ 一个 **IFLA_IFNAME 属性**存 peer 名。
旧实现把 peer 名当裸数据塞进去，内核解析失败 → **丢弃整条 RTM_NEWLINK、
不回 ACK** → 父进程 `Recvfrom` 等满超时返回 EAGAIN。

> 附带说明：容器侧 `ConfigurePeer` 的"veth 未就绪"也是因为新建请求被内核丢弃，
> peer 根本没被创建、更没被移入容器 netns。

### 修复

- `NewVeth` 改为用 `buildVethReq` 构造：`VETH_INFO_PEER` 值 =
  `ifinfomsg` + 包裹成 `IFLA_IFNAME` 属性的 peer 名；补充 `IFLA_INFO_DATA` /
  `VETH_INFO_PEER` 常量。
- `do()` 对 EAGAIN 给出明确错误"内核未确认请求（可能请求构造错误或接口状态异常）"，
  便于定位，而非含糊的"resource temporarily unavailable"。
- `SO_RCVTIMEO` 保持 ≥1s（当前 5s），且每请求 seq 唯一、响应严格按 seq 匹配（原已正确）。

### 单元测试

- `TestBuildVethReqPeerIsAttribute`：遍历请求 buffer，断言 `VETH_INFO_PEER` 内是
  `IFLA_IFNAME` 属性而非裸字符串。
- `TestSocketHasRecvTimeout`：断言 rtnetlink 套接字带接收超时。

### commit

`e0c7c84 fix(netlink): build veth peer as IFLA_IFNAME attribute (was kernel-dropped)`

## Bug 8【中】ps 显示 Up 的时序错误

### 现象

`licore run -d` 后立即 `ps` 显示 Up，但 shim 数秒后才检测到启动失败；init 没起来
时 ps 却已显示 Up。

### 根因

detach 路径在 fork shim 的瞬间就写 `Running=true`，早于容器 init 真正启动；若随后
装配/init 失败，在 shim 把它纠正为 not-running 之前，ps 一直误报 Up。

### 修复

为 `RuntimeState` 增加**状态阶段** `Status`：`starting → running → exited`。

- `run -d`（engine detach）写 `Status=starting, Running=false`（不再提前声称 running）。
- shim：每次启动前写 `starting`；`OnStart`（init 存活）写 `running, Running=true`
  （ps 只有在此时才显示 Up）；init 退出或启动失败写 `exited, Running=false`。
- `licore ps`：`starting` 渲染为 `Starting`（明确标注，且默认视图也列出），不再与
  genuine Up 混淆。

### 单元测试

- shim `TestRunMarksStoppedWhenInitExits` 断言最终 `Status=exited`、`Running=false`；
- `TestRunMarksStoppedWhenStartFails` 断言启动失败后不残留 `Running=true`；
- cli `TestPsStatusStarting` 断言 `starting` 渲染为 `Starting` 且被默认 ps 列出。

### commit

`11ba16b fix(shim,engine,ps): add Starting state so ps isn't 'Up' before init is alive`

## 验证结果

- `go test ./...` 16 包全绿；`-race` 关键包无竞态；
- `gofmt`、`go vet ./...` 干净；
- linux/darwin/android（amd64/arm64）交叉编译通过；
- veth 请求结构由单测直接校验（IFLA_IFNAME 属性形式）。

## 待 root 真机回归

```bash
cd /root/licore-test && licore build -t demo:v1 . && licore run -d -p 18080:80 demo:v1
licore ps -a
ps aux | grep hello
curl -s http://localhost:18080
licore stop demo && licore rm demo
```

期望：veth 创建成功（`ip link | grep veth`，master 为 licore0）、进程存活、
curl 返回 hello、stop/rm 秒退、无残留。