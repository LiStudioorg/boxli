// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/engine"
	"github.com/LiStudioorg/licore/internal/store"
)

// newStopCommand 实现 `boxli stop`：停止容器并标记 stopped-by-user。
func newStopCommand(out io.Writer) *cobra.Command {
	var (
		dataDir string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "stop <容器ID|名字>",
		Short: "停止容器（标记 stopped-by-user，unless-stopped 下次开机不再拉起）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defer cleanupCmdContext(cmd)
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			res, err := engine.StopWithContext(sigCtx(cmd), st, args[0], timeout)
			if err != nil {
				if cmd.Context().Err() != nil {
					return fmt.Errorf("stop 已取消")
				}
				return err
			}
			switch {
			case !res.WasRunning:
				fmt.Fprintf(out, "容器 %s（%s）本就未在运行，已记录停止标记\n",
					res.Container.Name, res.Container.ID)
			case res.Forced:
				fmt.Fprintf(out, "容器 %s（%s）未在 %s 内退出，已强制终止\n",
					res.Container.Name, res.Container.ID, timeout)
			default:
				fmt.Fprintf(out, "容器 %s（%s）已停止（耗时 %s）\n",
					res.Container.Name, res.Container.ID, res.Waited.Round(time.Millisecond))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	cmd.Flags().DurationVarP(&timeout, "time", "t", engine.DefaultStopTimeout,
		"等待容器退出的宽限，超时后 SIGKILL")
	// 容器 init 是 PID namespace 的 1 号进程：未安装 SIGTERM 处理时内核忽略
	// 该信号（PID-1 保护），最终由 shim 的宽限兜底强杀，退出码 137。属预期行为。
	cmd.Long = cmd.Short + "\n\n" +
		"停止流程：写 stopped-by-user 标记 → 向 shim 发 SIGTERM（shim 转发容器 init）\n" +
		"→ 宽限内等其退出 → 超时对 shim 与 init 强杀（SIGKILL，退出码 137）。\n\n" +
		"注意：容器 1 号进程未自行处理 SIGTERM 时，内核会忽略该信号（PID-1 保护），\n" +
		"此类容器会在 shim 宽限到期后被强杀，这是预期行为而非缺陷。"
	return cmd
}
