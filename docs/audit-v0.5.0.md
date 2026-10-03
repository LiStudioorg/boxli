# LiCore v0.5.0 审计报告

记录从 v0.4.0 到 v0.5.0 对"半成品"的全面扫描、补齐情况、剩下未实现项与环境限制。

## 1. 半成品扫描清单与处置

| # | 半成品 | 处置 |
| --- | --- | --- |
| 1 | `licore build` 只输出计划，未调 `build.Build()` | ✅ **完成**：真正构建并自动导入 store（`-t/--tag`、`-f/--file`、上下文、`FROM scratch`） |
| 2 | `compose up` 打印"编排计划"不创建容器 | ✅ **完成**：经 `engine.Run` 真实创建（boxfile/build 服务就地构建导入；image 服务直接运行） |
| 3 | `compose scale` 输出目标副本数不改实际 | ✅ **完成**：按目标增启/缩容副本 |
| 4 | `--storage` 配额：`resource.writeStorageAndDevices` 空实现 | ✅ **转为显式拒绝**（user 请求时返回 ErrUnsupported） |
| 5 | `--network-bandwidth`：`writeNetworkBandwidth` 返回 nil 空转 | ✅ **转为显式拒绝** |
| 6 | `--gpu/--npu`：`writeDevices` 写不存在文件、忽略错误"假装生效" | ✅ **转为显式拒绝** |
| 7 | `-p` 在 host/none 网络"警告后忽略" | ✅ **转为显式错误** |
| 8 | `engine.go` / `devrun.go` 陈旧"阶段 3 占位 / run 尚未实现"注释 | ✅ **更新** |
| 9 | `resource info / update` 广告未实现的 `--storage/--gpu/--npu/--bandwidth` | ✅ **改为只列已实现项；update 移除未实现 flag** |
| 10 | `licore build/test 无缓存/精简` 语义未兑现 | `--no-cache`/`--slim` 保留为明确提示的 no-op（构建本就每次重打追加层，无缓存）；已注释说明 |

## 2. 仍未实现（明确返回错误 / 未来阶段，不做伪装）

| 项 | 状态 | 说明 |
| --- | --- | --- |
| Boxfile `RUN` 指令 | 返回 `ErrBadInstruction`"尚未实现" | 需在构建期运行容器（buildkit 式） |
| Boxfile `ADD`（远程 URL） | 返回 `ErrBadInstruction`"尚未实现" | 需拉取远程 tar |
| `--gpu/--npu` 设备直通 | 显式拒绝 | 需 cgroup v2 eBPF devices 程序 |
| `--network-bandwidth` | 显式拒绝 | 需 tc/HTB 流量整形 |
| `--storage` 配额 | 显式拒绝 | 需 XFS project quota |
| `hub serve --storage s3` | 显式拒绝（仅 local） | S3 驱动需 SigV4/对象存储客户端 |
| 非 Linux 后端（Android 无 root proot、macOS VM） | 跨平台 stub 返回 ErrUnsupported | 未来平台后端 |

以上均"传入即明确报错"，绝无"接受参数但假装成功"。

## 3. 修复的 bug

| 严重度 | 位置 | 原因 | 修复 |
| --- | --- | --- | --- |
| 高 | `resource.write*`（--storage/--gpu/--npu/--bandwidth） | 静默 no-op，用户以为已生效 | 改显式 `ErrUnsupported`；CLI 层 fail-fast |
| 中 | `compose up/scale` | 占位，不创建容器 | 接线 `engine.Run` 与副本管理 |

## 4. 测试与静态检查

- `go vet ./...`、`gofmt -l`、`go test ./... -count=1` 全绿；
- `go test ./... -race -count=2` 无竞态；
- linux/amd64、linux/arm64、android/arm64、darwin/arm64 全编译通过；
- 新增：`build` 的 Name/Version tag 覆盖、`runLimits` 未实现能力拒绝测试。

## 5. 端到端验证结果

- **场景 A（build）**：本环境实测通过。
- **场景 G（hub）**：本环境实测通过（serve→login→push→search→pull）。
- **场景 B–F（容器 veth/cgroup/exec/卷）**：代码就绪+单测；**需 root 真机**。
  自动化沙箱为非 root 且禁止 fork/re-exec（landlock），无法运行任何容器，
  故 B–F 只能由 root 真机按 `docs/e2e-v0.5.0.md` 验收。

## 6. 遗留问题 / 环境说明

- 特权路径（veth、cgroup 写、setns exec、容器 fork）必须在 root 环境验证；
  本次开发环境为非 root 沙箱，无法代为执行容器 e2e。
- 自动化沙箱：系统使用 `/tmp/licore-*` 与 `$PWD/.?gocache` 隔离构建缓存，
  仓库内 `.gopath/.gocache/.modcache` 为沙箱遗留（已 gitignore，不提交）。
- `--gpu/npu/bandwidth/storage`、`RUN/ADD`、`s3` 属未来阶段，当前显式报错。