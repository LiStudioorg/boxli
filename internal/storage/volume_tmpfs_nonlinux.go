// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package storage

import (
	"fmt"
	"os"
)

// Create 非 Linux 平台不支持真 tmpfs，退回普通目录（含配额元数据）。
func (d *tmpfsDriver) Create(dataDir string, v *Volume) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建卷目录失败: %w", err)
	}
	return nil
}

func (d *tmpfsDriver) Remove(dataDir string, v *Volume) error {
	if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除卷数据失败: %w", err)
	}
	return nil
}

var _ = fmt.Sprintf
