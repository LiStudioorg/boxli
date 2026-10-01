// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/boxli/internal/boot"
	"github.com/LiStudioorg/boxli/internal/store"
)

// newPsCommand 实现 `boxli ps`：默认只列运行中容器，-a 含已停止。
func newPsCommand(out io.Writer) *cobra.Command {
	var (
		all     bool
		quiet   bool
		dataDir string
	)
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "列出容器（默认仅运行中，-a 含已停止）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			rows, err := collectContainerRows(st, all)
			if err != nil {
				return err
			}
			if quiet {
				for _, r := range rows {
					fmt.Fprintln(out, r.ID)
				}
				return nil
			}
			if len(rows) == 0 {
				if all {
					fmt.Fprintln(out, "暂无容器（运行 boxli run <镜像> 创建）")
				} else {
					fmt.Fprintln(out, "暂无运行中的容器（boxli ps -a 查看全部）")
				}
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tIMAGE\tSTATUS\tCREATED\tRESTART")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					r.ID, r.Name, r.Image, r.Status, r.Created, r.Restart)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "包含已停止的容器")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "只输出容器 ID")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $BOXLI_HOME 或 ~/.boxli）")
	return cmd
}

// containerRow 是 `boxli ps` 的一行。
type containerRow struct {
	ID      string
	Name    string
	Image   string
	Status  string
	Created string
	Restart string
}

// collectContainerRows 汇总容器行。all=false 时只保留运行中（shim 存活）的容器。
// 没有运行状态记录的新容器按"已创建"呈现。
func collectContainerRows(st *store.Store, all bool) ([]containerRow, error) {
	list, err := st.ListContainers()
	if err != nil {
		return nil, err
	}
	var rows []containerRow
	for _, cfg := range list {
		state, ok, err := st.ReadRuntimeState(cfg.ID)
		if err != nil {
			return nil, err
		}
		running := ok && state.Running && boot.PidAlive(state.ShimPID)
		if !running && !all {
			continue
		}
		rows = append(rows, containerRow{
			ID:      cfg.ID,
			Name:    cfg.Name,
			Image:   cfg.ImageRef,
			Status:  psStatus(st, cfg.ID, state, ok, running),
			Created: formatCreated(cfg.CreatedAt),
			Restart: string(cfg.Restart),
		})
	}
	return rows, nil
}

// psStatus 渲染容器状态列：Up（运行中）/ Exited / Created。
func psStatus(st *store.Store, id string, state *store.RuntimeState, hasState, running bool) string {
	if running {
		up := "Up"
		if state.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339, state.StartedAt); err == nil {
				up += " " + humanDuration(time.Since(t))
			}
		}
		if state.RestartCount > 0 {
			up += fmt.Sprintf(" (restarted %d)", state.RestartCount)
		}
		return up
	}
	if !hasState {
		return "Created"
	}
	s := fmt.Sprintf("Exited (%d)", state.ExitCode)
	if state.RestartCount > 0 {
		s += fmt.Sprintf(" restarts=%d", state.RestartCount)
	}
	if st.IsStoppedByUser(id) {
		s += ", user-stopped"
	}
	return s
}

// humanDuration 渲染粗粒度时长（与 docker ps 的 Up 列风格一致）。
func humanDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
