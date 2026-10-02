// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && (nocgo_exec || !cgo)

// Package execns 的纯 Go stub。用于 CGO_ENABLED=0 或 `-tags nocgo_exec` 构建。
// 此时 exec 进容器挂载命名空间不可用（纯 Go 无法 setns(CLONE_NEWNS)，见 Go
// issue #9091），返回 ErrNoCgoExec 明确提示。
package execns

import (
	"errors"
	"fmt"
)

// ErrNoCgoExec 表示当前构建不支持 cgo exec。
var ErrNoCgoExec = errors.New("exec 需要 cgo 构建（CGO_ENABLED=1），当前构建不支持")

// cgoEnabled 供纯 Go 决策层判断。
const cgoEnabled = false

// Enter stub：纯 Go 构建直接报错。
func Enter(targetPID int, workdir, user string, env []string, inFd, outFd, errFd int, cmd []string) (int, error) {
	return -1, fmt.Errorf("%w：exec 仅支持 linux+cgo 构建（CGO_ENABLED=1，勿用 -tags nocgo_exec）", ErrNoCgoExec)
}

// Wait stub。
func Wait(pid int) int { return -1 }

// Enabled 报告当前构建是否启用 cgo exec。
func Enabled() bool { return false }
