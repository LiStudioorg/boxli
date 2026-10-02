// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// 本文件实现 cgroups v1 适配。Android 上有大量设备仍使用 cgroup v1（或
// v1/v2 混合），只支持 v2 会让这些设备的资源限制整体不可用。
//
// 与 v2 的**结构性差异**（这是本文件存在的原因）：
//   - v2 是单一统一层级，一个容器一个目录，所有控制器文件都在同一目录下；
//   - v1 每个控制器各自挂载（memory/、cpu/、pids/ …），容器要在**每个**
//     已挂载的控制器目录下各建一次同名子目录。
//
// 因此 v1 下 Cgroup.Path 只代表**主控制器目录**（优先 memory，其次 cpu），
// 其余控制器路径由 cgroupV1Path 按需派生；所有写入都在内部走分支，
// Setup/Apply/AddPID/Remove/Collect 的签名保持不变。

// cgroupV1Roots 是 cgroup v1 的候选根挂载点，按优先级排列。
// /dev/cgroup 是部分 Android 定制内核的挂载位置。
// 做成变量（而非常量）以便单元测试注入临时目录；生产环境不要修改。
var cgroupV1Roots = []string{CgroupV2Mount, "/dev/cgroup"}

// v1Controllers 是 boxli 尝试使用的 v1 控制器，顺序固定以保证可复现。
// 顺序即写入优先级：memory 优先（资源限制里最常用）。
var v1Controllers = []string{"memory", "cpu", "cpuacct", "cpuset", "pids", "blkio"}

// ErrCgroupV1Unavailable 表示没有可用的 cgroup v1 控制器挂载。
var ErrCgroupV1Unavailable = errors.New("cgroups v1 控制器不可用")

// v1MemswUnlimited 是 v1 下"不限内存+swap"的替代值。
// v1 的 memory.memsw.limit_in_bytes 不支持 v2 的 "max" 关键字，只能写一个
// 足够大的数（PAGE_COUNTER_MAX 的实际上限），这里取 1<<62 字节（4 EiB），
// 远大于任何真实内存，等价于不限。
const v1MemswUnlimited = int64(1) << 62

// CgroupMode 描述实际生效的 cgroup 层级形态。
type CgroupMode string

// cgroup 层级形态取值。
const (
	// ModeV2 表示统一层级 cgroup v2。
	ModeV2 CgroupMode = "v2"
	// ModeV1 表示 cgroup v1 控制器各自挂载。
	ModeV1 CgroupMode = "v1"
)

// DetectCgroupMounts 返回实际可用的 cgroup 挂载点候选列表，按优先级排列：
// 统一层级 v2 根在前，v1 控制器根在后。空列表表示未探测到可用 cgroup。
//
// 本函数只读，供 doctor 与资源层共用。
func DetectCgroupMounts() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if Available() {
		add(CgroupV2Mount)
	}
	for _, root := range cgroupV1Roots {
		if len(v1AvailableControllers(root)) > 0 {
			add(root)
			// 找到第一个可用的 v1 根即可，多个根同时存在没有意义。
			break
		}
	}
	return out
}

// v1ModeOverride 是测试用的形态覆盖开关：非空时 CgroupModeOf 直接返回它，
// 使单元测试能在只有 cgroup v2 的开发机上验证 v1 代码路径。
// 生产环境恒为空串。
var v1ModeOverride CgroupMode

// CgroupModeOf 报告当前宿主实际使用的 cgroup 形态。
// 优先 v2（有统一层级就用 v2），否则退回 v1，都没有则返回空串。
func CgroupModeOf() CgroupMode {
	if v1ModeOverride != "" {
		return v1ModeOverride
	}
	if Available() {
		return ModeV2
	}
	for _, root := range cgroupV1Roots {
		if len(v1AvailableControllers(root)) > 0 {
			return ModeV1
		}
	}
	return ""
}

