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

**阶段 2 进行中**：Linux 原生运行时 spike 已完成——`internal/runtime`（native_linux）实现纯 Go 的 namespace + pivot_root 容器（rootless 自动 user namespace），`boxli init`（隐藏命令）为容器 1 号进程入口，`boxli dev-run`（隐藏命令）为开发/基准入口；实测每容器 ≈ 2.3 MiB，报告见 [docs/runtime-benchmark.md](docs/runtime-benchmark.md)。`internal/storage` 层解包器已完成——`UnpackFile` 内容寻址解包（layers/sha256/<hex>/fs），`MergeLayers` 按序合并（whiteout/opaque 删除语义、符号链接逃逸防护、设备节点与 setuid 剥离、并发安全）；测试覆盖路径逃逸、重复条目、损坏 gzip、opaque 符号链接防护等场景。`boxli images` 已完成——`store.ListImages` 扫描 state.json（损坏条目跳过并告警），输出 REPOSITORY/TAG/ARCH/LAYERS/SIZE/CREATED 按导入时间倒序，支持 `-q` 与 `--format` Go 模板，空 store 友好提示且退出码 0。boot/shim 体系已完成——`<root>/containers/<id>/` 状态目录（config.json + runtime.json + stopped-by-user 标记）为 run/boot/shim 共用地基；`internal/shim` 为每容器生命周期持有者（Reexec 重执行 + setsid 脱终端 + container.log，restart 策略循环与退避重启）；`internal/boot.StartAll` 实现 `boxli boot` 一次性扫描拉起（策略矩阵 + 停止标记 + 幂等防重）；`internal/service` 管理 systemd unit（enable/disable/status，无 systemd 或无权限时明确提示并给出 sudo 手动命令）；`boxli shutdown` 经 SIGTERM shim 优雅停机；首次使用引导接入真实 boot enable。剩余：`boxli run` / `boxli stop` / `boxli ps` / `boxli rm` 整合已完成（阶段 2 收官）。`internal/engine` 为一次 run 的编排层（镜像查找 → 每层解包 → rootfs 合并 → 容器状态落盘 → 前台持有或后台 fork shim），CLI 只做参数绑定；`boxli run` 默认前台 stdio 直连、Ctrl+C 经 StopCh 转发容器、退出码透传 shell，`-d` 后台 fork shim 并打印容器 ID；`--name` 缺省自动生成 `adjective_animal` 式名字并去重；`-p`/`-v`/`--memory`/`--cpus`/`--pids-limit` 已解析并记入容器配置、运行时忽略并 `slog.Warn`（阶段 3 落地）。`boxli stop` 先写 `stopped-by-user` 标记再 SIGTERM shim，超时强杀并补写终态，已停止容器幂等；默认宽限为 `shim.GraceHold+5s`，小于该值会与 shim 写终态竞态导致退出码丢失。`boxli ps` 默认仅列运行中容器，`-a` 含已停止，`-q` 只出 ID，状态列区分 Up/Exited/Created 并标注 `user-stopped`。`boxli rm` 删除已停止容器整目录（含该容器独占的 rootfs，共享层缓存保留），运行中拒绝并提示先 stop，`-f` 先停再删。另修复 `boot.PidAlive` 真实缺陷：僵尸进程对 `signal 0` 仍探活成功，僵死 shim 会被 boot 误判为“已在运行”而永不重启，现读 `/proc/<pid>/stat` 判僵尸态。剩余（阶段 3）：`boxli exec`、资源限制（memory/cpus/pids）实际生效、`-p` 端口映射与 `-v` 卷挂载、`internal/network` 与 `internal/resource`。

## 冻结接口（阶段 2 收官）

以下接口自 `boxli run` 端到端跑通（阶段 2 收官）起**冻结**：签名、语义与哨兵错误均视为稳定契约。
多模块并行开发期间，**修改任一冻结接口必须先提 issue 讨论**，说明动机、兼容性影响与迁移方案，
达成一致后再动代码；禁止在业务分支里顺手改签名。只读使用不受限制。

