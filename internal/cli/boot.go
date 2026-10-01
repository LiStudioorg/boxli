// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// newBootTestCommand 实现 `boxli boot` 及子命令 enable/disable/status。
// 设计见 AGENTS.md「开机自启动机制」：boot/on 是一次性命令，非常驻。
func newBootTestCommand(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "boot",
		Short: "开机时由系统服务调用，一次性拉起自启容器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("boot")
		},
	}

	enable := &cobra.Command{
		Use:   "enable",
		Short: "启用开机自启（自动检测平台并写入系统服务）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("boot enable")
		},
	}
	disable := &cobra.Command{
		Use:   "disable",
		Short: "关闭开机自启（移除系统服务文件并取消注册）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("boot disable")
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "查看开机自启状态与自启容器列表",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("boot status")
		},
	}

	cmd.AddCommand(enable, disable, status)
	return cmd
}

// newShutdownCommand 实现 `boxli shutdown`：由系统服务停止时调用，优雅停止自启容器。
func newShutdownCommand(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "停止所有自启容器（由系统服务 ExecStop 调用）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return notImplemented("shutdown")
		},
	}
}