// v1AvailableControllers 返回指定根下已挂载且可用的 v1 控制器名。
//
// 判定依据是该控制器目录下存在 cgroup.procs（挂载后内核必然提供）；
// 只看目录存在会把空目录误判为已挂载。
func v1AvailableControllers(root string) []string {
	var out []string
	for _, c := range v1Controllers {
		dir := filepath.Join(root, c)
		if _, err := os.Stat(filepath.Join(dir, "cgroup.procs")); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// v1Root 返回第一个可用的 cgroup v1 根挂载点，不可用时返回空串。
func v1Root() string {
	for _, root := range cgroupV1Roots {
		if len(v1AvailableControllers(root)) > 0 {
			return root
		}
	}
	return ""
}

// cgroupV1Path 返回容器在指定 v1 控制器下的目录路径。
//
// 注意不能直接用 joinCGroup：后者无条件加 "/" 前缀，会把绝对路径变成
// "//tmp/..."（虽多数系统能容忍，但 Stat/MkdirAll 的错误信息与比较都会失真）。
// 这里用 filepath.Join，它会正确清理多余分隔符。
func cgroupV1Path(root, controller, containerID string) string {
	return filepath.Join(root, controller, BoxliGroup, containerID)
}

// newV1Cgroup 构造 v1 形态的容器 cgroup 句柄。
//
// Path 取"主控制器"目录：优先 memory（内存限制最常用），其次 cpu，
// 再次任一可用控制器。这样 StatsFor / Collect 等只读 Path 的调用方
// 仍能拿到有意义的数据，而写入路径由各 write*V1 方法分别派发。
func newV1Cgroup(containerID, root string) *Cgroup {
	ctrl := v1PrimaryController(root)
	return &Cgroup{
		Root:        root,
		ContainerID: containerID,
		Path:        cgroupV1Path(root, ctrl, containerID),
	}
}

// v1PrimaryController 选出 v1 下的主控制器名。无可用的返回 "memory"（调用方
// 在此之前已确认 v1 可用，这里只做兜底）。
func v1PrimaryController(root string) string {
	avail := v1AvailableControllers(root)
	for _, want := range []string{"memory", "cpu", "cpuacct"} {
		for _, got := range avail {
			if got == want {
				return want
			}
		}
	}
	if len(avail) > 0 {
		return avail[0]
	}
	return "memory"
}

// v1ControllersFor 返回 limits 实际需要写入的缺失检查所需控制器集合。
// 仅用于测试与诊断，按字母序返回以保证可复现。
func v1ControllersFor(l *Limits) []string {
	if l == nil {
		return nil
	}
	set := map[string]bool{}
	if l.Memory > 0 || l.MemoryReservation > 0 || l.SwapLimit() != 0 {
		set["memory"] = true
	}
	if l.CPUQuota() > 0 || l.CPUShares > 0 {
		set["cpu"] = true
	}
	if l.CPUSet != "" {
		set["cpuset"] = true
	}
	if l.PidsLimit > 0 {
		set["pids"] = true
	}
	if l.BlkioWeight > 0 || len(l.BlkioWeightDevice) > 0 ||
		len(l.DeviceReadBps) > 0 || len(l.DeviceWriteBps) > 0 ||
		len(l.DeviceReadIOps) > 0 || len(l.DeviceWriteIOps) > 0 {
		set["blkio"] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// setupV1 创建容器在全部可用 v1 控制器下的 cgroup 目录并写入限制。
func setupV1(containerID string, l *Limits, root string) (*Cgroup, error) {
	c := newV1Cgroup(containerID, root)
	avail := v1AvailableControllers(root)
	if len(avail) == 0 {
		return nil, fmt.Errorf("%s 下无可用 cgroup v1 控制器: %w", root, ErrCgroupV1Unavailable)
	}
	for _, ctrl := range avail {
		dir := cgroupV1Path(root, ctrl, containerID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建 cgroup v1 %s: %w", dir, err)
		}
	}
	if l != nil {
		if err := applyV1(c, l, root); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// applyV1 把限制写入 v1 的各控制器文件。
//
// 与 v2 不同，v1 下**缺失的控制器不是错误**：设备只挂了 memory 没挂 pids 时，
// 应当只写 memory 并让 pids 限制优雅降级，而不是整体失败。因此这里对每个
// 控制器先检查是否可用，不可用则跳过（由调用方通过 Warnings 告知用户）。
func applyV1(c *Cgroup, l *Limits, root string) error {
	if err := l.Validate(); err != nil {
		return err
	}

	if l.Memory > 0 {
		if err := writeV1(root, "memory", c.ContainerID, "memory.limit_in_bytes",
			strconv.FormatInt(l.Memory, 10)); err != nil {
			return err
		}
	}
	if sw := l.SwapLimit(); sw != 0 {
		// 语义差异（v1 vs v2 的关键坑）：
		//   - v2 的 memory.swap.max 是 **swap 单独** 的上限；
		//   - v1 的 memory.memsw.limit_in_bytes 是 **内存 + swap 的总和**。
		// 因此 v1 下要写 Memory + swapLimit；直接写 swapLimit 会把容器总内存
		// 限制得比 --memory 还小，导致容器被立刻 OOM。
		if val, ok := v1MemswValue(l, sw); ok {
			if err := writeV1(root, "memory", c.ContainerID, "memory.memsw.limit_in_bytes", val); err != nil {
				return err
			}
		}
	}
	if l.MemoryReservation > 0 {
		if err := writeV1(root, "memory", c.ContainerID, "memory.soft_limit_in_bytes",
			strconv.FormatInt(l.MemoryReservation, 10)); err != nil {
			return err
		}
	}
	if l.OOMKillDisable {
		if err := writeV1(root, "memory", c.ContainerID, "memory.oom_control", "1"); err != nil {
			return err
		}
	}
	if q := l.CPUQuota(); q > 0 {
		// v1：cfs_quota_us / cfs_period_us。
		if err := writeV1(root, "cpu", c.ContainerID, "cpu.cfs_period_us",
			strconv.Itoa(DefaultCPUPeriod)); err != nil {
			return err
		}
		if err := writeV1(root, "cpu", c.ContainerID, "cpu.cfs_quota_us",
			strconv.FormatInt(q, 10)); err != nil {
			return err
		}
	}
	if l.CPUShares > 0 {
		// v1 的 cpu.shares 就是 --cpu-shares 的原始语义（[2,262144]），
		// 不做 v2 cpu.weight 的换算——两者量纲不同，换算是 v2 专属。
		if err := writeV1(root, "cpu", c.ContainerID, "cpu.shares",
			strconv.FormatInt(l.CPUShares, 10)); err != nil {
			return err
		}
	}
	if l.CPUSet != "" {
		// cpuset 控制器需要先初始化 cpus/mems，否则写入子组会 EINVAL。
		for _, f := range []string{"cpuset.cpus", "cpuset.mems"} {
			if err := writeV1(root, "cpuset", c.ContainerID, f, l.CPUSet); err != nil {
				return err
			}
		}
	}
	if l.PidsLimit > 0 {
		if err := writeV1(root, "pids", c.ContainerID, "pids.max",
			strconv.FormatInt(l.PidsLimit, 10)); err != nil {
			return err
		}
	}
	if l.BlkioWeight > 0 {
		if err := writeV1(root, "blkio", c.ContainerID, "blkio.weight",
			strconv.FormatInt(l.BlkioWeight, 10)); err != nil {
			return err
		}
	}
	return nil
}

// v1MemswValue 计算 v1 memory.memsw.limit_in_bytes 应写入的值。
// sw 是 SwapLimit() 的返回值（swap 单独上限）：
//   - sw < 0（不限 swap）→ 写一个极大值（v1 无 "max" 关键字）；
//   - 其它 → 写内存 + swap 的总和；总和非正时返回 ok=false 表示不写入。
func v1MemswValue(l *Limits, sw int64) (string, bool) {
	if sw < 0 {
		return strconv.FormatInt(v1MemswUnlimited, 10), true
	}
	total := l.Memory + sw
	if total <= 0 {
		return "", false
	}
	return strconv.FormatInt(total, 10), true
}

// writeV1 写入一个 v1 控制文件。
//
// 若目标控制器未挂载，返回 nil（优雅降级），不阻断容器启动；这与 v1
// 设备控制器子集不完整的事实相符。文件存在但写入失败（如 EPERM）才是错误。
func writeV1(root, controller, containerID, file, val string) error {
	if !v1ControllerAvailable(root, controller) {
		return nil
	}
	path := filepath.Join(cgroupV1Path(root, controller, containerID), file)
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		return fmt.Errorf("写入 cgroup v1 %s/%s: %w", controller, file, err)
	}
	return nil
}

// v1ControllerAvailable 报告指定 v1 控制器是否已挂载可用。
func v1ControllerAvailable(root, controller string) bool {
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, controller, "cgroup.procs"))
	return err == nil
}

// addPIDV1 把 PID 写入容器在各 v1 控制器下的 tasks/procs 文件。
// 至少写入一个控制器才算成功；全部不可用时返回错误。
func addPIDV1(containerID, root string, pid int) error {
	avail := v1AvailableControllers(root)
	if len(avail) == 0 {
		return fmt.Errorf("%s 下无可用 cgroup v1 控制器: %w", root, ErrCgroupV1Unavailable)
	}
	val := []byte(strconv.Itoa(pid))
	var wrote bool
	var firstErr error
	for _, ctrl := range avail {
		dir := cgroupV1Path(root, ctrl, containerID)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		// cgroup.procs 迁移整个线程组，tasks 只迁移单线程；容器 init 用 procs。
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), val, 0o644); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("写入 cgroup v1 %s/cgroup.procs: %w", ctrl, err)
			}
			continue
		}
		wrote = true
	}
	if !wrote {
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("写入 cgroup v1 cgroup.procs 失败: %w", ErrUnsupported)
	}
	return nil
}

