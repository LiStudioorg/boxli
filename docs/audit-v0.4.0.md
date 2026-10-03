# LiCore v0.4.0 审计报告

本文档记录 v0.4.0 开发完成后的总检查 / 自动修复 / 测试结果。
审计在 **非 root** 沙箱（UID 1000，landlock 限制）中进行，特权场景以
runbook 方式给出（见 `docs/e2e.md`）。

## 1. 检查项清单与结果

| 检查项 | 做法 | 结果 |
| --- | --- | --- |
| gofmt | `gofmt -l`（全部 tracked go 文件 + 新增文件） | ✅ 无输出 |
| go vet | `go vet ./internal/... ./hub/... .` | ✅ 无警告 |
| 单测 | `go test ./... -count=1` | ✅ 全绿 |
| 竞态 | `go test ./... -race -count=2` | ✅ 无 race |
| 交叉编译 | linux/amd64, linux/arm64, android/arm64, darwin/arm64 | ✅ 全过 |
| 覆盖率 | runtime 25.2% / network 55.7% / shim 15.4% / storage 68.6% / hub 66.9% / engine 67.9% | 部分达标* |
| Hub 端到端 | `hub serve` → login/push/search/pull | ✅ 可跑通（见 hub-e2e.md） |
| 容器端到端 | 网络/卷/资源/exec | 需 root，本环境列出 runbook |

> *覆盖率说明：runtime/shim/network 的缺口集中于**特权系统调用路径**
> （setns、veth/nft、cgroup 写入、shim 回环、`runtime.Start`/`RunInit`/
> `exec` 的 fork+namespace）。这些路径在非 root 沙箱无法执行，只能以真实
> root 容器做集成验证；纯数据/解析逻辑已补测试。`netlink` 包由 0% 提升到
> ~20%（字节编码辅助函数）。

## 2. 修复的 bug 列表

| 严重程度 | 位置 | 原因 | 修复 |
| --- | --- | --- | --- |
| 高 | `internal/resource/cgroup_linux.go` `write()` | 用「临时文件 + rename」写 cgroups v2 控制文件（`memory.max` 等是内核伪文件，不支持 rename）→ 限制从不生效 | 改为整行直写（`os.WriteFile`）<br>commit `71a24df` |
| 中 | `internal/engine/engine.go` `Run()` | 容器启动失败后仅删 rootfs，未撤回已建网络端点（veth/NAT）与 cgroup → 残留 | 失败路径 `disconnectContainer` + `resource.Remove`<br>commit `71a24df` |
| 中 | 阶段 3 引入 | `wireNetwork`/`wireVolumes`/`resource.Setup` 在 `engine.Run` 返回后才执行，前台模式下容器已退出才接线 → veth 从未进 netns | 接线前移进 engine（1.1–1.3），见特性提交 |
| 低 | `network` 缺省 | 缺省 `licore0` 未预建时 `licore run` 报“网络不存在” | 自动 `EnsurePreset` 补建 |

## 3. 安全审计

| 项 | 结论 |
| --- | --- |
| 命令注入 | exec 直接 `exec` argv，不经 shell；CLI 参数逐项传递；✅ |
| 路径穿越 | 卷目标强制绝对路径且 Clean 校验（拒绝 `..`）；exec 工作目录须容器内解析；✅ |
| 符号链接逃逸 | 卷挂载点 `MkdirAll` 目标在 rootfs 内；源由用户显式指定（宿主路径/卷），同 Docker 语义；✅ |
| 不安全临时文件 | store 卷/网络/凭证均「同目录临时文件 + rename」原子替换；`hub/serve` 凭证 0600；✅ |
| TOCTOU | 卷/网络持久化为原子 rename；✅ |
| cgroup/namespace 权限 | exec、veth 装配、cgroup 写入均需 root，非 root 明确报错不崩溃；✅ |
| 权限剥离 | runtime 启动默认剥离宿主挂载可见性（pivot_root + 卸载旧根）；✅ |
| Hub HTTP 未鉴权 | `/tags`、`/blobs`、`/search` 均要求 JWT Bearer；仅 `/auth/login` 公开；✅ |
| JWT 校验 | server 解析并校验签名/过期/scope（读/写）；✅ |
| blob 上传校验 digest | `Registry.PutBlob` 内容摘要不符即拒（ErrDigestMismatch → 409）；✅ |

已知限制（非漏洞）：
- `exec` 通过 `setns(pid)` 进 PID 命名空间，Go 进程多线程时 setns(pid) 可能
  EINVAL；需要单线程的 nsexec 化 helper 以彻底解决（v0.4 为 P1 近似，详情见
  `docs/e2e.md`）。
- hub JWT 密钥启动时随机生成，重启后旧令牌失效（覆盖说明在 hub-e2e.md）。

## 4. 测试覆盖率

```
internal/runtime  25.2%
internal/network  55.7%  (+ netlink 单独 ~20%)
internal/shim     15.4%
internal/storage  68.6%
hub               66.9%
internal/engine   67.9%
```

已达标：storage、hub、engine。runtime/shim/network 的剩余缺口为特权
系统调用路径（非 root 无法覆盖），已在第 1 节说明。

## 5. 遗留问题

- runtime/shim 覆盖率偏低：需 root 环境做真实容器测试补足。
- `exec` 的 PID 命名空间进入依赖单线程（Go 多线程 setns(pid) 边界）。
- `--storage s3` 为占位，尚未实现。

## 6. 端到端验证结果

- **Hub（本环境可跑）**：`licore hub serve` 启动 → `login` → `push` →
  `search` 命中 → `pull` 下载并走本地导入。✅
- **容器网络/卷/资源/exec（本环境需 root）**：runbook 见 `docs/e2e.md`，
  覆盖 veth 进 netns、-p NAT 绑定容器 IP、-v 绑定/匿名卷、--memory 等
  cgroup 限制、exec 命名空间进入、stop/rm 清理（网络端点、veth、cgroup）。