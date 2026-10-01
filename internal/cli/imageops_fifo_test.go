// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux || android

package cli

import (
	"fmt"
	"os"
	"syscall"
)

// makeFIFO 在 path 处创建一个命名管道，用于验证 commit 会跳过非常规文件。
// syscall.Mkfifo 只在 linux/android 可用，故本文件按平台打标签；
// 其余平台由 imageops_fifo_other_test.go 提供同名 stub。
func makeFIFO(path string) error {
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		return fmt.Errorf("mkfifo %s: %w", path, err)
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return nil
}
