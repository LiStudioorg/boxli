# Boxli

Boxli 是一个用 Go 编写的轻量级容器引擎：常驻内存 10–20 MiB，单二进制分发，覆盖 Linux / Android / macOS —— 但它是**完全自研生态，不兼容 Docker / OCI**。

## 特性

- 🪶 **极轻**：运行时内存目标 10–20 MiB，单个静态二进制；除可选的 `internal/execns`（`exec` 进入容器挂载命名空间）外全部为纯 Go。
- 🧩 **自研镜像格式**：`.boxli` = 分层 gzip tar + 自研 `index.json`，简单、可逐层审计。
- 📱 **平台**：Linux 服务器、Android（有 Root）、macOS；Android 无 Root 官方不支持（见《Android 支持策略》）。
- 🏗 **多架构**：amd64 / arm64 / 386 / riscv64 等同一条命令交叉编译。
- 🔌 **零外部依赖**：不需要 Docker、containerd 或任何 OCI 组件，装一个 `boxli` 就能用。
- 🛡 **资源限制**：CPU / 内存 / PID 限额内置于引擎。
- 🚀 **开机自启**：一条 `boxli boot enable` 完成系统服务配置，无全局守护进程，每个容器由轻量 shim 独立守护。

## 快速开始

> Boxli 已进入 **v0.6.0**：镜像、运行时、网络、卷、资源限制、`exec`、Hub 分发均落地，`boxli build` 真正构建并导入镜像、`compose up/scale` 真正创建容器；v0.6.0 补齐了 cgroup 限额真正生效、`exec` 进入全部命名空间、卷 `:ro` 只读、同名并发创建的原子性，并修复了 netlink 组包缺陷。真机验收与审计见 [docs/test-report-v0.6.0.md](docs/test-report-v0.6.0.md)、[docs/audit-v0.6.0.md](docs/audit-v0.6.0.md)。

```bash
boxli build -t demo:v1 .                            # 根据 Boxfile 构建 .boxli 并自动导入
boxli run -p 8080:80 -v data:/data --memory 256 demo:v1   # 端口映射 + 卷挂载 + 内存限制（MiB）
boxli ps                                            # 查看运行中的容器
boxli exec -it demo /bin/sh                         # 进入运行中容器的命名空间执行命令
boxli network ls                                    # 查看容器网络
boxli volume ls                                     # 查看卷
boxli resource info                                 # 查看资源能力（cgroups 等）
boxli stats                                         # 实时查看容器资源用量
```

> 当前已落地：镜像构建（`build`）、`.boxli` 镜像导入（`pull`）、容器运行（`run`）、
> 列出（`ps`）、命名空间执行（`exec`）、网络（`network`）、卷（`volume`）、资源限制
> （`--memory/--cpus/--pids-limit` 等）、镜像产物操作（`tag/commit/save/load/export/import`）、
> Hub 分发（`login/pull/push/search`）与服务端（`hub serve`）、compose 编排
> （`compose up/down/ps/logs/scale/config`）。可执行 `boxli --help` 查看完整命令树。

> ⚠️ v0.4.0 起网络 veth、cgroup 写入、`boxli exec` 需要 **root**（CAP_NET_ADMIN / CAP_SYS_ADMIN）；
> 未实现的资源能力（`--storage`/`--gpu`/`--npu`/`--network-bandwidth`）会显式报错而非静默生效。

## Hub 分发（login / pull / push / search）

Boxli 自研分发服务（不兼容 Docker Distribution API）。先登录，再推送与拉取：

```bash
boxli login                            # 交互式换令牌（--hub 指定服务端，缺省 $BOXLI_HUB 或 http://127.0.0.1:3727）
boxli push alice/myapp:v1 ./myapp.boxli   # 上传本地 .boxli 到 Hub
boxli pull alice/myapp:v1              # 从 Hub 拉取并落地为本地镜像
boxli search myapp                     # 在 Hub 上搜索镜像
```

`boxli pull ./x.boxli` 仍保留本地文件导入语义；Hub 地址按 `--hub` > `$BOXLI_HUB` > `http://127.0.0.1:3727` 顺序解析。

## 构建镜像（build）

根据 Boxfile（`FROM` / `COPY` / `ENV` / `WORKDIR` / `ENTRYPOINT` / `CMD` /
`EXPOSE` / `VOLUME` / `LABEL` / `USER` / `ARG`）构造 `.boxli` 镜像并自动导入本地：

```bash
boxli build -t demo:v1 .                 # 用 ./Boxfile（或 ./boxfile）构建并导入 demo:v1
boxli build -f path/to/Boxfile -t demo:v1 --context ./src
boxli images                              # 看到 demo:v1
```

- **构建上下文必须显式给出**（末尾位置参数或 `--context`，当前目录就传 `.`）：
  上下文决定 `COPY` 的源目录，静默落到 cwd 会把错误的（甚至敏感的）文件打进
  镜像；两者同时给出且不一致会直接报错。

- `FROM scratch` 为空基础镜像；`FROM name:version` 需先在本地存在（或先 `boxli pull`）。
- 未实现的指令（`RUN`、远程 `ADD`）与资源能力会显式报错，不假装成功。

