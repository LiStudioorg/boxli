// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/runtime"
)

// newSpikeCommand 注册隐藏命令 `licore dev-run`：spike / 基准测试入口，
// 直接在指定 rootfs 上以前台方式运行容器，等待退出并回传退出码。
// 与 `licore run`（正式命令）共用 internal/runtime。
func newSpikeCommand(out io.Writer) *cobra.Command {
	var (
		rootfs   string
		hostname string
		env      []string
	)
	cmd := &cobra.Command{
		Use:    "dev-run --rootfs <dir> -- <command...>",
		Short:  "以指定 rootfs 前台运行容器（开发/基准测试用）",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runtime.Start(&runtime.Config{
				Rootfs:   rootfs,
				Hostname: hostname,
				Env:      env,
				Cmd:      args,
			}, nil)
			if err != nil {
				return fmt.Errorf("dev-run: %w", err)
			}
			if res.ExitCode != 0 {
				cmd.SilenceUsage = true
				return &exitCodeError{code: res.ExitCode}
			}
			slog.Debug("容器正常退出", "pid", res.ChildPID)
			return nil
		},
	}
	cmd.Flags().StringVar(&rootfs, "rootfs", "", "容器根目录（必填）")
	cmd.Flags().StringVar(&hostname, "hostname", "licore", "容器 hostname")
	cmd.Flags().StringArrayVarP(&env, "env", "e", nil, "环境变量 KEY=VALUE，可重复")
	_ = cmd.MarkFlagRequired("rootfs")
	return cmd
}

// exitCodeError 把容器退出码转译为 licore 的进程退出码。
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("容器以退出码 %d 结束", e.code) }

// ExitCode 实现 main 侧统一取码。
func (e *exitCodeError) ExitCode() int { return e.code }
