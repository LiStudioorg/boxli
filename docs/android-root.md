# Android（有 Root）运行 Boxli

本文档说明 Boxli 在 **有 Root 的 Android 设备**上的支持范围、运行时适配细节、
已知限制与排查方法。

> 无 Root 的 Android **官方不支持**：缺少 namespace / cgroup / setns 等内核隔离
> 能力，任何用户态方案（含 proot）都无法提供真正的隔离。Boxli 不检测 proot、
> 不集成 Termux、不引导提权。详见 [AGENTS.md](../AGENTS.md) 的《Android 支持策略》。

---

## 1. 支持范围

Android 有 Root 走与 Linux 服务器相同的 `native_linux` 后端：namespace + cgroup，
功能与 Linux 服务器一致。差异由运行时**在探测到 Android 时自动适配**，用户无需
额外参数。

| 能力 | Android（有 Root） | 说明 |
| --- | --- | --- |
| namespace 隔离 | ✅ | `CLONE_NEWPID/NEWNS/NEWUTS/NEWIPC`，bridge 模式另加 `NEWNET` |
| cgroup 资源限制 | ✅ | 优先 cgroup v2，设备只有 v1（或 v1/v2 混合）时自动走 v1 |
| 网络（veth + NAT） | ✅ | 同 Linux |
| 卷 / `:ro` / 匿名卷 | ✅ | 同 Linux |
| `boxli exec`（含 `-it`） | ✅ | 走 cgo 组件 `internal/execns` |
| 开机自启 | ✅ | 经 Magisk `service.d` 生成脚本 |
| SELinux | ⚠️ 见第 2 节 | enforcing 设备上存在策略限制，无法完全消除 |

---

## 2. SELinux

绝大多数 Android 设备默认 **enforcing**，这是与 Linux 服务器最主要的差异来源。

### 2.1 Boxli 做了什么

容器 init 进程在 `execve` 用户命令**之前**，继承引擎自身的 exec 过渡上下文：

1. 读引擎进程的 `/proc/self/attr/exec`（当前继承到的上下文）；
2. 写回**容器 init 自己**的同一路径，使 `execve` 后过渡到该上下文；
3. 失败时**只降级告警**，容器继续启动。

设计原则：

- **容器上下文与引擎保持一致**。这样"引擎能访问的东西容器也能访问"，不会因为
  放宽标签而扩大攻击面，也不需要在设备上预置任何策略。
- **绝不调用 `setenforce`**，绝不修改 `/sys/fs/selinux/*`，绝不动全局策略或
  SELinux 运行状态。Boxli 只读写自身进程的 `attr/exec`。
  > 这条约束由单元测试 `TestSELinuxNoGlobalStateChange` 静态守护：源码中一旦出现
  > `setenforce` / `/sys/fs/selinux` 等字样，测试立即失败。
- **不做"假装成功"**：写不进去就明确告警，不会静默吞掉。

### 2.2 为什么用 `attr/exec` 而不是 `attr/current`

`/proc/self/attr/exec` 是**下一次 `execve` 的过渡目标**（可写）；
`/proc/self/attr/current` 是**当前运行上下文**（多数策略下不可写）。
容器 init 需要的是"exec 用户命令时进入哪个上下文"，因此只能用前者。

另外，该属性**只作用于调用者自己的下一次 `execve`**，所以必须在容器 init 进程内、
紧邻 `execve` 写入——在父进程里写只会影响父进程自己。

### 2.3 三种状态下的行为

| 设备 SELinux 状态 | Boxli 行为 | 日志 |
| --- | --- | --- |
| **disabled**（`selinuxfs` 未挂载） | 直接跳过 | Debug 级，无噪音 |
| **permissive**（只记录不拦截） | 尝试继承上下文；失败仅告警 | 失败时 Warn |
| **enforcing**（强制拦截） | 尝试继承上下文；失败仅告警 | 失败时 Warn（见下） |
| 状态不可读（`unknown`） | 同 enforcing 处理 | — |

