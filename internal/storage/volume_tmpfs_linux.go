// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package storage

import (
	"fmt"
	"os"
	"syscall"
)

// Create 在 dataDir 上挂一个 tmpfs（默认 50% RAM；有 --size 时按字节）。
func (d *tmpfsDriver) Create(dataDir string, v *Volume) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建 tmpfs 卷目录失败: %w", err)
	}
	sizeMB := "50%"
	if v.Size > 0 {
		sizeMB = fmt.Sprintf("%d", v.Size/1024/1024)
	}
	if err := syscall.Mount("tmpfs", dataDir, "tmpfs", 0, "size="+sizeMB); err != nil {
		return fmt.Errorf("挂载 tmpfs 卷失败（需要 root）: %w", err)
	}
	return nil
}

func (d *tmpfsDriver) Remove(dataDir string, v *Volume) error {
	_ = syscall.Unmount(dataDir, 0)
	return tmpfsRemove(dataDir)
}
