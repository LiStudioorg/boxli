// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/LiStudioorg/licore/internal/boot"
	"github.com/LiStudioorg/licore/internal/service"
	"github.com/LiStudioorg/licore/internal/store"
)

// bootCmdOpts 汇总 boot 命令族共享的 flags。
type bootCmdOpts struct {
	dataDir string
}

// newBootTestCommand 实现 `licore boot` 及子命令 enable/disable/status。
// 设计见 AGENTS.md《开机自启动机制》：boot 是一次性命令，非常驻。
func newBootTestCommand(out io.Writer) *cobra.Command {
	o := &bootCmdOpts{}
	cmd := &cobra.Command{
		Use:   "boot",
		Short: "开机时由系统服务调用，一次性拉起自启容器",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(o.dataDir)
			if err != nil {
				return err
			}
			res, err := boot.StartAll(st, nil)
			for _, c := range res.Started {
				fmt.Fprintf(out, "已拉起 %s（%s，restart=%s）\n", c.Name, c.ID, c.Restart)
			}
			for _, sk := range res.Skipped {
				fmt.Fprintf(out, "跳过 %s：%s\n", sk.Cfg.Name, sk.Reason)
			}
			for _, f := range res.Failed {
				fmt.Fprintf(out, "启动失败 %s：%v\n", f.Cfg.Name, f.Err)
			}
			if err != nil && !errors.Is(err, boot.ErrPartial) {
				return err
			}
			fmt.Fprintf(out, "boot 完成：拉起 %d，跳过 %d，失败 %d\n",
				len(res.Started), len(res.Skipped), len(res.Failed))
			// 部分失败不算致命：boot 由 systemd 调用，返回 0 避免服务进入 failed 态。
			return nil
		},
	}
	cmd.Flags().StringVar(&o.dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")

	enable := &cobra.Command{
		Use:   "enable",
		Short: "启用开机自启（自动检测平台并写入系统服务）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(o.dataDir)
			if err != nil {
				return err
			}
			so := serviceOptions(st)
			res, err := service.Enable(context.Background(), so)
			if err != nil {
				return err
			}
			if res != nil {
				fmt.Fprintf(out, "服务文件：%s\n", res.UnitPath)
				if res.Systemd {
					fmt.Fprintf(out, "已注册 systemd 开机自启（systemctl enable %s）\n", service.UnitName)
				}
				if res.Note != "" {
					fmt.Fprintf(out, "提示：%s\n", res.Note)
				}
			}
			return nil
		},
	}

	disable := &cobra.Command{
		Use:   "disable",
		Short: "关闭开机自启（移除系统服务文件并取消注册）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(o.dataDir)
			if err != nil {
				return err
			}
			so := serviceOptions(st)
			if err := service.Disable(context.Background(), so); err != nil {
				return err
			}
			fmt.Fprintf(out, "已关闭开机自启（移除 %s）\n", so.UnitPath())
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "查看开机自启状态与自启容器列表",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(o.dataDir)
			if err != nil {
				return err
			}
			so := serviceOptions(st)
			svc, err := service.Status(context.Background(), so)
			if err != nil {
				return err
			}
			state := "未启用"
			switch {
			case svc.Enabled:
				state = "已启用"
			case svc.FileExists:
				state = "未启用（服务文件已生成但未注册）"
			}
			fmt.Fprintf(out, "开机自启：%s\n", state)
			fmt.Fprintf(out, "服务类型：%s\n", serviceKindName(svc.Kind))
			fmt.Fprintf(out, "服务文件：%s\n", svc.UnitPath)
			if svc.UnitState != "" {
				fmt.Fprintf(out, "systemd 状态：%s\n", svc.UnitState)
			}
			if svc.Note != "" {
				fmt.Fprintf(out, "提示：%s\n", svc.Note)
			}

			// 自启容器列表（always / unless-stopped）。
			all, err := st.ListContainers()
			if err != nil {
				return err
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, "自启容器：")
			any := false
			for _, c := range all {
				if !c.Restart.BootEligible() {
					continue
				}
				any = true
				fmt.Fprintf(out, "  %-16s %-16s restart=%-15s %s\n", c.Name, c.ID, c.Restart, containerStateText(st, c.ID))
			}
			if !any {
				fmt.Fprintln(out, "  （无：licore run --restart always|unless-stopped 创建自启容器）")
			}
			return nil
		},
	}

	for _, sub := range []*cobra.Command{enable, disable, status} {
		sub.Flags().StringVar(&o.dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	}
	cmd.AddCommand(enable, disable, status)
	return cmd
}

// newShutdownCommand 实现 `licore shutdown`：由系统服务 ExecStop 调用，
// 优雅停止所有在运行的容器（SIGTERM shim → shim 转发容器 init）。
func newShutdownCommand(out io.Writer) *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "shutdown",
		Short: "停止所有自启容器（由系统服务 ExecStop 调用）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.Open(dataDir)
			if err != nil {
				return err
			}
			stopped, idle, timeout, err := boot.StopAll(st, 30*time.Second)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "shutdown 完成：停止 %d，未在运行 %d，超时 %d\n", stopped, idle, timeout)
			if timeout > 0 {
				// 超时容器多半卡在 init 收尾；返回 0 让 systemd 继续关机流程。
				fmt.Fprintln(out, "警告：部分容器未在时限内退出，可能仍持有挂载")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "数据目录（默认 $LICORE_HOME 或 ~/.licore）")
	return cmd
}

// serviceOptions 组装服务管理器参数：数据目录固化为绝对路径写入 unit，
// 避免 systemd 环境（HOME=/root）与用户数据目录错位。
func serviceOptions(st *store.Store) *service.Options {
	return &service.Options{DataDir: st.Root}
}

func serviceKindName(kind string) string {
	switch kind {
	case "systemd":
		return "systemd（" + service.UnitName + "）"
	default:
		return "无（本机未检测到 systemd）"
	}
}

// containerStateText 渲染容器的运行状态描述。
func containerStateText(st *store.Store, id string) string {
	stt, ok, err := st.ReadRuntimeState(id)
	if err != nil {
		return "状态未知（" + err.Error() + "）"
	}
	if !ok {
		return "未运行过"
	}
	if stt.Running && boot.PidAlive(stt.ShimPID) {
		return "运行中（init PID " + fmt.Sprint(stt.InitPID) + "）"
	}
	if stt.Running {
		return "已中断（shim 丢失，boot 会重新拉起）"
	}
	text := fmt.Sprintf("已停止（退出码 %d", stt.ExitCode)
	if stt.RestartCount > 0 {
		text += fmt.Sprintf("，累计重启 %d 次", stt.RestartCount)
	}
	if st.IsStoppedByUser(id) {
		text += "，用户停止"
	}
	return text + "）"
}
