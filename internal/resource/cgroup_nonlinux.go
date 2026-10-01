// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package resource

// 非 Linux 平台：cgroup 一律不可用，提供占位以便交叉编译。

func Available() bool { return false }

func Setup(containerID string, l *Limits) (*Cgroup, error) {
	return NewCgroup(containerID), ErrUnsupported
}

func Apply(c *Cgroup, l *Limits) error { return ErrUnsupported }

func AddPID(containerID string, pid int) error { return ErrUnsupported }

func Remove(containerID string) error { return nil }

func Update(containerID string, l *Limits) error { return ErrUnsupported }

func Collect(c *Cgroup) (*Stats, error) { return nil, ErrUnsupported }

func StatsFor(containerID string) (*Stats, error) {
	return &Stats{ContainerID: containerID, Running: false}, nil
}
