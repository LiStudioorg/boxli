// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"errors"
	"fmt"
	"os"
)

// 卷驱动名。
const (
	DriverLocal = "local" // 数据落在宿主 <root>/volumes/<name>
	DriverTmpfs = "tmpfs" // 数据落在内存 tmpfs（仅 Linux）
)

// drivers 是驱动注册表。将来新增 nfs 等驱动时在此登记并实现 VolumeDriver。
var drivers = map[string]VolumeDriver{
	DriverLocal: &localDriver{},
	DriverTmpfs: &tmpfsDriver{},
}

// drv 按名取驱动。
func drv(name string) VolumeDriver { return drivers[name] }

// checkDriver 校验驱动名是否受支持。
func checkDriver(name string) error {
	if drv(name) == nil {
		return fmt.Errorf("卷驱动 %q 不受支持（可用：local|tmpfs）: %w", name, ErrBadDriver)
	}
	return nil
}

// localDriver 是最简单的驱动：数据目录就是 <root>/volumes/<name> 的一个普通目录。
type localDriver struct{}

func (d *localDriver) Name() string { return DriverLocal }

func (d *localDriver) Create(dataDir string, v *Volume) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("创建 local 卷数据目录失败: %w", err)
	}
	if v.Size > 0 {
		// 配额通过硬链接环 /tmpfs 之外由上层计数约束；local 驱动落盘大小
		// 由引擎的 quota 记账（阶段：--size 记录于元数据）。
	}
	return nil
}

func (d *localDriver) Remove(dataDir string, v *Volume) error {
	if err := os.RemoveAll(dataDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除 local 卷数据失败: %w", err)
	}
	return nil
}

func (d *localDriver) Mountpoint(dataDir string, v *Volume) string { return dataDir }
