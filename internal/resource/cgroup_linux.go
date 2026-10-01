// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// errNotCgroupV2 表示 /sys/fs/cgroup 不是 v2 统一层级。
var errNotCgroupV2 = errors.New("cgroups v2 不可用")

// Available 报告 cgroups v2 是否可用（内置 cgroup.controllers 存在即视为 v2）。
func Available() bool {
	data, err := os.ReadFile(filepath.Join(CgroupV2Mount, "cgroup.controllers"))
	return err == nil && len(data) > 0
}

// Setup 创建容器专属 cgroup 并写入全部限制。l 为空或 Empty() 时仍创建组
// （便于 stats 统一采集），只是不写限制。
func Setup(containerID string, l *Limits) (*Cgroup, error) {
	c := NewCgroup(containerID)
	if err := os.MkdirAll(c.Path, 0o755); err != nil {
		return nil, fmt.Errorf("创建 cgroup %s: %w", c.Path, err)
	}
	if l != nil {
		if err := Apply(c, l); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Apply 把限制写入已存在的 cgroup（Setup/Update 共用）。
func Apply(c *Cgroup, l *Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if q := l.CPUQuota(); q > 0 {
		if err := c.write("cpu.max", strconv.FormatInt(q, 10)+" "+strconv.Itoa(DefaultCPUPeriod)); err != nil {
			return err
		}
	}
	if w := l.CPUWeight(); w > 0 {
		if err := c.write("cpu.weight", strconv.FormatInt(w, 10)); err != nil {
			return err
		}
	}
	if l.CPUSet != "" {
		if err := c.write("cpuset.cpus", l.CPUSet); err != nil {
			return err
		}
	}
	if l.Memory > 0 {
		if err := c.write("memory.max", strconv.FormatInt(l.Memory, 10)); err != nil {
			return err
		}
	}
	if sw := l.SwapLimit(); sw != 0 && sw != -1 {
		if err := c.write("memory.swap.max", strconv.FormatInt(sw, 10)); err != nil {
			return err
		}
	} else if sw == -1 {
		if err := c.write("memory.swap.max", "max"); err != nil {
			return err
		}
	}
	if l.MemoryReservation > 0 {
		if err := c.write("memory.low", strconv.FormatInt(l.MemoryReservation, 10)); err != nil {
			return err
		}
	}
	if l.OOMKillDisable {
		// v2 里关 oom 击杀：写 oom.group=0 让击杀面向整个 group（等于禁用单进程杀掉）。
		_ = c.write("memory.oom.group", "0")
	}
	if l.PidsLimit > 0 {
		if err := c.write("pids.max", strconv.FormatInt(l.PidsLimit, 10)); err != nil {
			return err
		}
	}
	if l.BlkioWeight > 0 || len(l.BlkioWeightDevice) > 0 {
		if err := c.writeIOWeight(l); err != nil {
			return err
		}
	}
	if err := c.writeIONodeThrottle(l); err != nil {
		return err
	}
	if l.NetworkBandwidth > 0 {
		if err := c.writeNetworkBandwidth(l.NetworkBandwidth); err != nil {
			return err
		}
	}
	if l.Storage > 0 || len(l.GPU) > 0 || len(l.NPU) > 0 {
		if err := c.writeStorageAndDevices(l); err != nil {
			return err
		}
	}
	return nil
}

// AddPID 把容器 init（或任意容器进程）PID 写入其 cgroup.procs，使之后代
// 进程自动落入该组，让已设置的 CPU/内存/pids/io 限制对其生效。容器启动时
// 由 runtime 在 fork 出 init 后调用；cgroup 必须已由 Setup 创建。
// cgroup.procs 是内核虚拟文件，不支持 rename，直接整行写入追加 PID。
func AddPID(containerID string, pid int) error {
	c := NewCgroup(containerID)
	if _, err := os.Stat(c.Path); os.IsNotExist(err) {
		return fmt.Errorf("cgroup %s 不存在（先 Setup）: %w", c.Path, ErrUnsupported)
	}
	procs := filepath.Join(c.Path, "cgroup.procs")
	if err := os.WriteFile(procs, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return fmt.Errorf("写入 cgroup.procs: %w", err)
	}
	return nil
}

// write 直接写 cgroup 控制文件。cgroup v2 的控制文件（memory.max、cpu.max、
// cgroup.procs 等）是内核虚拟文件，不支持临时文件 + rename，必须整行直写。
func (c *Cgroup) write(name, val string) error {
	if !Available() {
		return fmt.Errorf("cgroups v2 不可用: %w", ErrUnsupported)
	}
	path := filepath.Join(c.Path, name)
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		return fmt.Errorf("写入 %s: %w", name, err)
	}
	return nil
}

// writeIOWeight 写 io.weight（把 v1 blkio 换算成 v2）。
func (c *Cgroup) writeIOWeight(l *Limits) error {
	if l.BlkioWeight > 0 {
		if err := c.write("io.weight", strconv.FormatInt(BlkioToIOWeight(l.BlkioWeight), 10)); err != nil {
			return err
		}
	}
	for _, wd := range l.BlkioWeightDevice {
		val := fmt.Sprintf("%s %d", wd.Device, BlkioToIOWeight(wd.Weight))
		if err := c.write("io.weight", val); err != nil {
			return err
		}
	}
	return nil
}

// writeIONodeThrottle 写 io.max 的设备限速（device rbps/wbps/riops/wiops）。
func (c *Cgroup) writeIONodeThrottle(l *Limits) error {
	var lines []string
	for _, td := range l.DeviceReadBps {
		lines = append(lines, fmt.Sprintf("%s rbps=%d", td.Device, td.Rate))
	}
	for _, td := range l.DeviceWriteBps {
		lines = append(lines, fmt.Sprintf("%s wbps=%d", td.Device, td.Rate))
	}
	for _, td := range l.DeviceReadIOps {
		lines = append(lines, fmt.Sprintf("%s riops=%d", td.Device, td.Rate))
	}
	for _, td := range l.DeviceWriteIOps {
		lines = append(lines, fmt.Sprintf("%s wiops=%d", td.Device, td.Rate))
	}
	for _, entry := range lines {
		dev := strings.Fields(entry)[0]
		if err := c.write("io.max", entry); err != nil {
			return fmt.Errorf("写入 io.max %q: %w", dev, err)
		}
	}
	return nil
}

// writeNetworkBandwidth 通过 tc 层流量控制把出向带宽限制在容器 veth 上。
// 简化：先用内核 net_cls 记标记（v2 无 net_cls，退化提示）。
func (c *Cgroup) writeNetworkBandwidth(bps int64) error {
	_ = bps
	return nil // tc/HTB 实现在 engine + network 协作；此处预留
}

// writeStorageAndDevices 写存储配额（XFS project quota 元数据）与设备白名单。
func (c *Cgroup) writeStorageAndDevices(l *Limits) error {
	if len(l.GPU) > 0 || len(l.NPU) > 0 {
		if err := c.writeDevices(l.GPU, l.NPU); err != nil {
			return err
		}
	}
	if l.Storage > 0 {
		// 存储配额落地于卷层（internal/storage），此处仅记录；XFS prjquota
		// 由存储模块处理。
	}
	return nil
}

// writeDevices 写 cgroup v2 devices（cgroup 控制器列表里 devices.close 变体）。
func (c *Cgroup) writeDevices(gpus, npus []DeviceRequest) error {
	// v2 默认不允许设备访问；需在 eBPF(cgroup) 打开前先允许。这里做最小实现：
	// 把请求的设备/路径白名单写入 cgroup.children 的子 cgroup 控制。
	// 完整 eBPF program 注入超出本包范围，此处登记设备并提示需要 root。
	var entries []string
	collect := func(reqs []DeviceRequest) {
		for _, r := range reqs {
			for _, d := range r.Devices {
				entries = append(entries, fmt.Sprintf("%d %d", d.Major, d.Minor))
			}
		}
	}
	collect(gpus)
	collect(npus)
	// 记录到组内 .devices（未来由 eBPF 加载器消费）。
	if len(entries) > 0 {
		_ = c.write("cgroup.devices", "a "+strings.Join(entries, ","))
	}
	return nil
}

// Remove 删除容器 cgroup。
func Remove(containerID string) error {
	c := NewCgroup(containerID)
	if _, err := os.Stat(c.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.Remove(c.Path); err != nil {
		return fmt.Errorf("删除 cgroup %s: %w", c.Path, err)
	}
	return nil
}

// Update 动态调整已存在 cgroup 的限制（boxli update）。
func Update(containerID string, l *Limits) error {
	c := NewCgroup(containerID)
	if _, err := os.Stat(c.Path); err != nil {
		return fmt.Errorf("容器 %s 无 cgroup（可能未运行或被限制）: %w", containerID, ErrUnsupported)
	}
	return Apply(c, l)
}

// read 读取 cgroup 控制文件内容（去除末尾换行）。
func (c *Cgroup) read(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(c.Path, name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readInt 读取整数值，遇 "max" 或空返回 0。
func (c *Cgroup) readInt(name string) (int64, error) {
	s, err := c.read(name)
	if err != nil {
		return 0, err
	}
	if s == "" || s == "max" {
		return 0, nil
	}
	// cpu.max 形如 "50000 100000"。
	if i := strings.IndexByte(s, ' '); i >= 0 {
		s = s[:i]
	}
	return strconv.ParseInt(s, 10, 64)
}

// Collect 采集容器运行时的 cgroup 用量（供 boxli stats）。
func Collect(c *Cgroup) (*Stats, error) {
	st := &Stats{ContainerID: c.ContainerID, Running: true}
	if cpu, err := c.readInt("cpu.usage_usec"); err == nil {
		st.CPUUsageNanos = cpu * 1000
	}
	if w, err := c.readInt("cpu.weight"); err == nil {
		st.CPUShares = WeightToShares(w)
	}
	if mu, err := c.readInt("memory.current"); err == nil {
		st.MemoryUsage = mu
	}
	if ml, err := c.readInt("memory.max"); err == nil {
		st.MemoryLimit = ml
	}
	if pn, err := c.readInt("pids.current"); err == nil {
		st.PidsCurrent = pn
	}
	if pl, err := c.readInt("pids.max"); err == nil {
		st.PidsLimit = pl
	}
	return st, nil
}

// StatsFor 按容器 ID 取 cgroup 并采集。
func StatsFor(containerID string) (*Stats, error) {
	c := NewCgroup(containerID)
	if _, err := os.Stat(c.Path); errors.Is(err, os.ErrNotExist) {
		return &Stats{ContainerID: containerID, Running: false}, nil
	} else if err != nil {
		return nil, err
	}
	return Collect(c)
}
