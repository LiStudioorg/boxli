// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/shim"
	"github.com/LiStudioorg/licore/internal/store"
)

// newShimCommand 注册隐藏命令 `boxli dev-shim`：为已创建的容器 fork 一个
// 脱离终端的 shim（开发/测试入口；正式路径是 run -d 与 boot 内部调用）。
func newShimCommand(out io.Writer) *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:    "dev-shim <容器ID|名字>",
		Short:  "为容器 fork shim 进程（开发用）",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(root)
			if err != nil {
				return err
			}
			cfg, err := st.FindContainer(args[0])
			if err != nil {
				return err
			}
			p, err := shim.Reexec(st.Root, cfg.ID)
			if err != nil {
				return fmt.Errorf("dev-shim: %w", err)
			}
			fmt.Fprintf(out, "shim 已启动：容器 %s（%s），shim PID %d\n", cfg.ID, cfg.Name, p.Pid)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}