新增接口（不改动既有签名）不需要 issue，但仍应在本节登记，保持本节为接口的单一索引。

### 一、Runtime（`internal/runtime`）

```go
// 启动
func Start(cfg *Config, onChildStart func(pid int)) (*StartResult, error)
func StartWith(cfg *Config, onChildStart func(pid int), opts *StartOptions) (*StartResult, error)

// 容器 1 号进程入口与分流
func RunInit() error
func IsInitProcess() bool

// 类型
type Config struct {
    Rootfs   string   // 容器新根（宿主机路径，必须已存在）
    Hostname string   // 容器 UTS 名
    Cmd      []string // 1 号进程 argv，必填
    Env      []string // KEY=VALUE
    Rootless bool     // 强制 user namespace；false 时按 euid 自动判定
}
func (c *Config) Validate() error

type StartOptions struct {
    Stdin, Stdout, Stderr *os.File     // nil → os.Stdin/os.Stdout
    StopCh                <-chan struct{} // 可读即向 init 转发 SIGTERM
    Grace                 time.Duration    // SIGTERM→SIGKILL 宽限，默认 10s
}
type StartResult struct {
    ChildPID int // 容器 init 在宿主上的 PID
    ExitCode int // 信号死亡时 = 128+signum
}

// 哨兵
ErrNotInit, ErrBadConfig, ErrNotRoot, ErrUnsupported
```

- **没有 `Stop` 函数**：停止容器由 `StartOptions.StopCh` 驱动（收到可读即 SIGTERM，`Grace` 后 SIGKILL）；
  面向用户的停止编排在 `internal/engine.Stop`（写 `stopped-by-user` 标记后 SIGTERM shim）。
- 平台后端以 build tag 分文件实现同一组签名（`*_linux.go` / `*_android.go` / `*_darwin.go`）；
  非 Linux 后端必须提供同名 stub 以保证全仓库可交叉编译。
- `Start` 与 `StartWith` 的分工：`Start` 是 `StartWith(cfg, onChildStart, nil)` 的简写，两者都必须保留。

### 二、Store（`internal/store`）

```go
// 数据目录
func Open(root string) (*Store, error) // root 为空 → $BOXLI_HOME → ~/.boxli

// 容器状态目录（config.json + runtime.json + stopped-by-user + rootfs）
func NewContainerID() (string, error)
func (s *Store) CreateContainer(cfg *ContainerConfig) error
func (s *Store) LoadContainer(id string) (*ContainerConfig, error)
func (s *Store) FindContainer(idOrName string) (*ContainerConfig, error) // ID 前缀或名字；歧义报错
func (s *Store) ListContainers() ([]*ContainerConfig, error)            // 创建时间倒序
func (s *Store) ContainerNames() (map[string]bool, error)
func (s *Store) ContainerDir(id string) string
func (s *Store) ContainersRoot() string
func (s *Store) WriteRuntimeState(id string, st *RuntimeState) error
func (s *Store) ReadRuntimeState(id string) (*RuntimeState, bool, error)
func (s *Store) MarkStoppedByUser(id string) error
func (s *Store) ClearStoppedByUser(id string) error
func (s *Store) IsStoppedByUser(id string) bool
func (s *Store) BootEligible(cfg *ContainerConfig) bool

// 镜像落地
func (s *Store) Put(srcPath string, force bool) (*image.Loaded, error)
func (s *Store) Exists(name, version string) (bool, error)
func (s *Store) ReadState(name, version string) (*State, error)
func (s *Store) ListImages() ([]ImageInfo, error)
func (s *Store) ImageDir(name, version string) string
func (s *Store) ImagesRoot() string

// boot 标记
func (s *Store) BootMarker() string
func (s *Store) EnsureBootDir() error

type Restart string // RestartNo | RestartAlways | RestartUnlessStoped | RestartOnFailure
func (r Restart) Valid() bool
func (r Restart) BootEligible() bool

// 哨兵
ErrExists, ErrContainerExists, ErrContainerNotFound, ErrBadContainerConfig
```

