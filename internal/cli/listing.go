// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/runtime"
	"github.com/LiStudioorg/licore/internal/store"
)

// newExecCommand 实现 `licore exec`：setns 进入运行中容器的命名空间执行命令。
//
//	licore exec <容器ID|名字> <command...>
//	licore exec -it myapp /bin/sh
func newExecCommand(out io.Writer) *cobra.Command {
	var opts struct {
		interactive bool
		tty         bool
		user        string
		workdir     string
		env         []string
		dataDir     string
	}
	cmd := &cobra.Command{
		Use:   "exec [flags] <容器ID|名字> <command...>",
		Short: "在运行中的容器里执行命令",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(opts.dataDir)
			if err != nil {
				return err
			}
			cfg, err := st.FindContainer(args[0])
			if err != nil {
				return err
			}
			state, _, err := st.ReadRuntimeState(cfg.ID)
			if err != nil {
				return err
			}
			if !state.Running || state.InitPID <= 0 {
				return fmt.Errorf("exec: 容器 %s（%s）未在运行: %w", cfg.Name, cfg.ID, runtime.ErrNotInit)
			}
			// 校验运行身份格式（uid[:gid]）。
			if opts.user != "" {
				uid, _, _ := strings.Cut(opts.user, ":")
				if uid == "" {
					return fmt.Errorf("exec: 非法 --user %q", opts.user)
				}
			}
			code, err := runtime.Exec(&runtime.ExecOptions{
				TargetPID: state.InitPID,
				Cmd:       args[1:],
				Env:       opts.env,
				Workdir:   opts.workdir,
				User:      opts.user,
				TTY:       opts.tty,
			})
			if err != nil {
				if errors.Is(err, runtime.ErrNotRoot) {
					return fmt.Errorf("exec: 需要 root（setns 进入他人命名空间）: %w", err)
				}
				return err
			}
			if code != 0 {
				return &exitCodeError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&opts.interactive, "interactive", "i", false, "保持 stdin 打开")
	cmd.Flags().BoolVarP(&opts.tty, "tty", "t", false, "分配伪终端")
	cmd.Flags().StringVarP(&opts.user, "user", "u", "", "运行用户 uid[:gid]")
	cmd.Flags().StringVarP(&opts.workdir, "workdir", "w", "", "工作目录（容器内路径）")
	cmd.Flags().StringArrayVarP(&opts.env, "env", "e", nil, "环境变量 KEY=VALUE，可重复")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	return cmd
}
