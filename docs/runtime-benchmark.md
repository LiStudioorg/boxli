# 运行时基准测试报告（阶段 2 · Linux 原生运行时 spike）

本报告记录 Boxli 第一个真实容器运行路径（`internal/runtime`，后端 `native_linux`）
的功能验证与内存实测。spike 目标：证明**纯 Go、零 CGO、零外部容器组件**
（不用 runc/containerd）可以直接创建可用容器，并验证 10–20 MiB 内存目标。

## 1. 测试环境

| 项 | 值 |
| --- | --- |
| 宿主内核 | Linux 5.15.0-179-generic x86_64（VM） |
| Go 工具链 | go1.27.1 linux/amd64，`CGO_ENABLED=0`（静态 ELF） |
| 引擎二进制 | `bin/boxli`，约 5.7 MiB（含 cobra 与符号表） |
| 运行身份 | 普通用户 uid 1000（非 root，走 user namespace 自动降级） |
| 特权路线 | euid==0 时跳过 CLONE_NEWUSER，其余 namespace 相同 |
| rootfs | 自制的最小 rootfs（静态 busybox 1.36 + applet 符号链接，约 2.2 MiB） |
| 触发入口 | `boxli dev-run --rootfs <dir> -- <cmd>`（隐藏开发命令，走 `runtime.Start`） |

注：测试环境本身在容器内，内核拒绝在 `pivot_root` 卸载旧根之后再新建 procfs 超块，
因此 proc 挂载提前到 pivot 之前（见 `internal/runtime/init_linux.go` 注释），
得到的是绑定本 PID namespace 的全新 procfs，语义等价。真机上该差异不存在。

## 2. 功能验证结果

| 验证项 | 结果 |
| --- | --- |
| namespace 隔离 | CLONE_NEWPID \| NEWNS \| NEWUTS \| NEWIPC（+ rootless 时 NEWUSER）全部生效；容器内 `$$` = 1；`/proc/sys/kernel/hostname` = `--hostname` 指定值 |
| 根切换 | `pivot_root` 成功；容器内 `/` 只有 rootfs 内容；旧根经 `MNT_DETACH` 卸载后宿主文件不可见 |
| /proc | 容器内 `/proc/1/cmdline` 指向自身 init（非宿主 systemd），PID 视野完全隔离 |
| /dev | 最小设备集（null/zero/full/random/urandom/tty）经 bind 注入，rootless 无法 mknod 时的标准做法 |
| 退出码 | 容器 `exit 42` → boxli 退出码 42；容器 init 被外部 SIGKILL → 137（128+9） |
| PID 1 信号语义 | 容器内 `kill -TERM $$` 不死——内核保护：PID namespace 的 init 未注册 handler 的信号被忽略（与 Docker/runc 行为一致，非缺陷） |
| 并发 | 100 个容器共享同一 rootfs 并发启动，0 失败（旧根目录名按容器实例唯一化后消除竞态） |
| 残留 | 容器全部退出后 rootfs 无 `.boxli_old_root.*` 残留、无挂载残留 |
| 清理 | shim（dev-run 父进程）等待 init 退出后一并退出，无常驻进程 |

## 3. 内存实测

方法：宿主 `/proc/<pid>/status` VmRSS 采样 + 系统 `MemAvailable` 差值法交叉验证；
rootfs 内进程为静态 busybox。

### 3.1 单容器（空闲 `sleep`）

| 进程 | VmRSS（稳定值） | 说明 |
| --- | --- | --- |
| 容器 init（busybox sleep） | **4 KiB** | 静态小进程，几乎全部页可回收/共享 |
| shim（boxli 父进程） | **~5.0 MiB** | cobra 命令行框架占大头；VmHWM 与稳态一致 |
| 合计（VmRSS 上界） | **~5 MiB** | |

MemAvailable 差值法在单容器粒度受页缓存噪声支配（重复 3 次实测
0.5–2.3 MiB 波动，不可用作单容器结论），可靠数字见 3.2 的 100 容器批量测量。

### 3.2 100 容器并发

| 指标 | 值 |
| --- | --- |
| 100 × (shim + init) 系统真实增量（MemAvailable 差值） | **227 MiB** |
| 摊到每容器 | **≈ 2.3 MiB** |
| 100 个 init 进程 VmRSS 合计 | 400 KiB（4 KiB/进程，页缓存共享） |
| 全部退出后 | 内存完全回收，无泄漏 |

VmRSS 直接求和（~529 MiB）会重复计共享页（同一静态二进制的 text 映射），
不代表真实成本；MemAvailable 差值（227 MiB）与理论分析一致，
**每容器真实成本 ≈ 2.3 MiB**。

### 3.3 对照 10–20 MiB 目标

Boxli 引擎**无常驻进程**（开机自启是一次性 `boxli boot`，之后退出），
所以"引擎常驻内存"实际为零；运行成本全部按容器计：

- 每容器 ≈ 2.3 MiB（init 近 0 + shim ~2.3 MiB 有效增量）；
- 单容器全家福 ~5 MiB（按 VmRSS 上界计）；
- **结论：远低于 10–20 MiB 目标，达成**。后续 shim 瘦身（去掉 cobra 依赖，
  改为极小 main 或复用 init 路径）可再压到 2 MiB 以内，作为阶段 3 优化项。

## 4. 已知限制（如实记录，不影响 spike 结论）

1. **rootless 时 shim 无法感知容器内 PID 视野**：容器内 PID 1 对应宿主 PID 由
   `StartResult.ChildPID` 提供，`boxli ps` 落地时要经状态文件而不是 `ps` 命令。
2. **cgroups 资源限制未实现**：`--memory/--cpus/--pids-limit` 仍是 CLI 骨架，
   属 `internal/resource`（阶段 2 后续任务）。
3. **嵌套容器（本环境）proc 必须在 pivot 前挂载**；真机两种顺序均可。
4. **shim 有效增量 ~2.3 MiB** 主要来自 Go runtime + cobra 启动分配；正式
   `run`/shim 实现时评估瘦身。
5. dev-run 为隐藏命令（`boxli dev-run`），正式入口是 `boxli run`（待实现）。

## 5. 复现方法

```bash
go build -o bin/boxli .
# 最小 rootfs：静态 busybox + ln -s 常用 applet
./bin/boxli dev-run --rootfs /tmp/boxli-rootfs --hostname demo -- /bin/sh -c \
  'echo PID=$$; cat /proc/sys/kernel/hostname; ls /'
```
