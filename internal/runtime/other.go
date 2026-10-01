// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package runtime

// 非 Linux 平台的运行时占位。真实后端：macOS 走 vm_darwin（轻量虚拟机），
// 无 Root Android 走 proot_android；落地前本文件保证全仓库跨平台可编译。

// RunInit 非 Linux 平台不支持。
func RunInit() error { return ErrUnsupported }

// Start 非 Linux 平台不支持。
func Start(_ *Config, _ func(pid int)) (*StartResult, error) {
	return nil, ErrUnsupported
}
