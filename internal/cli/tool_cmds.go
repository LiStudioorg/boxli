// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/doctor"
	"github.com/LiStudioorg/licore/internal/scaffold"
)

// newDoctorCommand 实现 `boxli doctor`：环境自检。
func newDoctorCommand(out io.Writer) *cobra.Command {
	var dataDir, version string
	var testRun bool
	var skip []string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "环境自检（内核/namespace/cgroup/systemd/存储）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			opts := &doctor.Options{
				DataDir: dataDir,
				Version: version,
				Skip:    skip,
				TestRun: testRun,
			}
			rep, err := doctor.Diagnose(ctx, opts)
			if err != nil {
				return err
			}
			return doctor.RenderText(out, rep)
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录")
	cmd.Flags().StringVar(&version, "version", Version, "版本号（默认 CLI 注入）")
	cmd.Flags().BoolVar(&testRun, "test-run", false, "启用真实容器冒烟测试")
	cmd.Flags().StringSliceVar(&skip, "skip", nil, "跳过的检查 ID，逗号分隔")
	return cmd
}

// newLintCommand 实现 `boxli lint`。
func newLintCommand(out io.Writer) *cobra.Command {
	var file, dir string
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "检查 compose / boxfile 文件或以目录为项目的配置",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var results []*scaffold.Result
			switch {
			case dir != "":
				r, err := scaffold.LintProject(dir)
				if err != nil {
					return err
				}
				results = append(results, r)
			case file != "":
				r, err := scaffold.LintBoxfile(file)
				if err != nil {
					// 可能是 compose 文件，回退。
					r2, err2 := scaffold.LintComposeFile(file)
					if err2 != nil {
						return err
					}
					results = append(results, r2)
				} else {
					results = append(results, r)
				}
			default:
				return fmt.Errorf("lint: 需要 --file 或 --dir")
			}
			_, _, err := scaffold.LintReport(out, results...)
			return err
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "要检查的 boxfile / compose 文件")
	cmd.Flags().StringVar(&dir, "dir", "", "项目目录（检查 boxfile + compose）")
	return cmd
}

// newScaffoldCommand 实现 `boxli scaffold init`：生成 boxfile / compose 脚手架。
// （`boxli init` 已被隐藏的容器 1 号进程入口占用，故脚手架放到 scaffold 命令下。）
func newScaffoldCommand(out io.Writer) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "scaffold",
		Short: "项目脚手架生成与文件模板",
	}
	init := &cobra.Command{
		Use:   "init",
		Short: "生成 boxli 项目脚手架（boxfile / compose）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := scaffold.Init(".", force)
			if err != nil {
				return err
			}
			for _, f := range res.Files {
				fmt.Fprintf(out, "已创建 %s\n", f)
			}
			return nil
		},
	}
	init.Flags().BoolVarP(&force, "force", "f", false, "覆盖已存在文件")
	cmd.AddCommand(init)
	return cmd
}

// newCompletionCommand 生成 shell 补全脚本（cobra 内置）。
func newCompletionCommand(root *cobra.Command, out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "生成 shell 补全脚本",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(out)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			default:
				return fmt.Errorf("completion: 未知 shell %q（bash|zsh|fish）", args[0])
			}
		},
	}
}

var _ = os.Exit
