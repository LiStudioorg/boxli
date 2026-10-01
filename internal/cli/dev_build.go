// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/build"
	"github.com/LiStudioorg/boxli/internal/dev"
)

// newBuildCommand 实现 `boxli build`：解析 Boxfile 并输出构建计划。
// 实际层打包/合并由 image 打包模块驱动，此处做解析 + 校验 + 规划。
func newBuildCommand(out io.Writer) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "build [--file Boxfile]",
		Short: "解析 Boxfile 并输出构建计划",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				if _, err := os.Stat("Boxfile"); err == nil {
					file = "Boxfile"
				} else if _, err := os.Stat("boxfile"); err == nil {
					file = "boxfile"
				} else {
					return fmt.Errorf("build: 未找到 Boxfile，用 --file 指定")
				}
			}
			bf, err := build.ParseBoxfileFile(file)
			if err != nil {
				return err
			}
			// 校验构建上下文可访问。
			if err := build.CheckContext("."); err != nil {
				fmt.Fprintf(out, "提示: 构建上下文检查: %v\n", err)
			}
			fmt.Fprintf(out, "== Boxfile %s ==\n", file)
			fmt.Fprintf(out, "FROM %s\n", bf.From)
			for _, ins := range bf.Instructions {
				var buf bytes.Buffer
				buf.WriteString(ins.Op)
				for _, a := range ins.Args {
					buf.WriteString(" " + a)
				}
				fmt.Fprintf(out, "  %d: %s\n", ins.Line, buf.String())
			}
			fmt.Fprintf(out, "== 构建计划已就绪（%d 条指令）==\n", len(bf.Instructions))
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "Boxfile 路径（默认 ./Boxfile 或 ./boxfile）")
	return cmd
}

// newDevCommand 实现 `boxli dev`：文件热重载。
// 监听路径，文件变化时去抖并输出批次（真实重建/重启由运行时编排）。
func newDevCommand(out io.Writer) *cobra.Command {
	var watch []string
	var ignore []string
	var once bool
	cmd := &cobra.Command{
		Use:   "dev",
		Short: "开发模式：文件热重载监听",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(watch) == 0 {
				watch = []string{"."}
			}
			ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			spec := dev.WatchSpec{Roots: watch, Ignore: ignore, Debounce: 300 * time.Millisecond}
			fmt.Fprintf(out, "监听 %v（Ctrl+C 退出）\n", watch)
			for {
				changed, err := dev.WaitForChange(ctx, spec)
				if err != nil {
					return err
				}
				for _, p := range changed {
					fmt.Fprintf(out, "变更: %s\n", p)
				}
				if once {
					return nil
				}
			}
		},
	}
	cmd.Flags().StringArrayVar(&watch, "watch", nil, "要监听的目录，可重复（默认 .）")
	cmd.Flags().StringArrayVar(&ignore, "ignore", nil, "忽略模式，可重复")
	cmd.Flags().BoolVar(&once, "once", false, "检测到一次变更后退出")
	return cmd
}
