// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

// Cgroup 表示一个容器专属的 cgroups v2 组及其路径。
// Linux 下路径为 <CgroupV2Mount>/<BoxliGroup>/<containerID>。
type Cgroup struct {
	// Root 是 cgroups v2 根挂载点。
	Root string
	// ContainerID 是容器 ID。
	ContainerID string
	// Path 是该组的绝对路径。
	Path string
}

// NewCgroup 返回容器对应的 cgroup 句柄（不检查是否存在）。
func NewCgroup(containerID string) *Cgroup {
	return &Cgroup{Root: CgroupV2Mount, ContainerID: containerID,
		Path: joinCGroup(CgroupV2Mount, BoxliGroup, containerID)}
}

// joinCGroup 拼接 cgroup 路径（平台无关实现，Linux 用 /）。
func joinCGroup(parts ...string) string {
	out := ""
	for _, p := range parts {
		out = out + "/" + p
	}
	return out
}

// Stats 是一次 stats 采集结果。
type Stats struct {
	// ContainerID 是容器 ID。
	ContainerID string
	// CPUUsage 是累计 CPU（用户+内核）纳秒数。
	CPUUsageNanos int64
	// CPUShares 是当前 cpu.weight（已换算成 v1 shares 语义）。
	CPUShares int64
	// MemoryUsage 是当前内存使用（字节）。
	MemoryUsage int64
	// MemoryLimit 是内存上限（字节；0 表示无限制）。
	MemoryLimit int64
	// PidsCurrent 是当前进程/线程数。
	PidsCurrent int64
	// PidsLimit 是进程上限（0 表示无限制）。
	PidsLimit int64
	// Running 表示容器是否在运行。
	Running bool
}