## 容器网络

```bash
boxli run --network boxli0 myapp:v1         # 接入默认 bridge：boxli0（自动分配 IP）
boxli run --network host myapp:v1           # 宿主网络
boxli run --network none myapp:v1           # 无网络
boxli run -p 8080:80 myapp:v1               # 端口映射 HOST:CONTAINER[:PROTO]
boxli network ls / create / inspect / rm     # 网络管理
boxli network connect NETWORK CONTAINER   # 把容器接入某网络
```

## 卷

```bash
boxli run -v /data myapp:v1                  # 匿名卷（自动命名）
boxli run -v myvol:/data myapp:v1            # 命名卷（先 create）
boxli run -v /host/path:/data:ro myapp:v1    # bind 挂载（只读）
boxli volume create / ls / inspect / rm      # 卷管理（local / tmpfs / snapshot）
```

## 资源限制

```bash
boxli run --memory 256 --memory-swap 512 myapp:v1    # 内存（MiB，含软限制 --memory-reservation）
boxli run --cpus 2 --cpuset-cpus 0-3  myapp:v1        # CPU 配额与绑核
boxli run --pids-limit 128 myapp:v1                   # PID 上限
boxli run --gpu 1 --npu 1 myapp:v1                    # 加速器直通
boxli run --storage 1024 --network-bandwidth 10mbps myapp:v1  # 存储配额与带宽（当前显式报错，见已知限制）
boxli resource info      # 资源能力诊断（cgroups v2 / 配额 / 加速器）
boxli stats  [容器ID]     # 实时资源用量
boxli resource update 容器ID --memory 512   # 动态调整运行中容器限制
```

注：资源限制在无权限或非 Linux 平台下列表应用失败时降级为告警（`slog.Warn`），不阻断容器运行。

## 进入运行中容器（exec）

```bash
boxli exec myapp /bin/echo hi               # 在容器命名空间执行命令
boxli exec -it myapp /bin/sh                # 交互式 TTY
boxli exec -e FOO=bar -w /data -u 1000 myapp /bin/env   # 环境变量 / 工作目录 / 用户
```

`exec` 通过 setns 进入容器的 mnt/uts/ipc/net/pid 命名空间后执行命令；
需要 root。交互模式可带 `-i`（保持 stdin）与 `-t`（伪终端）。

> **构建说明**：纯 Go 无法 `setns(CLONE_NEWNS)`（见 Go issue #9091），因此进入容器
> **挂载命名空间**这一步由可选的 cgo 组件 `internal/execns` 完成。这是全仓库唯一的 cgo
> 代码，且仅在 Linux + 启用 cgo 时编译：
>
> - `go build -o boxli .`（默认，Linux + cgo 可用）→ `exec` 功能完整。
> - `CGO_ENABLED=0 go build -o boxli .` 或 `-tags nocgo_exec` → 自动走纯 Go stub，
>   其余功能完全不受影响，只有 `boxli exec` 会返回明确的"未启用 cgo 支持"错误。
> - `GOOS=android` 官方交叉编译为纯 Go，`exec` 同样返回明确错误；需要 Android 上
>   的 `exec` 请用 NDK 工具链做 cgo 交叉编译（见 [docs/android-root.md](docs/android-root.md) 第 2 节）。
> - 交叉编译到 linux/arm64 需要对应架构的 C 工具链；没有时可加 `-tags nocgo_exec`。

## 自建 Hub 服务（hub serve）

```bash
boxli hub serve                                   # 启动分发服务（默认 127.0.0.1:3727，Ctrl+C 关闭）
boxli hub serve --port 9000 --data-dir /data/hub  # 自定义端口与数据目录
boxli hub serve --username alice --password secret # 注册登录用户
```

服务端数据布局：`<root>/tags`、`<root>/blobs/sha256/<hex>`、`<root>/manifests`；
客户端凭证存于 `<root>/hub/auth.json`。`boxli login/push/pull/search` 指向本地
Hub 即可端到端分发镜像（验证步骤见 `docs/hub-e2e.md`）。

## 开机自启

Boxli 没有常驻守护进程。给容器标记重启策略，再开启系统级自启，重启机器后容器会自动拉起：

```bash
boxli run -d --restart always myapp:v1   # always / unless-stopped / no（默认）/ on-failure
boxli boot enable                        # 一键写入并注册系统服务（自动识别平台）
boxli boot status                        # 查看自启状态与自启容器列表
boxli boot disable                       # 移除系统服务
```

开机时系统调用一次 `boxli boot`，拉起 `restart=always` / `unless-stopped` 的容器后立即退出；每个容器由各自的轻量 shim 进程持有生命周期。首次使用 `boxli` 时会引导你开启。

## 支持平台

| 平台 | 支持级别 |
| --- | --- |
| Linux 服务器 | 完整支持 |
| Android 有 Root | 完整支持（native_linux 后端；差异自动适配，见 docs/android-root.md） |
| Android 无 Root | 官方不支持（用户可自行在 proot 等环境中运行，不保证可用性） |
| macOS | 通过轻量 VM |

