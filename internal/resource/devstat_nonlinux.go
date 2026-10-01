// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package resource

import "fmt"

// statDevice 非 Linux 平台不支持从 /dev 解析，返回错误（由调用方降级）。
func statDevice(path string) (int64, error) {
	return 0, fmt.Errorf("%s: 非 Linux 平台不支持 /dev 设备解析: %w", path, ErrUnsupported)
}
