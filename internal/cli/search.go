// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newSearchCommand 实现 `licore search QUERY`：按关键字搜索 Hub 上的镜像。
func newSearchCommand(out io.Writer) *cobra.Command {
	var hubFlag, dataDir string
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "在 Hub 上搜索镜像",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base := hubBaseURL(hubFlag)
			c := newHubClient(base, dataDir)
			if c.Token == "" {
				return fmt.Errorf("search: 未登录 %s，请先 licore login", base)
			}
			res, err := c.Search(args[0])
			if err != nil {
				return err
			}
			if len(res) == 0 {
				fmt.Fprintf(out, "未在 %s 找到匹配 %q 的镜像\n", base, args[0])
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVERSION\tDIGEST")
			for _, r := range res {
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.Name, r.Version, r.Digest)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&hubFlag, "hub", "", "Hub 地址（默认 $LICORE_HUB 或 http://127.0.0.1:3727）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	return cmd
}
