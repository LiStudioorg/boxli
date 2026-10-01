// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// newPullCommand 实现 `boxli pull`。当前支持本地 .boxli 文件：
//
//	boxli pull ./myapp-1.0.boxli
func newPullCommand(out io.Writer) *cobra.Command {
	var (
		force   bool
		rootDir string
	)
	cmd := &cobra.Command{
		Use:   "pull <file.boxli>",
		Short: "从本地 .boxli 文件导入镜像（校验 index.json 与层 digest）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("pull")
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "同 name/version 已存在时覆盖")
	cmd.Flags().StringVar(&rootDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}
