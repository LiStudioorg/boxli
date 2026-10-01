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
│   ├── store/           # 数据目录（~/.boxli）：pull 落地、state.json、boot 标记
│   └── resource/        # 资源限制与采集：CPU / 内存 / PID
├── pkg/
│   └── sdk/             # 对外 Go SDK，供第三方以库方式驱动 Boxli
├── docs/
│   └── image-spec.md    # .boxli 镜像格式规范（单一事实来源，改格式先改这里）
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

## 开机自启动机制

Boxli **不采用全局常驻守护进程**。开机自启 = 一个**全局一次性系统服务** + **容器自身的 restart 策略**：

- 系统里只生成**一个** Boxli 服务文件。
- 开机时系统调用一次 `boxli boot`。
- `boxli boot` 扫描容器状态文件，拉起设置了自启的容器，**执行完即退出，不常驻**。
- 每个容器的生命周期由一个轻量 **shim 进程**持有（类似 Podman 的 conmon），引擎本体不常驻。

### 容器自启动标志（restart 策略）

创建容器时指定：

```bash
boxli run -d --restart always         alice/myapp:v1
boxli run -d --restart unless-stopped alice/myapp:v1
boxli run -d --restart no             alice/myapp:v1
```

| 策略 | 行为 |
| --- | --- |
| `no`（默认） | 开机不自动启动 |
| `always` | 容器退出就重启；开机自动启动 |
| `unless-stopped` | 类似 always，但用户手动 `stop` 后开机不再拉起 |
| `on-failure` | 非零退出码才被 shim 重启；**开机不自动启动** |

### 一键配置系统服务（boxli boot enable/disable/status）

用户只需一条命令，无需手写服务文件。`boxli boot enable` 自动检测平台并完成配置：

| 平台 | 服务文件 | 注册方式 |
| --- | --- | --- |
| Linux | `/etc/systemd/system/boxli.service` | `systemctl daemon-reload && systemctl enable boxli` |
| macOS | `~/Library/LaunchAgents/dev.boxli.boot.plist` | `launchctl load` |
| Android（Root） | `/data/adb/service.d/boxli.sh`（赋执行权限） | Magisk service.d |
| Android（无 Root） | `~/.termux/boot/boxli.sh` | 检测 Termux:Boot，未安装时提示用户 |

- `boxli boot enable`：写入服务文件并注册，完成后输出服务文件路径与状态。
- `boxli boot disable`：自动移除对应平台的系统服务文件并取消注册。
- `boxli boot status`：显示开机自启是否启用、服务类型、服务文件路径；列出所有设置了 `restart=always` / `unless-stopped` 的容器及其状态。
- `boxli boot`：由系统服务在开机时调用的一次性命令。扫描所有容器状态文件 → 启动 `restart=always` 或 `unless-stopped` 的容器 → 跳过标记 `stopped-by-user` 的 `unless-stopped` 容器 → 每个容器 fork 一个轻量 shim → 退出。

### 首次使用引导

用户首次执行任意 `boxli` 命令时，若检测到未启用开机自启，提示：

```text
检测到 Boxli 尚未启用开机自启
是否启用？启用后开机会自动拉起设置了 restart=always 的容器
[y/N]:
```

用户确认后自动执行 `boxli boot enable`。默认（直接回车）视为拒绝。

### 停止容器时的状态记录

`boxli stop myapp`：停止容器 → 标记 `stopped-by-user`。下次开机时 `unless-stopped` 的容器不再被拉起；`always` 的容器仍会被拉起（与 Docker 行为一致）。

### systemd 服务文件内容（Linux）

```ini
[Unit]
Description=Boxli container engine
After=network.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/bin/boxli boot
ExecStop=/usr/local/bin/boxli shutdown

[Install]
WantedBy=multi-user.target
```

### CLI 命令汇总（boot 相关）

