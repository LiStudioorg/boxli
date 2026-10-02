// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

// Package execns 的非 Linux stub：exec 需 Linux 命名空间，其他平台返回
// ErrUnsupported。
package execns

import (
	"errors"
	"fmt"
)

// ErrNoCgoExec 在非 Linux 平台语义为"不支持"。
var ErrNoCgoExec = errors.New("exec 需要 linux 平台")

// cgoEnabled 供纯 Go 决策层判断。
const cgoEnabled = false

// Enter stub：非 Linux 不支持。
func Enter(targetPID int, workdir, user string, env []string, inFd, outFd, errFd int, cmd []string) (int, error) {
	return -1, fmt.Errorf("exec 仅支持 linux 平台")
}

// Wait stub。
func Wait(pid int) int { return -1 }

// Enabled 报告当前构建是否启用 cgo exec。
func Enabled() bool { return false }
