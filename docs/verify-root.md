# licore 真机验证（root）—— 运行指南

配合 `docs/verify-root.sh` 使用。脚本会自动清理并重跑全部验证项，
把完整输出写入 `/tmp/licore-verify.log` 供回贴。

## 前置条件

在 root 的 ECS / 服务器上需要：

| 项 | 安装命令（Debian/Ubuntu 示例） | 作用 |
| --- | --- | --- |
| Go ≥ 1.20 | `apt install golang`（或官方 tarball） | 编译 licore 与脚本内自包含的 demo 静态二进制 |
| licore | 在本仓库 `go build -o /usr/local/bin/licore .` | 被测二进制 |
| nftables | `apt install nftables` | 容器网络 NAT（出口 MASQUERADE + 端口 DNAT） |
| iproute2 | `apt install iproute2` | 网桥 / veth / 路由管理（`ip` 命令） |
| curl | `apt install curl` | 端口连通性验证 |
| cgroup v2 | 内核 5.x，挂载 `/sys/fs/cgroup` readonly 为 controllers | 资源限制（`--memory`/`--cpus` 等） |

需要的内核能力：**CAP_NET_ADMIN**（veth/网桥/NFT）、**CAP_SYS_ADMIN**
（namespace/pivot_root/exec setns）、cgroup 写权限 —— 直接以 **root** 运行即满足。

可选：
- `shellcheck docs/verify-root.sh`：静态检查脚本（不装也能跑）。
- 用 `go test ./...` 在 root 跑一遍单测（推荐但会触发构建）。

## 运行

```bash
cd /home/li63050a/work/boxli
# 1. 编译到默认路径（或用 LICORE_BIN 指向其他位置）
go build -o /usr/local/bin/licore .

# 2. 以 root 运行（脚本内用 LICORE_HOME 隔离，不会污染 ~/.licore）
sudo bash docs/verify-root.sh
# 或 root 下直接：
#   bash docs/verify-root.sh

# 3. 如果 licore 不在默认路径：
LICORE_BIN=/path/to/licore bash docs/verify-root.sh
```

脚本幂等、可重复跑：每次开场会停止/删除旧容器、清理 cgroup、veth、网桥、
NAT 表与本地 hub 服务，然后从构建开始逐个验证。

## 覆盖项

1. 前置：root、cgroup v2、nft、iproute2、curl、go
2. A  构建：`licore build -t demo:v1 <context>`，`licore images` 可见 demo:v1
3. B  运行：`run -d --network licore0 -p 18080:80` → `ps` 显示 Up
4. B  进程真在：`exec demo /server check` 能取到容器 PID
5. B  veth：`ip link` 出现挂到 licore0 的 veth
6. B  端口：`curl http://127.0.0.1:18080/` 返回 `hello`
7. D  资源：`/sys/fs/cgroup/licore/<id>` 的 memory.max=268435456、
   cpu.max=`100000 100000`、cgroup.procs 含 PID
8. E  exec：`exec demo3 /server check` 成功
9. F  stop / rm：用 date 计时，> 5s 判 FAIL
10. G  hub：本地 `hub serve` → login → push → search → pull
11. 清理：无残留容器 / veth / cgroup / mount

> 说明：demo 镜像是脚本**就地用 `go build` 编译**的一个静态 Go HTTP 服务
> （`/server`），默认监听 :80 返回 `hello from licore`；`/server check` 打
> 印容器 hostname/pid/cgroup 供 exec 验证。这样脚本自包含、不依赖预置镜像。

## 结果回贴

脚本结束后：

```bash
cat /tmp/licore-verify.log
```

把完整日志回贴即可。脚本末尾会输出：

```text
===== 汇总 =====
  PASS=.. FAIL=..
== ALL PASS ==            （或 == THERE ARE FAILURES ==）
```

任何一个 `[FAIL]` 就代表该项未通过；请在回贴时保留对应 `[FAIL]` 行的前后
几行（含实际输出）以便定位。

## 疑难排查

- `syntax error, unexpected ip`（很旧的 licore）：升级到含 v0.5.1 修复的版本。
- `veth ... 未就绪 / 接收 rtnetlink 响应...`：确保用含 v0.5.2（veth peer 属性
  修复）的版本；若仍有，可能是内核/网络命名空间异常，把 log 中对应段贴出。
- `ps` 一直显示 `Starting`：说明 init 未起来，看容器 `container.log`（脚本会
  打印）。
- 资源限制不生效：确认 cgroup v2 挂载、以及脚本用的 `$cid3` 与容器实际 ID 一致。