### 平台能力矩阵（v0.6.0 实测）

| 能力 | Linux（root） | Linux（非 root） | Android（root） | macOS | `CGO_ENABLED=0` 构建 |
| --- | --- | --- | --- | --- | --- |
| 镜像 / 卷 / `build` | ✅ | ✅ | ✅ | ✅ | ✅ |
| `run` / `stop` / `ps` / `rm` | ✅ | ✅ | ✅ | — | ✅ |
| 网络（bridge / veth / NAT / DNS） | ✅ | 仅 host / none | ✅ | — | ✅ |
| 资源限制（cgroup） | ✅ v2 | ⚠️ 视 cgroup 委派而定 | ✅ v2 优先，自动回退 v1 | — | ✅ |
| `exec` 进入命名空间 | ✅ | ❌（需 CAP_SYS_ADMIN） | ✅ 仅 cgo 构建¹ | ❌ | ❌ 返回明确错误 |
| 卷 `:ro` 只读 | ✅ | ✅ | ✅ | — | ✅ |
| 开机自启（boot enable） | ✅ systemd | ✅ systemd（用户级视环境） | ❌ Magisk 后端未实现² | — | ✅ |

¹ 官方 `GOOS=android` 交叉编译是纯 Go，`exec` 返回明确错误；用 NDK 做 cgo 交叉编译后完整可用。
² 临时替代：手动放置 `/data/adb/service.d/boxli.sh`（见 [docs/android-root.md](docs/android-root.md) 第 2 节）。

### 已知限制（v0.6.0）

- **`-p` 端口映射依赖宿主的 FORWARD 链**：若宿主 `iptables` FORWARD 策略为 `DROP`
  且没有放行 boxli 网桥的规则（部分云主机、启用 rootless-docker 的机器如此），
  `-p` 发布的端口从宿主外部不可达。这与 Docker 在同一台机器上的行为一致；此时
  boxli 会打印告警，容器间通信与出网不受影响。可用 `boxli network ls` 确认网桥状态。
- **`exec` 需要 cgo 构建**：见上文《进入运行中容器（exec）》的构建说明。
- **层缓存不回收**：`boxli rm` 只删容器目录，共享的 `layers/sha256/<hex>/fs` 缓存
  跨容器复用且不会自动清理（引用计数尚未实现）。
- **未实现的资源能力显式报错**：`--storage`、`--gpu`、`--npu`、`--network-bandwidth`
  在 CLI 层直接拒绝，不会静默降级。
- **Android**：`boxli boot enable` 尚不生成 Magisk `service.d` 脚本（手动放置可替代）；
  `boxli doctor` 未输出 Android 专项（SELinux 状态等，探测函数已就绪未接线）；
  SELinux **enforcing 真机行为未实测**（无测试机），Boxli 从不调 `setenforce`、
  不改设备策略。详见 [docs/android-root.md](docs/android-root.md) 第 8 节。

### Android 支持策略

- **有 Root**：官方原生支持，走 `native_linux` 后端（namespace + cgroup），
  功能与 Linux 服务器一致。
- **无 Root**：官方不支持。Boxli 不做任何 proot 适配、不检测 proot、不集成
  proot；你可以在 proot / Termux 等用户态 Linux 环境里自行运行 boxli，但官方
  不保证可用性、不提供技术支持。
- **为什么无 Root 不支持**：Android 无 Root 环境缺少容器所需的内核隔离能力
  （namespace / cgroup / setns 等）。任何用户态方案（包括 proot）都只能模拟根
  目录，无法提供真正的进程 / 挂载 / 网络 / 资源隔离——这与 Boxli "真隔离" 的
  容器模型冲突，因此官方不支持。

**安装（有 Root）**：开发机交叉编译 → `adb push` 到 `/data/local/bin/boxli` →
`chmod 0755` → `su` 下运行并建议 `BOXLI_HOME=/data/boxli`：

```bash
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o boxli-android-arm64 .
# 需要 exec 功能时改用 NDK cgo 交叉编译（docs/android-root.md 第 2 节）
```

有 Root 设备上的适配细节（SELinux 处理与限制、cgroup v1/v2 回退与语义差异、
命名空间矩阵、root 下为何不用 user namespace、`/dev` 与 `/proc` 装配、排查指引）
见 [docs/android-root.md](docs/android-root.md)；逐项验收命令与期望输出见
[docs/android-verify.md](docs/android-verify.md)。

## 镜像格式：.boxli

`.boxli` 文件是一个自研容器镜像包：内部由若干**分层 gzip tar** 组成，附一份自研 **`index.json`** 描述层顺序、架构与元数据。

⚠️ **与 Docker 不兼容**：`.boxli` 不能由 Docker 构建或运行，`docker` 镜像也不能被 Boxli 使用。Boxli 配套自己的镜像构建与分发工具链（`boxli hub`），生态完全独立。

## 开发命令

```bash
go build -o boxli .   # 根目录编译
go vet ./...          # 静态检查
gofmt -l .            # 格式检查
go test ./...         # 测试
```

## 开源协议

AGPL-3.0-only，详见 [LICENSE](LICENSE)。
