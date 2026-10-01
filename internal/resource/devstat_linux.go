// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package resource

import (
	"fmt"
	"syscall"
)

// statDevice 从 /dev 路径读取 major:minor（Linux 用 syscall.Stat_t.Rdev）。
func statDevice(path string) (int64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, fmt.Errorf("设备 %s 不存在或不可访问: %w", path, ErrBadDevice)
	}
	return (int64(st.Rdev>>8) & 0xfff) + (int64(st.Rdev) & 0xff), nil
}
