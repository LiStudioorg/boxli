// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"log/slog"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/resource"
	"github.com/LiStudioorg/licore/internal/store"
)

// newStatsCommand 实现 `licore stats`：读取容器 cgroup 用量并实时展示。
// 无参数时列全部容器；也可指定容器名/ID（支持前缀）。
func newStatsCommand(out io.Writer) *cobra.Command {
	var dataDir string
	var all bool
	cmd := &cobra.Command{
		Use:     "stats [CONTAINER...]",
		Aliases: []string{"top"},
		Short:   "显示容器资源用量（CPU/内存/进程）",
		Args:    cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			containers, err := st.ListContainers()
			if err != nil {
				return err
			}
			// 收集需要展示的容器。
			var targets []*store.ContainerConfig
			if len(args) == 0 {
				targets = containers
			} else {
				for _, a := range args {
					c, err := st.FindContainer(a)
					if err != nil {
						return err
					}
					targets = append(targets, c)
				}
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "CONTAINER\tCPU\tMEMORY\tMEM.LIM\tPIDS\tPIDS.LIM\tUP")
			for _, c := range targets {
				rtState, hasState, err := st.ReadRuntimeState(c.ID)
				if err != nil {
					slog.Debug("读运行状态失败", "container", c.ID, "err", err)
				}
				s, err := resource.StatsFor(c.ID)
				if err != nil {
					slog.Debug("stats 采集失败", "container", c.ID, "err", err)
					status := psStatus(st, c.ID, rtState, hasState, false, false)
					fmt.Fprintf(tw, "%s\tn/a\tn/a\tn/a\tn/a\tn/a\t%s\n", c.Name, status)
					continue
				}
				status := psStatus(st, c.ID, rtState, hasState, s.Running, false)
				if s.Running || all {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						c.Name,
						friendlyCPU(s.CPUUsageNanos),
						resource.FormatBytes(s.MemoryUsage),
						bytesOrMax(s.MemoryLimit),
						s.PidsCurrent,
						fmt.Sprintf("%d", s.PidsLimit),
						status,
					)
				}
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "含已停止容器")
	return cmd
}

func friendlyCPU(nanos int64) string {
	if nanos <= 0 {
		return "0s"
	}
	return fmt.Sprintf("%.2fs", float64(nanos)/1e9)
}

func bytesOrMax(v int64) string {
	if v <= 0 {
		return "unlimited"
	}
	return resource.FormatBytes(v)
}
