// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/compose"
	"github.com/LiStudioorg/boxli/internal/engine"
	"github.com/LiStudioorg/boxli/internal/store"
)

// composeCmd 持有 compose 父命令的共享配置。
type composeCmd struct {
	out, errOut io.Writer
	file        string
	dataDir     string
}

// newComposeCommand 实现 `boxli compose`。
func newComposeCommand(out io.Writer) *cobra.Command {
	cc := &composeCmd{out: out}
	cmd := &cobra.Command{
		Use:   "compose",
		Short: "Boxli compose 服务编排",
		Long:  "解析自研 compose 文件（默认 ./boxli-compose.yaml）并对服务做 up/down/ps/logs/scale 编排。",
	}
	cmd.PersistentFlags().StringVarP(&cc.file, "file", "f", "boxli-compose.yaml", "compose 文件路径")
	cmd.PersistentFlags().StringVarP(&cc.dataDir, "data-dir", "", "", "数据目录")
	cmd.AddCommand(cc.config(out))
	cmd.AddCommand(cc.up(out))
	cmd.AddCommand(cc.down(out))
	cmd.AddCommand(cc.ps(out))
	cmd.AddCommand(cc.logs(out))
	cmd.AddCommand(cc.scale(out))
	return cmd
}

// projectPrefix 是容器名命名规范 "<project>_<service>" 的前缀。
func (cc *composeCmd) projectPrefix(p *compose.Project) string { return p.Name + "_" }

// load 载入并解析项目。
func (cc *composeCmd) load() (*compose.Project, []*compose.ResolvedService, *store.Store, error) {
	p, err := compose.LoadFile(cc.file)
	if err != nil {
		return nil, nil, nil, err
	}
	services, err := p.Resolve()
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := store.Open(cc.dataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	return p, services, st, nil
}

func (cc *composeCmd) config(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "校验并输出解析后的项目",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, services, _, err := cc.load()
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "# project %s (%s)\n", p.Name, orDashC(p.Version))
			for _, s := range services {
				fmt.Fprintf(out, "%s\timage=%s\tsource=%s\treplicas=%d\n",
					s.Name, orDashC(s.Image), orDashC(srcOf(s)), s.Replicas)
			}
			return nil
		},
	}
}

func (cc *composeCmd) up(out io.Writer) *cobra.Command {
	var detach bool
	c := &cobra.Command{
		Use:   "up [SERVICE...]",
		Short: "创建并启动服务（缺省全部）",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, services, st, err := cc.load()
			if err != nil {
				return err
			}
			_ = st
			want := map[string]bool{}
			for _, a := range args {
				want[a] = true
			}
			for _, s := range services {
				if len(want) > 0 && !want[s.Name] {
					continue
				}
				if s.Image == "" {
					fmt.Fprintf(out, "服务 %s：源为 %s，需先构建再 up（本分支输出编排计划）\n", s.Name, srcOf(s))
					continue
				}
				name, version := splitComposeRef(s.Image)
				ok, err := st.Exists(name, version)
				if err != nil || !ok {
					fmt.Fprintf(out, "服务 %s：镜像 %s 未导入，请先 pull\n", s.Name, s.Image)
					continue
				}
				// 计划确认；实际启动由 runtime 编排（见交接摘要）。
				fmt.Fprintf(out, "服务 %s → %s（镜像 %s 已就绪，容器名前缀 %s%s）\n",
					s.Name, s.Image, s.Image, cc.projectPrefix(p), s.Name)
			}
			_ = detach
			return nil
		},
	}
	c.Flags().BoolVarP(&detach, "detach", "d", false, "后台运行（占位：计划确认）")
	return c
}

func (cc *composeCmd) down(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "停止并移除项目的容器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, _, st, err := cc.load()
			if err != nil {
				return err
			}
			return cc.forEachProjectContainer(st, p, func(c *store.ContainerConfig) error {
				_, err := engine.Stop(st, c.ID, time.Second*15)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "已停止 %s\n", c.Name)
				return nil
			})
		},
	}
}

func (cc *composeCmd) ps(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "列出该项目下运行的容器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, _, st, err := cc.load()
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tIMAGE")
			return cc.forEachProjectContainer(st, p, func(c *store.ContainerConfig) error {
				fmt.Fprintf(tw, "%s\t%s\n", c.Name, c.ImageRef)
				return nil
			})
		},
	}
}

func (cc *composeCmd) logs(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "logs SERVICE",
		Short: "显示服务容器的日志",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, st, err := cc.load()
			if err != nil {
				return err
			}
			prefix := cc.projectPrefix(p) + args[0]
			containers, err := st.ListContainers()
			if err != nil {
				return err
			}
			for _, c := range containers {
				if !strings.HasPrefix(c.Name, prefix) {
					continue
				}
				logPath := filepath.Join(st.ContainerDir(c.ID), "container.log")
				data, rerr := os.ReadFile(logPath)
				if rerr != nil {
					continue
				}
				fmt.Fprintln(out, string(data))
			}
			return nil
		},
	}
}

func (cc *composeCmd) scale(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "scale SERVICE=N",
		Short: "设置服务副本数（占位：输出目标副本数）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, a := range args {
				svc, n, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("compose scale: %q 应为 SERVICE=N", a)
				}
				fmt.Fprintf(out, "服务 %s → %s 副本\n", svc, n)
			}
			return nil
		},
	}
}

// forEachProjectContainer 对匹配项目前缀的容器执行 fn。
func (cc *composeCmd) forEachProjectContainer(st *store.Store, p *compose.Project, fn func(*store.ContainerConfig) error) error {
	prefix := cc.projectPrefix(p)
	containers, err := st.ListContainers()
	if err != nil {
		return err
	}
	for _, c := range containers {
		if strings.HasPrefix(c.Name, prefix) {
			if err := fn(c); err != nil {
				return err
			}
		}
	}
	return nil
}

func srcOf(s *compose.ResolvedService) string {
	switch {
	case s.Boxfile != "":
		return "boxfile:" + s.Boxfile
	case s.Build != "":
		return "build:" + s.Build
	default:
		return "unknown"
	}
}

func splitComposeRef(ref string) (name, version string) {
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return ref, ""
	}
	return ref[:i], ref[i+1:]
}

// orDashC 是 compose 命令的 orDash 辅助。
func orDashC(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
