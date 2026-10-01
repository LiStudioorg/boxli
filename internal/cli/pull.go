// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/store"
)

// newPullCommand 实现 `boxli pull`。当前支持本地 .boxli 文件：
//
//	boxli pull ./myapp-1.0.boxli
func newPullCommand(out io.Writer) *cobra.Command {
	var (
		force   bool
		rootDir string
	)
	cmd := &cobra.Command{
		Use:   "pull <file.boxli>",
		Short: "从本地 .boxli 文件导入镜像（校验 index.json 与层 digest）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPull(out, args[0], force, rootDir)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "同 name/version 已存在时覆盖")
	cmd.Flags().StringVar(&rootDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}

func runPull(out io.Writer, srcPath string, force bool, rootDir string) error {
	st, err := store.Open(rootDir)
	if err != nil {
		return err
	}
	loaded, err := st.Put(srcPath, force)
	if err != nil {
		return fmt.Errorf("pull %s: %w", srcPath, err)
	}
	m := loaded.Manifest
	fmt.Fprintf(out, "已导入镜像 %s（%s/%s，%d 层，全部摘要校验通过）\n",
		m.Ref(), m.OS, m.Architecture, len(m.Layers))
	fmt.Fprintf(out, "落地目录：%s\n", st.ImageDir(m.Name, m.Version))

	suggestBoot(out, st)
	return nil
}

// suggestBoot 实现"首次使用引导"（AGENTS.md）：数据目录中没有 boot 标记文件时询问用户。
// boot enable 本体在阶段 2 落地；此处确认后仅记录标记，不执行半截系统操作。
func suggestBoot(out io.Writer, st *store.Store) {
	marker := st.BootMarker()
	if marker == "" {
		return
	}
	if _, err := os.Stat(marker); err == nil {
		return // 已启用或已询问过
	}
	fmt.Fprint(out, "\n检测到 Boxli 尚未启用开机自启\n是否启用？启用后开机会自动拉起设置了 restart=always 的容器\n[y/N]: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	ack := "declined\n"
	switch {
	case err != nil && line == "":
		// 非交互输入（EOF）：视为默认拒绝，记录标记避免反复打断脚本执行。
		fmt.Fprintln(out, "已跳过。")
	case strings.EqualFold(strings.TrimSpace(line), "y"), strings.EqualFold(strings.TrimSpace(line), "yes"):
		fmt.Fprintln(out, "boxli boot enable 尚未实现（阶段 2 落地），已记录引导结果，不再重复询问。")
		ack = "prompted\n"
	default:
		fmt.Fprintln(out, "已跳过。可随时执行 boxli boot enable 启用。")
	}
	if err := st.EnsureBootDir(); err != nil {
		return
	}
	_ = os.WriteFile(marker, []byte(ack), 0o644)
}
