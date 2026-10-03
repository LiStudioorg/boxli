# Android（有 Root）真机验证手册

逐项验证 Boxli 在 Android 设备上的运行时行为：每条给出**命令**、**期望输出**、
**不符时的排查方向**。适配 [verify-root.sh](verify-root.sh) 无法直接跑通的
Android 场景（该脚本依赖宿主 `go build`、Docker 基线探测等 Linux 服务器组件）。

> **诚实边界**：本手册的期望输出取自**同一代码路径在 Linux root 真机的实测结果**
> （A–J 全绿，见 [test-report-v0.6.0.md](test-report-v0.6.0.md)），Android 特有
> 差异（SELinux enforcing、ROM 定制 cgroup）属于**已知未实测区域**，凡涉及处均
> 标注 ⚠️。若你的设备输出与手册不符，先按各节"排查"处理，再把完整输出反馈到
> issue——那就是新发现，而不是你的操作问题。
>
> 概念背景（为什么是这个期望）见 [android-root.md](android-root.md)。

## 0. 约定

以下所有命令默认在设备 root shell 中执行：

```bash
adb shell
su                    # Magisk 授权弹窗确认一次
export PATH=/data/local/bin:$PATH
export BOXLI_HOME=/data/boxli
alias demo='BOXLI_HOME=/data/boxli boxli'   # 下文用 demo 代替长前缀
```

镜像准备（开发机执行一次，产物推给设备）：

```bash
# 开发机：任意带 /server 静态二进制的 Boxfile
boxli build -t demo:v1 --file Boxfile ./ctx
adb push demo_v1.boxli /data/local/tmp/
```

## 1. 前置条件

### 1.1 Root 与身份

```bash
id          # 期望: uid=0(root) ...
uname -r    # 期望: >= 4.14（GKI/5.10+ 最佳）
getenforce  # Enforcing / Permissive / Disabled 都可继续；Enforcing 注意 ⚠️SELinux
```

不符：非 root → 官方不支持（见 android-root.md 开头），到此为止。

### 1.2 namespace 能力

```bash
ls /proc/self/ns/
# 期望至少有: pid mnt net user uts ipc cgroup（user 允许缺失）
```

缺 `pid/mnt/uts/ipc` 任一 → 内核没开 CONFIG_NAMESPACES，Boxli 会硬失败
`ErrNoNamespaces`（这是设计：没有命名空间就没有容器，宁可失败也不假装）。

### 1.3 cgroup 形态

```bash
ls /sys/fs/cgroup/cgroup.controllers 2>/dev/null && cat /sys/fs/cgroup/cgroup.controllers
# 有输出 → v2（或 hybrid 的 v2 侧），期望看到 memory pids（cpu 可选）
grep cgroup /proc/mounts
# v1 设备 → 各控制器分行挂载；只挂了一部分是常态，不是故障

# 可写性（boxli 建组的真实判据是 mkdir，不是 touch——cgroupfs 只允许建目录）：
mkdir /sys/fs/cgroup/.boxli_probe && rmdir /sys/fs/cgroup/.boxli_probe && echo cgroup 可写
```

不符：不可写（ROM 把 cgroup 挂成只读或 delegated 给了 init 域）→ 容器仍可运行，
资源限制不生效，Boxli 会打出明确警告；**不会**假装限制成功。

### 1.4 user namespace（非必需项）

```bash
cat /proc/sys/user/max_user_namespaces 2>/dev/null
# 0 或缺失 → userns 被 ROM 禁用。对 root 路线【无影响】：
# root 下 Boxli 本就不加 CLONE_NEWUSER（android-root.md 3.3）
```

### 1.5 网络工具

```bash
nft --version   # boxli 的 NAT 走 nftables；缺失 → 端口映射/出口不可用
ip link show    # iproute2（Android 为 toybox/ip 工具，一般内置）
```

## 2. 安装与自检