- `ContainerConfig` 的 JSON 字段为 `run`/`boot`/`shim`/`ps` 共用契约；**新增字段必须 omitempty**，
  且旧版本读新配置不得失败（向后兼容是硬要求）。
- 文件写入一律"临时文件 + rename"原子替换；容器目录内的临时文件必须与目标同目录（跨设备 rename 报 EXDEV）。
- `RuntimeState` 的写方只有 shim（前台模式下是持有容器的 CLI 进程）；其他模块只读。

### 三、Image（`internal/image`）

```go
// 清单解析（严格解析，未知字段一律拒绝，禁"尽力猜测"）
func ParseManifest(data []byte) (*Manifest, error) // index.json
func ParseConfig(data []byte) (*Config, error)     // config blob
func (m *Manifest) Validate() error
func (m *Manifest) Ref() string // name:version

// 归档打开与校验
func OpenFile(path string) (*Loaded, error)
func (l *Loaded) VerifyLayers() error
func (l *Loaded) CheckPlatform() error
func (l *Loaded) Entry(name string) (EntryInfo, bool)
func (l *Loaded) ExtractFile(name, dst string) error

// 路径安全（规范第 4 节）
func SafeArchivePath(name string) error

// 常量
IndexName, BlobsDir, MediaTypeManifest

// 哨兵
ErrBadManifest, ErrUnsafePath, ErrLayerMissing, ErrConfigMissing,
ErrSizeMismatch, ErrDigestMismatch, ErrBadApplyOrder, ErrArchMismatch,
ErrUnsafeLayer, ErrIndexTooLarge
```

- **没有 `ParseIndex`**：`index.json` 的解析入口是 `ParseManifest`（早期草案名，已废弃）。
- 任何格式改动必须**先改 `docs/image-spec.md`、再改代码**，且只允许通过 `specVersion` 做不兼容升级。
- `OpenFile` 只做清单类/结构类/config blob 校验；层全量摘要由 `VerifyLayers` 重算（流式，内存 O(1)）。
  `boxli run` 走的是"信任 pull 期已校验"，不重复 `VerifyLayers`。

### 四、Storage（`internal/storage`）

```go
// 内容寻址层存储：<storeRoot>/layers/sha256/<hex>/fs
func UnpackFile(layerPath, wantDigest, storeRoot string) (*UnpackResult, error)
func MergeLayers(storeRoot string, orderedDigests []string, targetDir string) error
func LayerUnpacked(storeRoot, hexDigest string) bool
func LayerFSDir(storeRoot, hexDigest string) string
func LayersRoot(storeRoot string) string

type UnpackResult struct {
    DigestHex string
    FSDir     string
}

// 哨兵
ErrCorruptLayer, ErrDuplicateEntry, ErrBadDigest, ErrLayerMissingLocal
```

- 摘要参数形态固定：`UnpackFile`/`MergeLayers` 接受 64 位十六进制（`sha256:` 前缀可带可不带），
  `LayerUnpacked`/`LayerFSDir` 只接受裸十六进制。
- `UnpackFile` 是**差异视图**：whiteout 文件原样保留，删除语义由 `MergeLayers` 应用；两者职责不可混淆。
- `MergeLayers` 的 `targetDir` 可以不存在（内部 MkdirAll）；合并是"叠加拷贝"，同一容器重复合并不幂等，
  调用方须保证目标是全新目录。
- 层缓存**跨容器共享、永不随容器删除而回收**（引用计数是阶段 3 项）；`boxli rm` 只删容器目录。

### 五、Shim（`internal/shim`）

