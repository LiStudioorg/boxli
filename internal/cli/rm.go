// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/engine"
	"github.com/LiStudioorg/boxli/internal/store"
)

// newRmCommand 实现 `boxli rm`：删除已停止容器的状态目录。
func newRmCommand(out io.Writer) *cobra.Command {
	var (
		dataDir string
		force   bool
	)
	cmd := &cobra.Command{
		Use:   "rm [flags] <容器ID|名字>...",
		Short: "删除已停止的容器（运行中需先 boxli stop）",
		Long: "删除容器：移除 <数据目录>/containers/<id>/ 下的配置、运行状态、日志与\n" +
			"该容器独占的 rootfs。共享的镜像层缓存（layers/sha256/<hex>）保留，\n" +
			"其他容器仍可复用。\n\n" +
			"运行中的容器会拒绝删除并提示先 boxli stop；-f 可强制删除（先停止再删除）。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			var failed int
			for _, target := range args {
				res, err := engine.Remove(st, target, force)
				if err != nil {
					// 多个目标时逐个报错，全部处理完再以非零码收敛。
					fmt.Fprintf(cmd.ErrOrStderr(), "boxli rm: %v\n", err)
					failed++
					continue
				}
				if res.Stopped {
					fmt.Fprintf(out, "容器 %s（%s）先前在运行，已停止并删除\n", res.Container.Name, res.Container.ID)
				} else {
					fmt.Fprintf(out, "已删除容器 %s（%s）\n", res.Container.Name, res.Container.ID)
				}
			}
			if failed > 0 {
				return &exitCodeError{code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "运行中也删除（先停止）")
	return cmd
}