```bash
boxli --version
# 期望: boxli version <ver>；若显示"尚未实现"说明拿到的是过旧二进制

boxli doctor
# 期望（Linux/Android 同构输出）：
#   [OK]  kernel.version       内核 >= 5.8
#   [OK]  kernel.namespaces    namespace 支持
#   [OK]  cgroups.mount        cgroups 挂载（Android 只挂 v1 时也应 OK）
#   [OK]  storage.data-dir     /data/boxli 可写
# ⚠️ doctor 尚未输出 Android 专项（SELinux 状态等），用 getenforce 确认
#    （android-root.md 第 8 节路线图）。
```

## 3. 导入与运行

```bash
boxli pull /data/local/tmp/demo_v1.boxli
# 期望: 已导入 demo:v1 ...（落地目录 /data/boxli/images/demo/v1）

boxli run -d --name demo demo:v1
# 期望: 打印 12 位容器 ID，立即返回 shell

boxli ps
# 期望: STATUS 列 Up（起容器到 Up 之间若 SELinux 拦截，见 §9 排查）
```

## 4. 隔离验证（android-root.md 第 3 节矩阵的现场版）

```bash
# scratch 镜像没有 shell，用打包了检查子命令的 demo（verify-root.sh 同款）：
boxli exec demo -- /server check
# 期望: hostname=<容器 UTS 名，非设备名> pid=1 ...
#   pid=1  → pid ns 生效
#   cgroup=0::/boxli/<id> → cgroup 组生效（v1 设备显示 /boxli/<id> 形态）

# 容器 init 的宿主 PID 从 runtime.json 取（boxli 暂无容器级 inspect 命令）：
CID=$(boxli ps -q); PID=$(sed -n 's/.*"initPid": *\([0-9]*\).*/\1/p' /data/boxli/containers/$CID/runtime.json)
ls -l /proc/$PID/ns/pid /proc/1/ns/pid
# 两个 inode 编号必须不同 → 独立 pid 命名空间（相同则隔离未生效，应报错而非静默）
```

⚠️ 纯 Go 构建（官方交叉编译）下 `boxli exec` 返回 `ErrNoCgoExec`——这是预期
行为（android-root.md 3.2），换 cgo 构建再验本节。

## 5. cgroup 资源限制

```bash
boxli rm -f demo
boxli run -d --name demo --memory 64 --cpus 1 --pids-limit 64 demo:v1
# 注意 --memory/--memory-swap 单位是 **MiB 整数**（不是 64m 这种带后缀的写法）

# v2 设备:
cat /sys/fs/cgroup/boxli/$(boxli ps -q)/memory.max /sys/fs/cgroup/boxli/$(boxli ps -q)/cpu.max /sys/fs/cgroup/boxli/$(boxli ps -q)/pids.max
# 期望: 67108864 / "100000 100000" / 64

# v1 设备（按设备实际挂载的控制器查对应目录）:
cat /sys/fs/cgroup/memory/boxli/$(boxli ps -q)/memory.limit_in_bytes
cat /sys/fs/cgroup/cpu/boxli/$(boxli ps -q)/cpu.shares 2>/dev/null   # 原始 shares 值，无换算
```

注意（v1 语义坑，android-root.md 第 5 节）：
- `--memory-swap` 在 v1 写的是**内存+swap 总和**（memsw），v2 写 swap 单值——
  对用户参数语义一致，落盘文件不同是正常适配（memsw 值 = memory + swap）。
- 设备只挂了部分控制器时，未挂载控制器的限制会被**跳过并告警**，其余照常生效。

## 6. /dev 与 /proc 形态

```bash
boxli exec demo -- /bin/sh -c 'ls /dev'      # 带 shell 的镜像
# 期望含: null zero random urandom tty ptmx shm pts fd stdin stdout stderr
# 期望不含: binder ashmem（Android 平台节点【不】透传进容器——设计如此）

boxli exec demo -- /bin/sh -c 'df -m /dev/shm | tail -1'
# 期望 size ≈ 64M（或 BOXLI_SHM_SIZE 指定值）

boxli exec demo -- /bin/sh -c 'ps aux | head -3'   # 或读 /proc/1/cmdline
# 期望能看到容器自身进程；看不到 → 宿主 hidepid=2，Boxli 已自动传 hidepid=0
# 重新挂载，若仍异常把 mount 输出发 issue
```

