// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import "path/filepath"

// cgroupV2GroupRoot 是 cgroup v2 统一层级的根（licore 父组的所在根），同时
// 用作 v2 可用性判定（读 <root>/cgroup.controllers）的基准路径。
//
// 生产环境恒等于 CgroupV2Mount，与 cgroupV1Roots 一样只作为测试缝存在：
// 测试注入临时目录即可完整驱动 setupV2/applyV2/AddPID/Collect 等整条 v2
// 写入链，而不必（也不应该在 CI 里）真写宿主 /sys/fs/cgroup。
var cgroupV2GroupRoot = CgroupV2Mount

// Cgroup 表示一个容器专属的 cgroups v2 组及其路径。
// Linux 下路径为 <CgroupV2Mount>/<LiCoreGroup>/<containerID>。
type Cgroup struct {
	// Root 是 cgroups v2 根挂载点。
	Root string
	// ContainerID 是容器 ID。
	ContainerID string
	// Path 是该组的绝对路径。
	Path string
}

// NewCgroup 返回容器对应的 cgroup 句柄（不检查是否存在）。
//
// 路径用 filepath.Join 而非手工拼接：手工拼接会在挂载点前多出一个 "//"，
// 虽然内核容忍，但会污染错误信息（曾让排障者误以为路径来自别处）。
func NewCgroup(containerID string) *Cgroup {
	root := cgroupV2GroupRoot
	return &Cgroup{Root: root, ContainerID: containerID,
		Path: filepath.Join(root, LiCoreGroup, containerID)}
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
