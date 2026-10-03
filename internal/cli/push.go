// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// newPushCommand 实现 `licore push NAME:VERSION file.licore`：
// 把本地 .licore 文件上传到 Hub（内容寻址 blob + 打 tag），令牌取自登录缓存。
func newPushCommand(out io.Writer) *cobra.Command {
	var hubFlag, dataDir string
	cmd := &cobra.Command{
		Use:   "push NAME:VERSION file.licore",
		Short: "推送本地 .licore 镜像到 Hub",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, file := args[0], args[1]
			if _, err := os.Stat(file); err != nil {
				return fmt.Errorf("push: 打开 %s: %w", file, err)
			}
			base := hubBaseURL(hubFlag)
			c := newHubClient(base, dataDir)
			if c.Token == "" {
				return fmt.Errorf("push: 未登录 %s，请先 licore login", base)
			}
			if err := c.Push(ref, file); err != nil {
				return err
			}
			fmt.Fprintf(out, "已推送 %s 到 %s\n", ref, base)
			return nil
		},
	}
	cmd.Flags().StringVar(&hubFlag, "hub", "", "Hub 地址（默认 $LICORE_HUB 或 http://127.0.0.1:3727）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	return cmd
}
