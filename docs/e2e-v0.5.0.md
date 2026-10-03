# LiCore v0.5.0 端到端验证

本文档记录 v0.5.0 真机端到端验证。**网络 veth、cgroup 写入、容器执行、
`licore exec` 均需 root（CAP_NET_ADMIN / CAP_SYS_ADMIN / cgroup 写权限）**，
请在 root 环境执行。

> 自动化环境（CI/沙箱）明确标注"非 root 沙箱，无法执行容器"。代码层已完成
> 并单测通过；特权路径需在 root 真机按本 runbook 验收。

## 场景 A：构建

```bash
# 用 busybox 式 rootfs 准备一个可运行镜像（示意）
mkdir -p /root/licore-test && cd /root/licore-test
cat > Boxfile <<'EOF'
FROM scratch
COPY hello.sh /bin/hello
COPY index.html /index.html
ENV APP=licore
ENTRYPOINT ["/bin/hello"]
EOF
chmod +x hello.sh
licore build -t demo:v1 .
licore images        # 期望出现 demo:v1（linux/<arch>，自建层）
```

预期 `licore build -t demo:v1 .` 输出：

```text
已构建并导入 demo:v1（… .licore，1 层，… KiB）
落地目录：/root/.licore/images/demo/v1
```

> 已在本环境（非 root）验证：`licore build -t demo:v1 .` 真正调用 `build.Build()`
> 产出 `.licore` 并自动 `licore pull` 导入 store，`licore images` 可见 `demo:v1`。

## 场景 B：运行 + 网络

```bash
licore run -d --name demo --network licore0 -p 8080:80 demo:v1
licore ps
curl -s http://127.0.0.1:8080        # 期望返回 index.html 内容
ip link | grep veth                  # 期望出现 vethXX / vpeXX
nft list ruleset | grep 8080         # 期望 DNAT 绑定到容器 IP
```

验证点：
- 容器内应有一张 eth0、持有 172.18.0.x/16 IP、默认路由经 172.18.0.1。
- 容器内 DNS 指向 172.18.0.1（`/etc/resolv.conf`），`/etc/hosts` 含容器名。

## 场景 C：卷挂载

```bash
licore run -d --name demo2 -v /root/data:/data demo:v1
echo hi > /root/data/test.txt
licore exec demo2 -- cat /data/test.txt   # 期望输出 hi
```

验证点：/root/data 与容器内 /data 为同一挂载（bind）；`:ro` 时写入失败。

## 场景 D：资源限制

```bash
licore run -d --name demo3 --memory 256 --cpus 1 demo:v1
cat /sys/fs/cgroup/licore/<demo3-id>/memory.max   # 期望 268435456
cat /sys/fs/cgroup/licore/<demo3-id>/cpu.max      # 期望 100000 100000
cat /sys/fs/cgroup/licore/<demo3-id>/cgroup.procs # 期望含容器 init 的宿主 PID
licore stats demo3
```

验证点：`--memory 256`（MiB）→ memory.max=268435456；`--cpus 1` →
cpu.max=100000 100000；容器各进程落在 `licore/<id>` 组。

## 场景 E：exec

```bash
licore exec -it demo3 /bin/sh
# 容器内执行：
#   hostname      （容器 UTS 名）
#   ip addr       （eth0 与 172.18.0.x）
#   ls /data      （卷内容）
#   exit
licore exec -e FOO=bar -w /tmp -u 1000 demo3 /bin/env   # env/工作目录/用户生效
```

## 场景 F：清理

```bash
licore stop demo demo2 demo3
licore rm demo demo2 demo3
# 验证无残留：
licore ps -a
ls /sys/fs/cgroup/licore/            # 无 demo* 目录
ip link | grep -E 'veth|vpe'        # 无 veth
nft list ruleset | grep 8080        # 无残留 DNAT
```

## 场景 G：Hub

```bash
licore hub serve --port 3727 &
LICORE_HUB=http://127.0.0.1:3727 licore login --username admin --password admin
LICORE_HUB=http://127.0.0.1:3727 licore push demo:v1
LICORE_HUB=http://127.0.0.1:3727 licore search demo
LICORE_HUB=http://127.0.0.1:3727 licore pull demo:v1
pkill -f 'licore hub serve'
```

> 已在本环境（非 root）实测通过：serve 启动 → login → push → search → pull 全链路
> 成功，拉回的 `source.licore` 摘要校验通过并导入本地 store。

## 已补齐的半成品（本版本）

- `licore build`：从"输出计划"改为真正调用 `build.Build()` 并自动导入 store，
  支持 `-t/--tag`、`-f/--file`、构建上下文、`FROM scratch`。
- `compose up / scale`：改为真正创建（engine.Run）与扩缩副本，不再打印计划。
- 未实现的资源能力（`--storage`、`--gpu/--npu`、`--network-bandwidth`）显式报错，
  不再"降级 warn 后假装成功"（见 `docs/audit-v0.5.0.md`）。
- `-p` 在 host/none 网络显式报错，不再忽略。

## 声明

本环境（非 root 沙箱）能验证 A/G 与代码层；B–F（veth、cgroup、fork、exec）
需 root，请按本 runbook 在 root 真机执行。
## v0.5.1 修复与回归（root 真机发现 6 bug）

- 修复清单与根因见 `docs/audit-v0.5.1.md`（nft 语法、LinkByName NUL、veth
  有界重试、shim 状态、stop/rm 信号与 netlink 超时）。
- **root 真机回归步骤**（在 /root/licore-test）：

```bash
cd /root/licore-test && go build -o /tmp/licore ../../home/li63050a/work/boxli 2>/dev/null || true
licore build -t demo:v1 .            # 期望「已构建并导入 demo:v1」
licore run -d --name demo --network licore0 -p 8080:80 demo:v1
licore ps                            # 期望 Up 且能 grep 到容器进程（不再"Up 但无进程"）
curl -s http://127.0.0.1:8080       # 返回 hello
licore exec -it demo /bin/sh         # 进入容器
licore stop demo                     # 快速返回（不再等 15s/挂起）；Ctrl+C 可中断
licore rm -f demo                    # 快速返回，Ctrl+C 可中断
ip link | grep veth; nft list ruleset | grep 8080   # 清理后无残留
```
