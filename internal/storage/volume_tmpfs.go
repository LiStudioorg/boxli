// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"fmt"
	"os"
)

// tmpfsDriver 把卷落地在内存 tmpfs。仅在 Linux 下真正 mount；非 Linux 平台
// 退回 local 语义（Create 返回 ErrUnsupported 由调用方决定，默认忽略配额）。
type tmpfsDriver struct{}

func (d *tmpfsDriver) Name() string { return DriverTmpfs }

func (d *tmpfsDriver) Mountpoint(dataDir string, v *Volume) string { return dataDir }

// tmpfsRemove 是两平台共用的清理实现。
func tmpfsRemove(dataDir string) error {
	if err := os.RemoveAll(dataDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 tmpfs 卷数据失败: %w", err)
	}
	return nil
}
