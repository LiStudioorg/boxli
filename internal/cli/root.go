// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package cli 组装 Boxli 的 cobra 命令树。这是全仓库唯一允许 import
// github.com/spf13/cobra 的包（main.go 除外），业务逻辑一律放在 internal/* 中。
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
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
		newPsCommand(out),
		newExecCommand(out),
		newImagesCommand(out),
		newBootTestCommand(out),
		newShutdownCommand(out),
	)
	return root
}

// Execute 运行命令树，返回进程退出码。调用方（main）负责 os.Exit。
func Execute() int {
	root := NewRootCommand(os.Stdout, os.Stderr)
	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "boxli: %v\n", err)
		return 1
	}
	return 0
}

// notImplemented 统一生成"骨架阶段未实现"错误，避免各命令文案漂移。
func notImplemented(name string) error {
	return fmt.Errorf("boxli %s: 尚未实现（阶段 1 骨架）", name)
}
