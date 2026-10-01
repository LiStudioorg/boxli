// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newRunCommand 实现 `boxli run`。阶段 1 仅定义参数面，运行时在阶段 2 落地。
func newRunCommand(out io.Writer) *cobra.Command {
	var opts struct {
		detach     bool
		restart    string
		name       string
		memoryMB   int
		cpus       float64
		pidsLimit  int
		env        []string
		workdir    string
		user       string
		entrypoint []string
	}
	cmd := &cobra.Command{
		Use:   "run [flags] <image> [command...]",
		Short: "创建并启动容器",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch opts.restart {
			case "no", "always", "unless-stopped", "on-failure":
			default:
				return fmt.Errorf("run: 非法 --restart 值 %q（可选：no|always|unless-stopped|on-failure）", opts.restart)
			}
			return notImplemented("run")
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.detach, "detach", "d", false, "后台运行，打印容器 ID")
	f.StringVar(&opts.restart, "restart", "no", "重启策略：no|always|unless-stopped|on-failure")
	f.StringVar(&opts.name, "name", "", "容器名")
	f.IntVar(&opts.memoryMB, "memory", 0, "内存上限（MiB，0=不限制）")
	f.Float64Var(&opts.cpus, "cpus", 0, "CPU 配额（核数，0=不限制）")
	f.IntVar(&opts.pidsLimit, "pids-limit", 0, "进程数上限（0=不限制）")
	f.StringArrayVarP(&opts.env, "env", "e", nil, "环境变量 KEY=VALUE，可重复")
	f.StringVar(&opts.workdir, "workdir", "", "工作目录")
	f.StringVar(&opts.user, "user", "", "运行用户 uid:gid")
	f.StringSliceVar(&opts.entrypoint, "entrypoint", nil, "覆盖镜像 entrypoint")
	_ = out // 阶段 2 输出容器 ID 时使用
	return cmd
}