```text
boxli boot enable    启用开机自启
boxli boot disable   关闭开机自启
boxli boot status    查看开机自启状态与自启容器列表
boxli boot           由系统服务在开机时调用，一次性拉起自启容器
boxli shutdown       由系统服务停止时调用，优雅停止自启容器
```

## 代码规矩

- **错误处理**：错误必须包装上下文后再向上返回：`fmt.Errorf("load index: %w", err)`；只在 `main.go` / CLI 出口层打印，中间层只 `return`。忽略错误必须显式 `_ =`。
- **日志**：统一使用标准库 `log/slog`，结构化字段（`slog.String("container", id)` 等）；禁止 `fmt.Println` 打日志、禁止引入第三方日志库。
- **CLI**：使用 [cobra](https://github.com/spf13/cobra) 组织命令树（`pull` / `run` / `ps` / `exec` / `images` / `boot [enable|disable|status]` / `shutdown`；`run` 支持 `--restart no|always|unless-stopped|on-failure`）。命令注册代码全部在 `internal/cli`，未实现命令统一返回"尚未实现"。隐藏命令（`init` / `dev-run`）仅内部与开发用途，不在帮助中展示。
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

## 镜像格式：唯一规范在 docs/image-spec.md

`.boxli` = 外层未压缩 tar（内含 `index.json` + `layers/NNNNNN.<name>.tar.gz` + 可选 `blobs/`）。

- 规范文档：[docs/image-spec.md](docs/image-spec.md)，它是镜像格式的**单一事实来源**。
- 任何格式改动必须**先改规范、再改代码**，且只允许通过 `specVersion` 做不兼容升级。
- 解析器实现位于 `internal/image`，必须实现规范第 4 节全部拒绝规则，禁止"尽力猜测"式宽容解析。
- digest 仅允许 `sha256:`；`index.json` 是唯一元数据源。

## 依赖白名单

`go.mod` 中的第三方依赖需要逐条批准，白名单如下：

| 依赖 | 用途 | 批准范围 |
| --- | --- | --- |
| `github.com/spf13/cobra` | CLI 命令树 | 只允许 `internal/cli` 包引用（main.go 仅调 `cli.Execute`） |

除此之外的第三方依赖一律不批；容器 / 镜像 / OCI / cgroups 相关库永久禁止（见"禁止事项"）。日志、配置、压缩、归档一律用标准库（`log/slog`、`archive/tar`、`compress/gzip`、`encoding/json`、`crypto/sha256`）。

## 当前阶段：阶段 2

阶段 0 已完成：目录骨架、`go.mod`、文档、占位包，并已发布 `v0.1.0` 被 pkg.go.dev 收录。

**阶段 1 已完成**：`.boxli` 镜像格式定义（docs/image-spec.md）、cobra CLI 骨架、`internal/image` 清单解析器、`internal/store` 落地存储、`boxli pull` 本地 `.boxli` 文件支持（`boxli run` / `ps` / `exec` / `boot` / `shutdown` 为骨架占位，明确返回未实现）。

**阶段 2 进行中**：Linux 原生运行时 spike 已完成——`internal/runtime`（native_linux）实现纯 Go 的 namespace + pivot_root 容器（rootless 自动 user namespace），`boxli init`（隐藏命令）为容器 1 号进程入口，`boxli dev-run`（隐藏命令）为开发/基准入口；实测每容器 ≈ 2.3 MiB，报告见 [docs/runtime-benchmark.md](docs/runtime-benchmark.md)。`internal/storage` 层解包器已完成——`UnpackFile` 内容寻址解包（layers/sha256/<hex>/fs），`MergeLayers` 按序合并（whiteout/opaque 删除语义、符号链接逃逸防护、设备节点与 setuid 剥离、并发安全）；测试覆盖路径逃逸、重复条目、损坏 gzip、opaque 符号链接防护等场景。剩余：`boxli images`、`boxli boot enable` 与 shim 落地、`boxli run` 整合（pull→unpack→merge→start 端到端）。
