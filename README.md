# Boxli

Boxli 是一个用 Go 编写的轻量级容器引擎：常驻内存 10–20 MiB，单二进制分发，覆盖 Linux / Android / macOS —— 但它是**完全自研生态，不兼容 Docker / OCI**。

## 特性

- 🪶 **极轻**：运行时内存目标 10–20 MiB，纯 Go 无 CGO，单个静态二进制。
- 🧩 **自研镜像格式**：`.boxli` = 分层 gzip tar + 自研 `index.json`，简单、可逐层审计。
- 📱 **全平台**：Linux 服务器、Android（有 Root / 无 Root）、macOS。
- 🏗 **多架构**：amd64 / arm64 / 386 / riscv64 等同一条命令交叉编译。
- 🔌 **零外部依赖**：不需要 Docker、containerd 或任何 OCI 组件，装一个 `boxli` 就能用。
- 🛡 **资源限制**：CPU / 内存 / PID 限额内置于引擎。
- 🚀 **开机自启**：一条 `boxli boot enable` 完成系统服务配置，无全局守护进程，每个容器由轻量 shim 独立守护。

## 快速开始

> Boxli 已进入 **阶段 3 收官（v0.3.0）**：镜像、运行时、网络、卷、资源限制、Hub 分发均已落地。除标注"开发中"的命令外均为当前真实能力。

```bash
boxli pull ./myapp-1.0.boxli                           # 导入本地 .boxli 镜像文件
boxli run -p 8080:80 -v data:/data --memory 256 myapp:v1   # 端口映射 + 卷挂载 + 内存限制（MiB）
boxli ps                                               # 查看运行中的容器
boxli network ls                                       # 查看容器网络
boxli volume ls                                        # 查看卷
boxli resource info                                    # 查看资源能力（cgroups/GPU 等）
boxli stats                                            # 实时查看容器资源用量
```

> 当前已落地：`.boxli` 镜像导入（`pull`）、容器运行（`run`）、列出（`ps`）、网络（`network`）、卷（`volume`）、资源限制（`--memory/--cpus/--pids-limit` 等）、镜像产物操作（`tag/commit/save/load/export/import`）、以及 Hub 分发（`login/pull/push/search`）。可执行 `boxli --help` 查看完整命令树。

## Hub 分发（login / pull / push / search）

Boxli 自研分发服务（不兼容 Docker Distribution API）。先登录，再推送与拉取：

```bash
boxli login                            # 交互式换令牌（--hub 指定服务端，缺省 $BOXLI_HUB 或 http://127.0.0.1:3727）
boxli push alice/myapp:v1 ./myapp.boxli   # 上传本地 .boxli 到 Hub
boxli pull alice/myapp:v1              # 从 Hub 拉取并落地为本地镜像
boxli search myapp                     # 在 Hub 上搜索镜像
```

`boxli pull ./x.boxli` 仍保留本地文件导入语义；Hub 地址按 `--hub` > `$BOXLI_HUB` > `http://127.0.0.1:3727` 顺序解析。

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
boxli run --storage 1g --network-bandwidth 10mbps myapp:v1   # 存储配额与带宽
boxli resource info      # 资源能力诊断（cgroups v2 / 配额 / 加速器）
boxli stats  [容器ID]     # 实时资源用量
boxli resource update 容器ID --memory 512   # 动态调整运行中容器限制
```

注：资源限制在无权限或非 Linux 平台下列表应用失败时降级为告警（`slog.Warn`），不阻断容器运行。

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

| 平台 | 运行路线 | 状态 |
| --- | --- | --- |
| Linux 服务器（amd64 / arm64 / riscv64…） | 原生 namespace/cgroups | 开发中 |
| Android（有 Root） | 原生路线 | 开发中 |
| Android（无 Root） | proot 路线 | 开发中 |
| macOS | 轻量虚拟机 | 计划中 |

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