```go
// 生命周期
func Reexec(storeRoot, id string) (*os.Process, error) // setsid 脱终端，日志追加 container.log
func Run(ctx context.Context, o *Options) error        // 主循环：启动 → 写状态 → 按策略重启或退出
func RunFromEnv(ctx context.Context) error             // main 分流入口
func IsShimProcess() bool
func LogPath(storeRoot, id string) string

type Options struct {
    Store        *store.Store
    Cfg          *store.ContainerConfig
    Stdin, Stdout, Stderr *os.File // 前台模式由持有容器的进程提供
    OnStart      func(pid int)     // init 起来后的回调，用于尽早落状态
}

// 常量
EnvMarker    = "BOXLI_SHIM"
EnvStoreRoot = "BOXLI_STORE_ROOT"
EnvContainer = "BOXLI_CONTAINER"
GraceHold    = 10 * time.Second // 导出：stop 的宽限必须大于它

// 哨兵
ErrShimNotRequested
```

- `GraceHold` 是**跨模块契约**：`engine.Stop` 的默认超时派生为 `GraceHold+5s`。任何调小它的改动
  都会重新引入"stop 抢在 shim 写终态前强杀导致退出码丢失"的竞态，必须同步评估调用方。
- shim 是 `runtime.json` 的唯一写方（含前台模式下由 CLI 进程充当 shim 的场景）。
- 重启退避序列固定为 1s/2s/4s/8s/30s（封顶 30s）；改动需同步 `boxli boot` 的幂等判定窗口评估。

## 并行开发约定

阶段 3 起多模块并行推进，约定如下。

### 分支与模块边界

每个模块一个独立分支，只允许改自己模块的目录与其 `_test.go`：

| 分支 | 允许修改 | 职责边界 |
| --- | --- | --- |
| `feat/network` | `internal/network/`（+ `*_test.go`） | bridge、veth、端口映射、DNS |
| `feat/volume` | `internal/storage/volume/`（+ `*_test.go`） | 卷、驱动、配额 |
| `feat/resource` | `internal/resource/`（+ `*_test.go`） | cgroup、GPU、IO |
| `feat/cli` | `internal/cli/`、`internal/engine/`（+ `*_test.go`） | 新命令、参数、输出 |
| `feat/hub` | `hub/`（+ `*_test.go`） | 仓库服务端 |

### 硬性禁止

1. **禁止修改 AGENTS.md、go.mod、main.go**，除非该模块明确需要且已在 issue 中说明理由。
   `go.mod` 新增第三方依赖一律需要单独批准（见"依赖白名单"）。
2. **禁止修改其他模块的接口签名**（上节"冻结接口"列出的全部符号）。需要新能力时，
   在**自己的模块内**定义窄接口并依赖它，不要反向改动对方包。
3. **禁止跨模块目录写入**：一个 PR 只碰自己那一列的路径。跨模块改动必须拆成多个 PR，
   由对应模块分支分别提交。
4. 新增顶层目录前先在"目录结构"一节登记（`internal/network` 与 `internal/resource` 已登记，
   `internal/storage/volume`、`hub/` 需在各自首个 PR 中补登记）。

### 跨模块协作方式

- 需要用别人的能力时，**依赖已冻结的具体函数**（当前内部包之间就是这样直连的），
  或者在自己模块里声明小接口由调用方注入。冻结接口已经足够支撑阶段 3，预期不需要新的跨模块缝。
- 共享的磁盘布局（`<root>/containers/<id>/`、`<root>/layers/sha256/<hex>/`、`<root>/images/`）
  是事实契约：新模块只允许**新增**子路径，不得改变既有文件名与语义。
- 每个模块的 PR 必须自带：`gofmt -l .` 为空、`go vet ./...` 通过、`go test ./...` 全绿、
  三平台交叉编译通过（linux/android/darwin amd64+arm64）。
- 发现冻结接口有缺陷时：**先提 issue，再改 AGENTS.md，最后改代码**；不要在自己的分支里
  悄悄放宽或绕过它。
