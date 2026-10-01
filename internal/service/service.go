// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package service 管理系统级开机自启服务（AGENTS.md《开机自启动机制》）。
//
// Boxli 只注册一个全局一次性服务：Linux 上为 systemd unit
// boxli.service（Type=oneshot，ExecStart=boxli boot，ExecStop=boxli
// shutdown）。本包只做"生成文件 + 调用 systemctl 注册"两件事，自身不
// 常驻；systemctl 通过 Runner 注入以便测试。macOS/Android 后端随各自
// 平台阶段补充（launchd / Magisk service.d / Termux:Boot）。
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// UnitName 是 systemd 服务名。
const UnitName = "boxli.service"

// DefaultUnitDir 是 systemd 系统级 unit 目录。
const DefaultUnitDir = "/etc/systemd/system"

// RuntimeDir 存在表示本机 PID 1 是 systemd。
const RuntimeDir = "/run/systemd/system"

// 哨兵错误。
var (
	// ErrNotPermitted 表示写系统目录或调用 systemctl 权限不足。
	ErrNotPermitted = errors.New("boxli/service: 权限不足")
	// ErrNoSystemd 表示本机没有 systemd 运行时。
	ErrNoSystemd = errors.New("boxli/service: 本机未检测到 systemd")
)

// Runner 抽象 systemctl 调用；测试注入 fake。
type Runner interface {
	// Output 执行命令并返回 stdout+stderr。
	Output(ctx context.Context, name string, args ...string) (string, error)
}

// execRunner 是真实实现。
type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w（%s）", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Options 配置服务管理器的行为，零值可用（Linux 默认路径）。
type Options struct {
	// UnitDir 是 unit 文件目录（默认 /etc/systemd/system）。
	UnitDir string
	// Runner 是 systemctl 执行器（默认 execRunner）。
	Runner Runner
	// ExecPath 写入 ExecStart/ExecStop 的 boxli 绝对路径（默认 os.Executable）。
	ExecPath string
	// DataDir 非空时以 --data-dir 参数固化到 ExecStart/ExecStop，
	// 避免 systemd 环境（HOME=/root）与用户数据目录不一致。
	DataDir string
}

func (o *Options) unitDir() string {
	if o.UnitDir != "" {
		return o.UnitDir
	}
	return DefaultUnitDir
}

// UnitPath 返回 unit 文件完整路径。
func (o *Options) UnitPath() string { return filepath.Join(o.unitDir(), UnitName) }

func (o *Options) runner() Runner {
	if o.Runner != nil {
		return o.Runner
	}
	return execRunner{}
}

func (o *Options) execPath() (string, error) {
	if o.ExecPath != "" {
		return o.ExecPath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位 boxli 可执行文件失败: %w", err)
	}
	return exe, nil
}

// UnitContent 按 AGENTS.md 模板生成 systemd unit 内容。
func (o *Options) UnitContent() (string, error) {
	exe, err := o.execPath()
	if err != nil {
		return "", err
	}
	arg := ""
	if o.DataDir != "" {
		arg = " --data-dir " + o.DataDir
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Boxli container engine\n")
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=oneshot\n")
	b.WriteString("RemainAfterExit=yes\n")
	b.WriteString("ExecStart=" + exe + " boot" + arg + "\n")
	b.WriteString("ExecStop=" + exe + " shutdown" + arg + "\n\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String(), nil
}

// detectsSystemd 报告本机是否有 systemd 运行时；测试中可替换。
var detectsSystemd = func() bool {
	fi, err := os.Stat(RuntimeDir)
	return err == nil && fi.IsDir()
}

// EnableResult 是 Enable 的结果摘要。
type EnableResult struct {
	UnitPath string
	// Systemd 报告本次是否完成了 systemctl 注册（false=仅生成文件）。
	Systemd bool
	// Note 是给用户的补充说明（未注册原因等）。
	Note string
}

// Enable 写入 unit 文件并注册开机自启。无 systemd 时仍生成文件并在 Note
// 说明；权限不足返回包装 ErrNotPermitted 的错误。
func Enable(ctx context.Context, o *Options) (*EnableResult, error) {
	content, err := o.UnitContent()
	if err != nil {
		return nil, err
	}
	unitPath := o.UnitPath()
	if err := os.MkdirAll(o.unitDir(), 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, permissionHint(unitPath, err)
		}
		return nil, fmt.Errorf("创建 unit 目录失败: %w", err)
	}
	tmp := unitPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, permissionHint(unitPath, err)
		}
		return nil, fmt.Errorf("写 unit 文件失败: %w", err)
	}
	if err := os.Rename(tmp, unitPath); err != nil {
		return nil, fmt.Errorf("落位 unit 文件失败: %w", err)
	}

	res := &EnableResult{UnitPath: unitPath}
	if !detectsSystemd() {
		res.Note = "未检测到 systemd 运行时：已生成服务文件，但未注册；在真机上重新执行 boxli boot enable 即可"
		return res, nil
	}
	r := o.runner()
	steps := []struct {
		desc string
		args []string
	}{
		{"daemon-reload", []string{"daemon-reload"}},
		{"enable " + UnitName, []string{"enable", UnitName}},
	}
	for _, s := range steps {
		if _, err := r.Output(ctx, "systemctl", s.args...); err != nil {
			if isPermissionErr(err) {
				return res, fmt.Errorf("systemctl %s 权限不足: %w；请执行：sudo boxli boot enable", s.desc, ErrNotPermitted)
			}
			return res, fmt.Errorf("systemctl %s 失败: %w", s.desc, err)
		}
	}
	res.Systemd = true
	return res, nil
}