## 7. exec -it（PTY）

```bash
boxli exec -it demo -- /bin/sh
# 期望: 进入交互 shell，tty 形态正常：
#   容器内执行 ls -l /proc/self/fd/0 → 指向 /dev/pts/N
#   Ctrl+D 退出、退出码透传
# 历史坑（已修）：TIOCSPTLCK 参数传错导致 "openpty: bad address"；
# 控制终端必须先 setsid 再 TIOCSCTTY，否则容器内 tty 报 ENOTTY。
```

## 8. 停止与清理

```bash
boxli stop demo && time boxli rm demo
# 期望: 秒级完成（>5s 说明 shim 回收有问题，收集 /data/boxli/containers/<id>/container.log）

ls /sys/fs/cgroup/boxli/ 2>/dev/null
# 期望: 无该容器 ID 的子目录（boxli rm 同步清理 cgroup）

ls /data/boxli/containers/
# 期望: 无该容器目录
```

异常中断（掉电/kill -9）后的残留，Linux 宿主可用
`verify-root.sh --cleanup-only` 思路手动清：杀带 `BOXLI_SHIM=1` 环境变量的进程、
rmdir 空的 `/sys/fs/cgroup/boxli/*` 子目录（**先子后父**）、删 `nft list tables`
里的 boxli/boxli-fwd 表。Android 上按同样顺序手动执行即可。

## 9. 常见问题排查

| 症状 | 最可能原因 | 处置 |
| --- | --- | --- |
| 创建容器 `EPERM`（mount/setns/cgroup 处） | SELinux 域拦截 boxli 自身 | `dmesg \| grep avc \| tail`：看 scontext 是否为 boxli 所在域；按 android-root.md 4.5 调整放置位置/标签。⚠️enforcing 行为未实测 |
| 容器起来即退 + `Permission denied` | 容器进程标签不允许 execute/读文件 | 同上查 avc；Boxli 继承引擎 exec 上下文，不放宽标签（设计） |
| 日志 `WARN 设置 SELinux exec 上下文失败…` | attr/exec 写入被策略拒 | 预期内降级路径，容器继续运行；后续 EACCES 基本归因标签 |
| `ErrNoNamespaces` | 内核未开对应 CONFIG | 换 GKI 内核/ROM；Boxli 不做无隔离假容器 |
| `ErrNotRoot` | 非 root 且 userns 不可用 | 用 su 提权运行；或走官方不支持的自担路径 |
| 端口映射不通 | 缺 nftables / 内核缺 `nf_tables` | `nft list ruleset` 应能看到 boxli 表；缺 nft 则网络功能整体降级，验证 §3 直连容器 IP |
| `--memory` 不生效且告警"无可用 cgroup" | cgroup 只读/未挂载/未委托 | 见 §1.3；这是设备限制，Boxli 明示不假装 |
| `boxli exec` 报 `ErrNoCgoExec` | 拿的是纯 Go 交叉构建 | 需要 exec 就用 NDK cgo 构建（android-root.md 3.2） |
| 容器内 `ps` 看不到进程 | hidepid=2 且 hidepid=0 重挂失败 | 收集 `mount \| grep proc` 与容器日志发 issue |
| `boxli boot enable` 只生成 systemd unit | Magisk 后端未实现 | 按 android-root.md 第 2 节手动放 `/data/adb/service.d/boxli.sh` |

## 10. 验证完成后

反馈 issue 时请附上：`uname -r`、`getenforce`、`cat /sys/fs/cgroup/cgroup.controllers`
或 `grep cgroup /proc/mounts`、`boxli doctor` 全文、失败命令与完整输出、
`/data/boxli/containers/<id>/container.log`。**先不要** `setenforce 0`——
带着 enforcing 现场的 avc 记录来，比复现环境更有价值。
