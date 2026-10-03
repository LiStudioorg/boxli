// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package shim

import (
	"context"
	"os"

	"github.com/LiStudioorg/licore/internal/store"
)

// 非 Linux 平台占位：shim 依赖 native_linux 运行时，随其后端一起落地。

// RunFromEnv 非 Linux 平台不支持。
func RunFromEnv(_ context.Context) error { return errUnsupported }

// Reexec 非 Linux 平台不支持。
func Reexec(_, _ string) (*os.Process, error) { return nil, errUnsupported }

var errUnsupported = &shimError{"licore/shim: 本平台 shim 未实现（阶段 2 仅支持 linux）"}

type shimError struct{ msg string }

func (e *shimError) Error() string { return e.msg }

// Options 非 Linux 平台占位（保持引擎层跨平台可编译）。
type Options struct {
	Store   *store.Store
	Cfg     *store.ContainerConfig
	Stdin   *os.File
	Stdout  *os.File
	Stderr  *os.File
	OnStart func(pid int)
}

// Run 非 Linux 平台不支持。
func Run(_ context.Context, _ *Options) error { return errUnsupported }
