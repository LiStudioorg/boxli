# Boxli v0.4.0 端到端验证

本文档记录 v0.4.0 的完整场景验证（网络 + 卷 + 资源 + exec + 清理）。
网络 veth、cgroup、`boxli exec` 均需要 **root**（CAP_NET_ADMIN /
CAP_SYS_ADMIN），请在 root 或具备相应特权的环境执行。

## 0. 前置

```bash
# 以 root 构建
go build -o boxli .
# 隔离数据目录，避免污染 ~/.boxli
export BOXLI_HOME=/tmp/boxli-e2e
```

## 1. 构建镜像

```bash
# 准备一个带入口的临时 rootfs/busybox 构建上下文（示例，按需调整）
mkdir -p /tmp/boxli-src && cd /tmp/boxli-src
# ……放入业务文件……
./boxli build --file /tmp/boxli-src/Boxfile /tmp/boxli-src   # 上下文必须显式给出
./boxli pull ./demo.boxli          # 本地导入构建产物（若产出 .boxli 文件）
```

> 说明：v0.4 的 `boxli build` 输出构建计划；实际 `.boxli` 镜像可由
> `boxli save` / 手工 `boxli build` 产物或 `github.com/...` 提供。
> 示例以 `demo:v1` 引用一个已导入的镜像为准。

## 2. 启动带网络 + 卷 + 资源的容器

```bash
./boxli run -d --name demo \
  --network boxli0 -p 8080:80 \
  -v mydata:/data \
  --memory 256M --cpus 1 \
  demo:v1
```

预期：
- 打印容器 ID 与 `已在后台运行`。
- `./boxli network ls` 显示 `boxli0`，且有端点 `demo` 及 IP（172.18.0.x）。
- `./boxli volume ls` 显示 `mydata`。

## 3. 验证容器内网卡/端口

```bash
# 容器内是否存在网卡且持有 IP（进入容器验证）
./boxli exec demo ip addr show eth0        # 期望 172.18.0.x/16 与 UP 状态
./boxli exec demo ip route                 # 期望经 172.18.0.1 的默认路由
# 宿主访问端口（若容器内监听 80）
curl -s http://127.0.0.1:8080/ | head
```

## 4. 验证卷挂载

```bash
# 容器内 /data 应看到卷内容
./boxli exec demo sh -c 'echo hello > /data/hello.txt'
./boxli exec demo cat /data/hello.txt      # hello
# 宿主侧卷数据目录同步存在
ls <BOXLI_HOME>/volumes/mydata/
# 只读挂载（-v ...:ro）写入应失败
./boxli exec demo sh -c 'touch /ro-mount/x'  # 若目标只读则报错
```

## 5. 验证内存限制

```bash
./boxli exec demo sh -c 'cat /sys/fs/cgroup/memory.max'  # 应为 256MiB
./boxli stats demo   # 内存上限/用量可见
```

## 6. exec 进入容器

```bash
./boxli exec -it demo /bin/sh
# 交互 shell 后可用 exit 退出
```

## 7. 停止与清理

```bash
./boxli stop demo      # 优雅停止（SIGTERM → 宽限后 SIGKILL）
./boxli rm demo        # 删除容器目录 + 断开网络端点 + 删除其 cgroup
# 确认无残留
./boxli ps -a
./boxli network ls     # boxli0 上不再有 demo 端点
ls /sys/fs/cgroup/boxli/  # demo 的 cgroup 已移除
ip link show           # 宿主 veth 已清除（boxli0 本身保留）
```

## 异常场景

| 场景 | 预期 |
| --- | --- |
| 镜像损坏 pull | 拒绝并清理临时文件，不残留 `images/` 半成品 |
| 启动失败 | 容器目录保留（有 runtime.json），rootfs 半成品清理 |
| 非 root 运行 | `exec` 与 veth 装配给出"需要 root"清晰提示，不崩溃 |
| 进程 SIGKILL | shim 检测到 init 消失并按 restart 策略处理或退出 |

## 本环境（CI/沙箱非 root）说明

上述网络/卷/资源/exec 步骤需特权；在非 root 沙箱中无法执行，本文件作为
发布前的 root 验收 runbook。非特权下已验证的部分：
- `go build ./...`、`go vet ./...`、`go test ./...`（-race 见审计报告）全绿；
- `boxli hub serve` → login / push / search / pull 端到端可跑通（见
  `docs/hub-e2e.md`）；
- CLI 参数绑定与错误分支（exec 未运行容器、network 缺省 boxli0 自动建、
  卷匿名自动创建）有单测覆盖。