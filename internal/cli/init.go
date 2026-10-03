// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/runtime"
)

// newInitCommand 注册隐藏命令 `boxli init`：仅由运行时代码通过
// /proc/self/exe 重执行触发，不面向用户。
func newInitCommand(_ io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:    "init",
		Short:  "容器 1 号进程入口（内部使用）",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.RunInit()
		},
	}
}