非 SELinux 宿主上 `/proc/self/attr/exec` 读取返回 **EINVAL**（内核只在 SELinux 为
活跃 LSM 时才提供该属性的读写；若宿主用 AppArmor，该文件存在但读写返回 EINVAL）。
这被当作**正常情况跳过**而不是错误——否则所有非 SELinux 设备都会打出误导性告警。
该分支有真实宿主上的测试覆盖（本项目开发机即 AppArmor，`attr/exec` 恒返回 EINVAL）。

> 语义依据：proc(5) 说明 `/proc/[pid]/attr/exec` 表示"**后续 execve(2) 时**要赋予
> 进程的属性"，且"SELinux 下该属性在 `execve(2)` 时被重置"。这正是"必须在
> execve 之前、在将要 exec 的那个进程里写"的原因。

### 2.4 enforcing 设备上的已知限制

Boxli **不修改策略、不放宽标签**，因此下面这些情况必然存在：

1. **`boxli` 二进制自身的域受限**。若 `boxli` 运行在受限域（如从 `/data/local/tmp`
   执行、被 Magisk 域约束），引擎本身可能无法挂载、建 cgroup 或 `setns`。
   表现为创建容器时 `EPERM`。

2. **容器内进程沿用引擎的标签**，可能被策略拦截。典型症状：
   - 读取某些宿主路径返回 `EACCES`／`Permission denied`；
   - `exec` 容器内可执行文件报 `Permission denied`（标签不允许 `execute`）；
   - 网络配置失败（标签无 `net_admin` 等）。

3. **把宿主目录 bind 进容器时标签不匹配**。宿主目录自带标签，容器内进程若
   无权访问该标签，即使挂载成功也读不到。设备文件同理。

### 2.5 排查指引

**第一步：确认 SELinux 状态与标签**

```bash
getenforce                     # Enforcing / Permissive / Disabled
cat /sys/fs/selinux/enforce    # 1=enforcing, 0=permissive
ls -Z /data/local/tmp/boxli    # 看 boxli 自身的标签
id -Z                          # 当前 shell 的上下文
```

**第二步：用 Boxli 自检**

```bash
boxli doctor          # 含 Android 环境探测：SELinux 状态、cgroup 形态、
                      # namespace 可用性、user namespace 支持
```

**第三步：读容器日志里的降级说明**

若日志出现：

```text
WARN 设置 SELinux exec 上下文失败，容器将继续启动；若系统处于 enforcing，
     容器内进程可能被策略拦截 context=... err=...
```

说明上下文继承没成功，容器已用内核默认标签启动。此时容器内遇到的
`EACCES`／`Permission denied` 基本都可以归因到标签不匹配，而不是 Boxli 的缺陷。

**第四步：定位是哪一步被拦**

```bash
# 先看内核审计日志（最直接）
dmesg | grep -i avc | tail -20
logcat | grep -i avc | tail -20

# avc 记录会给出 scontext（谁）、tcontext（访问谁）、tclass 与被拒的权限
```

**第五步：可选缓解（由用户自行决定，Boxli 不代劳）**

- 把 `boxli` 放到标签更宽松的位置执行；
- 由用户自行编写并加载针对性的策略模块（需要设备端 SELinux 工具链）；
- 仅在测试设备上临时 `setenforce 0` 验证"是否为 SELinux 导致"。
  > ⚠️ **Boxli 自身永远不会执行此操作**，也不会建议在生产设备上这样做。
  > 关闭 enforcing 会显著降低设备安全性。

### 2.6 本项目的验证边界

SELinux 的**上下文继承路径**有完整单元测试覆盖（读取、写入、跳过、降级、
安全约束），但：

- **enforcing 真机行为未经本项目实测**——所有开发/验证主机均为非 SELinux
  （`/sys/fs/selinux` 不存在，`attr/exec` 读取返回 EINVAL）。因此 enforcing 设备
  上的实际策略交互属于**已知未验证区域**，第 2.4 节的限制基于 SELinux 机制推导，
  而非实测结论。
- 验证脚本 [verify-root.sh](verify-root.sh) 在本机（非 SELinux）全部通过，
  确认**非 SELinux 路径零回归**。

---

## 3. cgroup 适配

Android 设备的 cgroup 形态比 Linux 服务器更分散：

