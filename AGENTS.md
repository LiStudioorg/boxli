# Boxli — AI 协作者指南

本文件面向参与 Boxli 开发的 AI 代理与人类协作者，描述项目定位、核心约定与代码规矩。开始动手前请先通读本文件。

## 项目是什么

Boxli 是一个用 Go 编写的**轻量级容器引擎**，使用场景类似 Docker，但**不兼容 Docker / OCI，完全自研生态**：自研镜像格式、自研分发方式、自研运行时与网络。目标平台为 Linux 服务器、Android（有 Root / 无 Root）、macOS；支持多 CPU 架构；引擎常驻内存目标 **10–20 MiB**。

## 核心约定

- **语言**：Go，纯 Go，**不使用 CGO**（`CGO_ENABLED=0`）。
- **模块路径**：`github.com/LiStudioorg/boxli`。
- **可执行文件**：`boxli`；`main.go` 位于项目根目录，便于在根目录直接 `go build`。
- **镜像后缀**：`.boxli`。
- **镜像格式**：分层 gzip tar + 自研 `index.json` 清单，与 Docker / OCI 镜像**互不兼容**。
- **开源协议**：AGPL-3.0-only（见 `LICENSE`）。每个 `.go` 文件头部必须带版权声明：

  ```go
  // Copyright (C) 2026 LiStudioorg
  // SPDX-License-Identifier: AGPL-3.0-only
  ```

- **零外部容器组件**：不依赖 Docker、containerd、runc 及任何 OCI/runc/cgroups 库。

## 目录结构

```
boxli/
├── main.go              # CLI 入口（根目录，直接 go build 即可编译）
├── internal/
│   ├── runtime/         # 容器运行时：创建 / 启动 / 停止 / 回收，按平台后端分文件
│   ├── image/           # .boxli 镜像的拉取、解析、校验（分层 gzip tar + index.json）
│   ├── network/         # 自研容器网络：容器间通信与 NAT 出口
│   ├── storage/         # 镜像与容器层存储：解压、层合并、读写层
│   └── resource/        # 资源限制与采集：CPU / 内存 / PID
├── pkg/
│   └── sdk/             # 对外 Go SDK，供第三方以库方式驱动 Boxli
├── go.mod
├── LICENSE              # AGPL-3.0
├── AGENTS.md
└── README.md
```

- `internal/` 下的包不对外暴露；只有 `pkg/sdk` 是公开 API，改动须保持向后兼容。
- 新增顶层目录前先在这里登记，避免结构漂移。

## 平台后端：build tags 分文件

运行时按平台后端拆分，同一接口、多套实现，用 Go build tags 分文件，**禁止**在公共代码里散落 `runtime.GOOS` 判断：

| 文件后缀 | build tag | 适用平台 |
| --- | --- | --- |
| `*_linux.go` | `//go:build linux`（后端标记 `native_linux`） | Linux 服务器、有 Root 的 Android：原生 namespace/cgroups 路线 |
| `*_android.go` | `//go:build android && !cgo`（后端标记 `proot_android`） | 无 Root 的 Android：proot 路线 |
| `*_darwin.go` | `//go:build darwin`（后端标记 `vm_darwin`） | macOS：轻量虚拟机路线 |

约定：每个后端实现同一组内部接口，公共层只依赖接口；新平台 = 新 tag + 新文件，不改公共代码。

## 代码规矩

- **错误处理**：错误必须包装上下文后再向上返回：`fmt.Errorf("load index: %w", err)`；只在 `main.go` / CLI 出口层打印，中间层只 `return`。忽略错误必须显式 `_ =`。
- **日志**：统一使用标准库 `log/slog`，结构化字段（`slog.String("container", id)` 等）；禁止 `fmt.Println` 打日志、禁止引入第三方日志库。
- **CLI**：使用 [cobra](https://github.com/spf13/cobra) 组织命令树（`pull` / `run` / `ps` / `exec` / `rm` / `images`）。阶段 0 尚未引入依赖；正式引入 cobra 时单独提交，只进 `main.go` 与命令注册代码。
- **配置**：一律 YAML（`~/.boxli/config.yaml` 及镜像 `index.json` 旁挂配置），字段用 `yaml` tag 显式命名；不要混用 TOML/JSON 配置文件（`index.json` 属于镜像格式，不算配置文件）。
- **依赖**：阶段 0 `go.mod` 保持零第三方依赖；新增第三方库必须在 PR 里单独说明理由，容器/镜像/oci 相关的库一律不批。
- **命名与注释**：导出标识符必须有文档注释；文件头保留 AGPL 版权声明两行。

## 禁止事项

1. **禁止**引入任何第三方容器组件 / 容器库（Docker、containerd、runc、buildkit、OCI 相关库、cgroups 库等）——容器生态完全自研。
2. **禁止**做任何形式的 Docker / OCI 兼容（不做镜像格式转换、不实现Distribution API），Boxli 只认 `.boxli`。
3. **无 Root Android 只允许 proot 路线**，不得尝试 ptrace 之外的特权方案或引导用户提权。
4. **禁止** CGO。
5. **禁止**在运行时引入常驻守护进程设计（引擎以单二进制按需执行为目标，服务化另立 RFC）。
6. **禁止**未经文档约定就新增顶层目录或改变 `pkg/sdk` 公开 API。

## 常用命令

```bash
go build -o boxli .        # 在根目录编译，产出 ./boxli
go vet ./...               # 静态检查
gofmt -l .                 # 格式化检查（输出应为空）
go test ./...              # 运行测试
go run .                   # 快速跑一下 CLI
# 交叉编译示例：
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o boxli-android-arm64 .
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o boxli-darwin-arm64 .
```

## 当前阶段：阶段 0

阶段 0 仅做项目初始化：目录骨架、`go.mod`（零依赖）、文档与占位包。`main.go` 只打印版本号，**尚无任何容器运行时代码**。下一阶段（阶段 1）目标：`.boxli` 镜像格式定义与 `boxli pull` / `boxli images`。