// Disable 取消注册并删除 unit 文件。文件不存在时视为已关闭（幂等）。
func Disable(ctx context.Context, o *Options) error {
	unitPath := o.UnitPath()
	if detectsSystemd() {
		r := o.runner()
		// disable 对未注册服务返回非零不构成错误语义冲突：失败仅记录。
		if _, err := r.Output(ctx, "systemctl", "disable", "--now", UnitName); err != nil && !isPermissionErr(err) {
			// 继续尝试删文件，由后续错误兜底。
			_ = err
		} else if isPermissionErr(err) {
			return fmt.Errorf("systemctl disable 权限不足: %w；请执行：sudo boxli boot disable", ErrNotPermitted)
		}
	}
	if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("删除 %s 需要 root 权限: %w；请执行：sudo boxli boot disable", unitPath, ErrNotPermitted)
		}
		return fmt.Errorf("删除 unit 文件失败: %w", err)
	}
	if detectsSystemd() {
		_, _ = o.runner().Output(ctx, "systemctl", "daemon-reload")
	}
	return nil
}

// StatusResult 描述开机自启服务的当前状态。
type StatusResult struct {
	// Kind 是服务类型（systemd / none）。
	Kind string
	// UnitPath 是服务文件路径（无论是否存在）。
	UnitPath string
	// FileExists 报告 unit 文件是否已生成。
	FileExists bool
	// Enabled 报告服务是否已注册开机自启（systemctl is-enabled = enabled）。
	Enabled bool
	// UnitState 是 systemctl 报告的状态原文（enabled/disabled/static/...）。
	UnitState string
	// Note 是补充说明（如"无 systemd"）。
	Note string
}

// Status 查询服务状态。systemctl 不可用或未注册时相应字段为 false/空。
func Status(ctx context.Context, o *Options) (*StatusResult, error) {
	res := &StatusResult{Kind: "none", UnitPath: o.UnitPath()}
	if _, err := os.Stat(res.UnitPath); err == nil {
		res.FileExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("检查 unit 文件失败: %w", err)
	}
	if !detectsSystemd() {
		res.Note = "本机无 systemd 运行时"
		return res, nil
	}
	res.Kind = "systemd"
	out, err := o.runner().Output(ctx, "systemctl", "is-enabled", UnitName)
	if err != nil {
		// is-enabled 对未注册服务返回非零；输出可能含英文长错误，统一折叠。
		res.UnitState = foldUnitState(out)
		return res, nil
	}
	res.UnitState = strings.TrimSpace(out)
	res.Enabled = res.UnitState == "enabled"
	if st, err := o.runner().Output(ctx, "systemctl", "is-active", UnitName); err == nil {
		res.UnitState += " / " + strings.TrimSpace(st)
	}
	return res, nil
}

// foldUnitState 从 systemctl is-enabled 的输出提炼状态词；无法识别或
// 含"不存在"语义时统一为 not-found。
func foldUnitState(out string) string {
	s := strings.ToLower(strings.TrimSpace(out))
	for _, w := range []string{"enabled", "disabled", "static", "masked", "indirect", "alias", "generated", "bad"} {
		if strings.HasPrefix(s, w) || strings.Contains(s, "\n"+w) {
			return w
		}
	}
	if strings.Contains(s, "no such file") || strings.Contains(s, "not-found") || s == "" {
		return "not-found"
	}
	return "unknown"
}

// isPermissionErr 从 systemctl 错误里识别权限类失败（polkit/非 root 场景
// 文案大小写与措辞不完全稳定，统一小写匹配）。
func isPermissionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "access denied") ||
		errors.Is(err, os.ErrPermission)
}

func permissionHint(unitPath string, cause error) error {
	return fmt.Errorf("写入 %s 需要 root 权限: %w；请执行：sudo boxli boot enable", unitPath, errors.Join(ErrNotPermitted, cause))
}
