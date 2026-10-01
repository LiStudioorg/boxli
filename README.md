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

> Boxli 处于阶段 0（项目初始化），以下命令展示的是目标体验，功能将随版本逐步可用。

```bash
boxli pull hub.boxli.dev/library/alpine:3.20.boxli   # 拉取一个 .boxli 镜像
boxli run alpine:3.20 -- /bin/sh                     # 进入容器交互终端
boxli ps                                             # 查看运行中的容器
boxli exec <容器ID> cat /etc/os-release              # 在运行中的容器里执行命令
```

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
