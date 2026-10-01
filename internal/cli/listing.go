// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// newPsCommand 实现 `boxli ps`。
func newPsCommand(out io.Writer) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "列出容器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("ps")
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "包含已停止的容器")
	return cmd
}

// newExecCommand 实现 `boxli exec`。
func newExecCommand(out io.Writer) *cobra.Command {
	var detach bool
	cmd := &cobra.Command{
		Use:   "exec [-d] <容器ID|名字> <command...>",
		Short: "在运行中的容器里执行命令",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("exec")
		},
	}
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "不等候命令退出")
	return cmd
}

// newImagesCommand 实现 `boxli images`。
func newImagesCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "im"},
		Short:   "列出本地 .boxli 镜像",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("images")
		},
	}
}