// removeV1 删除容器在各 v1 控制器下的目录。
//
// 与 v2 一样用 os.Remove（rmdir）而非 RemoveAll：真实的 cgroupfs 目录里只有
// 内核虚拟文件，rmdir 可直接成功；用 RemoveAll 反而危险——一旦某个控制器
// 目录被误判为普通目录，RemoveAll 会递归删掉宿主文件。目录不存在视为成功
// （幂等）。
func removeV1(containerID, root string) error {
	if root == "" {
		return nil
	}
	var firstErr error
	for _, ctrl := range v1Controllers {
		dir := cgroupV1Path(root, ctrl, containerID)
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.Remove(dir); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("删除 cgroup v1 %s: %w", dir, err)
		}
	}
	return firstErr
}

// collectV1 采集 v1 下的容器用量。
//
// 字段映射与 v2 的 Collect 保持一致，便于 stats 输出统一：
// cpu.usage_usec ← cpuacct.usage（纳秒）÷ 1000；
// memory.current ← memory.usage_in_bytes；pids.* ← pids.current/max。
func collectV1(c *Cgroup, root string) (*Stats, error) {
	st := &Stats{ContainerID: c.ContainerID, Running: true}

	if v1ControllerAvailable(root, "cpuacct") {
		path := filepath.Join(cgroupV1Path(root, "cpuacct", c.ContainerID), "cpuacct.usage")
		if ns, err := readV1Int(path); err == nil {
			st.CPUUsageNanos = ns
		}
	}
	if v1ControllerAvailable(root, "cpu") {
		path := filepath.Join(cgroupV1Path(root, "cpu", c.ContainerID), "cpu.shares")
		if shares, err := readV1Int(path); err == nil {
			st.CPUShares = shares
		}
	}
	if v1ControllerAvailable(root, "memory") {
		dir := cgroupV1Path(root, "memory", c.ContainerID)
		if mu, err := readV1Int(filepath.Join(dir, "memory.usage_in_bytes")); err == nil {
			st.MemoryUsage = mu
		}
		if ml, err := readV1Int(filepath.Join(dir, "memory.limit_in_bytes")); err == nil {
			st.MemoryLimit = ml
		}
	}
	if v1ControllerAvailable(root, "pids") {
		dir := cgroupV1Path(root, "pids", c.ContainerID)
		if pc, err := readV1Int(filepath.Join(dir, "pids.current")); err == nil {
			st.PidsCurrent = pc
		}
		if pl, err := readV1Int(filepath.Join(dir, "pids.max")); err == nil {
			st.PidsLimit = pl
		}
	}
	return st, nil
}

// readV1Int 读取 v1 控制文件的整数值；"max" 或空返回 0。
func readV1Int(path string) (int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || s == "max" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// v1StatsExist 报告容器在任一 v1 控制器下是否已有目录。
func v1StatsExist(containerID, root string) bool {
	for _, ctrl := range v1Controllers {
		if _, err := os.Stat(cgroupV1Path(root, ctrl, containerID)); err == nil {
			return true
		}
	}
	return false
}
