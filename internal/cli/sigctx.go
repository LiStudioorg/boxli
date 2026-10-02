// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// sigCtx 返回一个在 SIGINT/SIGTERM 时取消的上下文，供 stop/rm 等命令支持
// Ctrl+C：避免在 shim/netlink 等底层卡住时命令永久挂起。返回的 ctx 也已
// 写回 cmd；调用方应 defer cleanupCmdContext 在命令结束释放监听。
func sigCtx(cmd *cobra.Command) context.Context {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	cmd.SetContext(ctx)
	stopMu.Lock()
	stopFns = append(stopFns, stop)
	stopMu.Unlock()
	return ctx
}

// cleanupCmdContext 释放 sigCtx 注册的 signal.Notify 监听，避免泄漏。
func cleanupCmdContext(_ *cobra.Command) {
	stopMu.Lock()
	fns := stopFns
	stopFns = nil
	stopMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

var (
	stopMu  sync.Mutex
	stopFns []func()
)
