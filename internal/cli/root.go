// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package cli 组装 Boxli 的 cobra 命令树。这是全仓库唯一允许 import
// github.com/spf13/cobra 的包（main.go 除外），业务逻辑一律放在 internal/* 中。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/runtime"
	"github.com/LiStudioorg/boxli/internal/shim"
)

// Version 由 main 注入（可通过 -ldflags 覆盖），用于 --version。
var Version = "0.0.0-dev"

// NewRootCommand 构建 boxli 根命令及全部子命令。
func NewRootCommand(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "boxli",
		Short:         "Boxli —— 自研生态的轻量级容器引擎",
		Long:          "Boxli 是一个轻量级容器引擎：自研 .boxli 镜像格式，不兼容 Docker / OCI。\n详见 https://github.com/LiStudioorg/boxli",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.ArbitraryArgs,
	}
	root.SetOut(out)
	root.SetErr(errOut)

	root.AddCommand(
		newPullCommand(out),
		newRunCommand(out),
		newStopCommand(out),
		newRmCommand(out),
		newPsCommand(out),
		newExecCommand(out),
		newImagesCommand(out),
		newNetworkCommand(out),
		newVolumeCommand(out),
		newStatsCommand(out),
		newResourceCommand(out),
		newBootTestCommand(out),
		newShutdownCommand(out),
		newInitCommand(out),
		newSpikeCommand(out),
		newShimCommand(out),
		// 镜像产物操作。
		newTagCommand(out),
		newCommitCommand(out),
		newSaveCommand(out),
		newLoadCommand(out),
		newExportCommand(out),
		newImportCommand(out),
		// Hub 分发。
		newLoginCommand(out),
		newPushCommand(out),
		newSearchCommand(out),
		// 编排与工具。
		newComposeCommand(out),
		newDevCommand(out),
		newBuildCommand(out),
		newDoctorCommand(out),
		newLintCommand(out),
		newScaffoldCommand(out),
		newCompletionCommand(root, out),
	)
	return root
}

// Execute 运行命令树，返回进程退出码。调用方（main）负责 os.Exit。
// 若本进程是被 fork 的容器 init（BOXLI_CHILD=1），跳过命令解析直接进初始化路径。
func Execute() int {
	setupLogging()
	if runtime.IsInitProcess() {
		if err := runtime.RunInit(); err != nil {
			fmt.Fprintf(os.Stderr, "boxli init: %v\n", err)
			return 1
		}
		return 0
	}
	sigCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if shim.IsShimProcess() {
		if err := shim.RunFromEnv(sigCtx); err != nil {
			fmt.Fprintf(os.Stderr, "boxli shim: %v\n", err)
			return 1
		}
		return 0
	}
	root := NewRootCommand(os.Stdout, os.Stderr)
	if err := root.Execute(); err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "boxli: %v\n", err)
		return 1
	}
	return 0
}

// notImplemented 统一生成"尚未实现"错误，避免各命令文案漂移。
func notImplemented(name string) error {
	return fmt.Errorf("boxli %s: 尚未实现", name)
}

// setupLogging 初始化 log/slog：默认 Warn 级别，BOXLI_LOG=debug|info 提升。
func setupLogging() {
	lvl := slog.LevelWarn
	switch strings.ToLower(os.Getenv("BOXLI_LOG")) {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})))
}
