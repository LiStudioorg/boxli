// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// RenderText 以人类可读的对齐文本渲染报告：每项检查一个块
// （状态 · ID · 标题 / 详情 / 修复建议），最后一行是统计汇总。
func RenderText(w io.Writer, r *Report) error {
	if w == nil {
		return ErrNilWriter
	}
	if r == nil {
		return ErrNilReport
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintf(tw, "平台\t%s\n内核\t%s\n数据目录\t%s\n",
		orDash(r.Platform), orDash(r.Kernel), orDash(r.DataDir)); err != nil {
		return fmt.Errorf("doctor: 写报告头失败: %w", err)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("doctor: 刷新报告头失败: %w", err)
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return fmt.Errorf("doctor: 写报告头失败: %w", err)
	}

	for _, c := range r.Checks {
		if _, err := fmt.Fprintf(w, "%s %s  %s\n", statusLabel(c.Status), c.ID, c.Title); err != nil {
			return fmt.Errorf("doctor: 写检查项 %s 失败: %w", c.ID, err)
		}
		if c.Detail != "" {
			if _, err := fmt.Fprintf(w, "    %s\n", c.Detail); err != nil {
				return fmt.Errorf("doctor: 写检查项 %s 详情失败: %w", c.ID, err)
			}
		}
		if c.Hint != "" {
			if _, err := fmt.Fprintf(w, "    建议: %s\n", c.Hint); err != nil {
				return fmt.Errorf("doctor: 写检查项 %s 建议失败: %w", c.ID, err)
			}
		}
	}

	ok, warn, fail, skip := r.Counts()
	if _, err := fmt.Fprintf(w, "\n汇总: %d 通过, %d 警告, %d 失败, %d 跳过\n", ok, warn, fail, skip); err != nil {
		return fmt.Errorf("doctor: 写汇总失败: %w", err)
	}
	if _, err := fmt.Fprintln(w, verdict(r)); err != nil {
		return fmt.Errorf("doctor: 写结论失败: %w", err)
	}
	return nil
}

// statusLabel 返回状态的中文标签，未知状态按跳过处理。
func statusLabel(s Status) string {
	switch s {
	case StatusOK:
		return "[OK]  "
	case StatusWarn:
		return "[警告]"
	case StatusFail:
		return "[失败]"
	case StatusSkip:
		return "[跳过]"
	}
	return "[未知]"
}

// verdict 返回整体结论行。
func verdict(r *Report) string {
	_, warn, fail, _ := r.Counts()
	switch {
	case fail > 0:
		return "结论: 环境不满足运行要求，请按上面的建议处理后重试"
	case warn > 0:
		return "结论: 环境可用，但存在需要注意的项（见上方警告）"
	default:
		return "结论: 环境自检通过"
	}
}

// orDash 把空串替换成占位符 "-"。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// jsonReport 是 RenderJSON 的序列化骨架，保证输出稳定且字段可预期。
type jsonReport struct {
	Kernel   string  `json:"kernel,omitempty"`
	Platform string  `json:"platform"`
	DataDir  string  `json:"dataDir"`
	Summary  summary `json:"summary"`
	Checks   []Check `json:"checks"`
}

// summary 是 JSON 输出里的统计块。
type summary struct {
	OK       int  `json:"ok"`
	Warn     int  `json:"warn"`
	Fail     int  `json:"fail"`
	Skip     int  `json:"skip"`
	ExitCode int  `json:"exitCode"`
	Failed   bool `json:"failed"`
}

// RenderJSON 以缩进 JSON 渲染报告（含 summary 统计块，便于脚本消费）。
func RenderJSON(w io.Writer, r *Report) error {
	if w == nil {
		return ErrNilWriter
	}
	if r == nil {
		return ErrNilReport
	}

	ok, warn, fail, skip := r.Counts()
	checks := r.Checks
	if checks == nil {
		checks = []Check{}
	}
	doc := jsonReport{
		Kernel:   r.Kernel,
		Platform: r.Platform,
		DataDir:  r.DataDir,
		Summary:  summary{OK: ok, Warn: warn, Fail: fail, Skip: skip, ExitCode: r.ExitCode(), Failed: r.HasFailure()},
		Checks:   checks,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("doctor: 序列化报告失败: %w", err)
	}
	return nil
}
