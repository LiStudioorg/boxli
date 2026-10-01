// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/store"
)

// newImagesCommand 实现 `boxli images`：从 store 的 state.json 列出本地镜像。
func newImagesCommand(out io.Writer) *cobra.Command {
	var (
		quiet  bool
		format string
		root   string
	)
	cmd := &cobra.Command{
		Use:     "images",
		Aliases: []string{"image", "im"},
		Short:   "列出本地 .boxli 镜像",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(root)
			if err != nil {
				return err
			}
			infos, err := st.ListImages()
			if err != nil {
				return err
			}
			return renderImages(cmd.OutOrStdout(), infos, quiet, format)
		},
	}
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "只输出镜像引用 name:version")
	cmd.Flags().StringVar(&format, "format", "", "Go 模板自定义输出，例如 '{{.Ref}} {{.Architecture}}'")
	cmd.Flags().StringVar(&root, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}

// rendered 是单行镜像信息格式化后的字段集，供模板与表格共用。
type rendered struct {
	Repository   string
	Tag          string
	Architecture string
	Layers       int
	Size         string
	Created      string
	Ref          string
}

func renderImages(w io.Writer, infos []store.ImageInfo, quiet bool, format string) error {
	if len(infos) == 0 {
		if quiet || format != "" {
			return nil // 机器可读模式：无数据即无输出
		}
		fmt.Fprintln(w, "暂无本地镜像（运行 boxli pull <文件.boxli> 导入）")
		return nil
	}

	rows := make([]rendered, 0, len(infos))
	for _, in := range infos {
		repo, tag := splitRef(in.Ref)
		rows = append(rows, rendered{
			Repository:   repo,
			Tag:          tag,
			Architecture: in.Architecture,
			Layers:       in.LayerCount,
			Size:         humanBytes(in.SourceFileBytes),
			Created:      formatCreated(in.PulledAt),
			Ref:          in.Ref,
		})
	}

	switch {
	case format != "":
		tmpl, err := template.New("images").Parse(format)
		if err != nil {
			return fmt.Errorf("boxli images: --format 模板非法: %w", err)
		}
		for _, r := range rows {
			if err := tmpl.Execute(w, r); err != nil {
				return fmt.Errorf("boxli images: 渲染失败: %w", err)
			}
			fmt.Fprintln(w)
		}
	case quiet:
		for _, r := range rows {
			fmt.Fprintln(w, r.Ref)
		}
	default:
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "REPOSITORY\tTAG\tARCH\tLAYERS\tSIZE\tCREATED")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n",
				r.Repository, r.Tag, r.Architecture, r.Layers, r.Size, r.Created)
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("boxli images: 输出失败: %w", err)
		}
	}
	return nil
}

// splitRef 拆分 name:version；无冒号时 tag 为空。
func splitRef(ref string) (repo, tag string) {
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// humanBytes 把字节数格式化为易读单位（10 进制，与镜像体积习惯一致）。
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// formatCreated 把 UTC RFC3339 导入时间渲染为本地 "2006-01-02 15:04"；
// 解析失败时原样返回，保证 listing 不因个别脏数据中断。
func formatCreated(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.Local().Format("2006-01-02 15:04")
}