| 形态 | 探测方式 | Boxli 行为 |
| --- | --- | --- |
| cgroup v2 | `/sys/fs/cgroup/cgroup.controllers` 存在 | 优先使用 |
| cgroup v1 | 各控制器分别挂载（`memory/`、`cpu/`、`pids/`…） | v2 不可用时自动回退 |
| v1/v2 混合 | 部分控制器在 v1、部分在 v2 | 按 v2 优先，v1 补齐 |
| 无 cgroup | 两者都无 | 容器可运行，但**无资源限制**（明确告警） |

**优先 v2**：保证支持 v2 的设备（含桌面 Linux 与新 Android）行为不回归。

**v1 的控制器子集可以是不完整的**：设备的 v1 常常只挂了部分控制器。此时
**缺失的控制器不是错误**——能设的限制照设，设不了的跳过并继续（与 v2 统一层级
的全有全无语义不同）。

**两个 v1 与 v2 的语义陷阱**（已在实现中显式处理）：

- `memory.memsw.limit_in_bytes`（v1）是**内存 + swap 的总和**，而 v2 的
  `memory.swap.max` 是 **swap 单独**的上限。搞反会把容器总内存限得比 `--memory`
  还小，导致容器被立刻 OOM。
- v1 的 `cpu.shares` 就是 `--cpu-shares` 的原始语义 `[2,262144]`，与 v2 的
  `cpu.weight` 量纲不同，**不能**做换算。

---

## 4. `/dev` 与 `/proc` 适配

### 4.1 `/dev`

容器拿到一个**最小可用**的 `/dev`，而不是整体 bind 宿主 `/dev`——后者会把
Android 的 `binder` / `ashmem` / `kgsl` 等平台专有节点暴露给容器，既无意义也
可能带来越权面。

| 内容 | 实现 |
| --- | --- |
| `null` `zero` `full` `random` `urandom` `tty` `ptmx` | 按需从宿主 bind 单个节点 |
| `/dev/shm` | 独立 tmpfs，默认 64 MiB，可由 `BOXLI_SHM_SIZE` 调整 |
| `/dev/pts` | devpts 实例（`exec -it` 依赖） |
| `/dev/fd` `stdin` `stdout` `stderr` | 指向 `/proc/self/fd` 的符号链接 |

宿主缺少某设备节点时跳过（宿主自身的问题，不该阻断容器）。

### 4.2 `/proc`

部分 Android 设备默认 `hidepid=2`，会让容器内 `ps` 看不到自己的进程。

Boxli 先解析**宿主** `/proc/self/mountinfo` 判断现状：

| 宿主 `hidepid` | 容器挂载参数 |
| --- | --- |
| 未设置 | 不传（用内核默认） |
| `0` | 不传（已最宽松） |
| `1` 或 `2` | 显式 `hidepid=0` |
| 状态不可解析 | 不传（保守），记 Debug 日志 |

若内核拒绝 `hidepid` 参数，回退为不带参数重试；两次都失败才报错——
**参数不被支持不应阻断容器启动**。

> 用 `mountinfo` 而非 `/proc/mounts`：`hidepid` 是 per-mount 选项，
> `mounts` 只反映 superblock 级选项，**读不到** hidepid。

---

## 5. 已知限制汇总

| 限制 | 状态 | 说明 |
| --- | --- | --- |
| 无 Root 的 Android | **不支持** | 官方策略，见开头 |
| enforcing 下的策略拦截 | **无法消除** | Boxli 不改策略；见第 2.4 节 |
| enforcing 真机实测 | **未验证** | 无 SELinux 测试机；见第 2.6 节 |
| 无 cgroup 的设备 | 可运行但无限制 | 明确告警，不假装成功 |
| user namespace 不可用 | 自动降级 | 有 Root 场景本就无需 rootless |

---

## 6. 相关文档

- [AGENTS.md](../AGENTS.md) — Android 支持策略与项目约定
- [verify-root.sh](verify-root.sh) — 真机验证脚本（A–J）
- [test-report-v0.6.0.md](test-report-v0.6.0.md) — 历史真机验收报告
