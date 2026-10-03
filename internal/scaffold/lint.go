// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package scaffold

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Severity 是诊断的严重级别。
type Severity string

const (
	// SeverityError 表示必须修复的问题：配置无法被正确解析或必然导致运行失败。
	SeverityError Severity = "error"
	// SeverityWarning 表示可疑但可运行的问题：可能是笔误或遗漏。
	SeverityWarning Severity = "warning"
)

// 稳定的规则 ID。调用方（CLI、编辑器、CI）依赖这些字符串做过滤与抑制，
// 因此只允许新增，不允许改写既有取值。
const (
	// 编排文件（licore-compose.yml）规则。
	RuleComposeParse          = "compose/parse"
	RuleComposeValidate       = "compose/validate"
	RuleComposeUnknownKey     = "compose/unknown-key"
	RuleComposeServiceUnknown = "compose/service-unknown-key"
	RuleComposeRestartInvalid = "compose/restart-invalid"
	RuleComposeMissingTarget  = "compose/missing-target"
	RuleComposeDependsUnknown = "compose/depends-on-unknown"
	RuleComposePortFormat     = "compose/port-format"
	RuleComposeVolumeFormat   = "compose/volume-format"
	RuleComposeDevRebuild     = "compose/dev-rebuild-unknown"

	// 构建描述（Boxfile）规则。
	RuleBoxfileParse           = "boxfile/parse"
	RuleBoxfileFromRequired    = "boxfile/from-required"
	RuleBoxfileUnknownInstruct = "boxfile/unknown-instruction"
	RuleBoxfileRunUnsupported  = "boxfile/run-unsupported"
	RuleBoxfileEntrypointShell = "boxfile/entrypoint-shell-form"
	RuleBoxfileWorkdirRelative = "boxfile/workdir-relative"
	RuleBoxfileArgUnused       = "boxfile/arg-unused"
)

// 解析器（licore-compose.yml / Boxfile）的静态已知键表。lint 不复用内部解析器：
// 解析器在首个错误处即返回，而"一次报全所有问题"要求自主扫描。两张表必须与
// internal/compose、internal/build 的解析器同步维护，新增键时两边一起改。
var (
	composeTopLevelKeys = []string{
		"version", "name", "services", "networks", "volumes",
	}
	composeServiceKeys = []string{
		"image", "boxfile", "build", "command", "entrypoint", "environment",
		"env", "ports", "volumes", "depends-on", "depends_on", "dependsOn",
		"restart", "hostname", "working-dir", "working_dir", "workingDir",
		"user", "dev", "replicas", "scale", "labels",
	}
	composeDevKeys = []string{"watch", "ignore", "rebuild"}
	boxfileOps     = []string{
		"FROM", "COPY", "ADD", "ENV", "WORKDIR", "ENTRYPOINT", "CMD", "RUN",
		"ARG", "LABEL", "EXPOSE", "USER", "VOLUME", "SHELL", "HEALTHCHECK",
		"STOPSIGNAL", "ONBUILD", "MAINTAINER",
	}
	composeRestarts = []string{"no", "always", "unless-stopped", "on-failure"}
)

// Diagnostic 是一条 lint 诊断。
type Diagnostic struct {
	// File 是诊断所属文件路径，与调用 lint 时传入的路径一致。
	File string
	// Line 是 1 起算的行号；无具体行（例如文件级问题）时为 0。
	Line int
	// Severity 是严重级别。
	Severity Severity
	// Rule 是稳定的规则 ID，见本包 Rule* 常量。
	Rule string
	// Message 是人类可读的中文说明。
	Message string
}

// String 以 `path:line: severity: rule: message` 渲染一条诊断，与 LintReport 同形。
func (d Diagnostic) String() string {
	return fmt.Sprintf("%s:%d: %s: %s: %s", d.File, d.Line, d.Severity, d.Rule, d.Message)
}

// Result 是一次 lint 的完整结果。
type Result struct {
	// Diagnostics 是全部诊断，按文件、行号稳定排序。
	Diagnostics []Diagnostic
}

// HasErrors 报告结果中是否存在 error 级诊断。退出码由调用方据此决定。
func (r *Result) HasErrors() bool {
	if r == nil {
		return false
	}
	for _, d := range r.Diagnostics {
		if d.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Error 实现 error，便于 lint 结果直接参与 CLI 的错误路径；
// 无 error 级诊断时返回空串（表示通过）。
func (r *Result) Error() string {
	if r == nil || !r.HasErrors() {
		return ""
	}
	var b strings.Builder
	for i, d := range r.Diagnostics {
		if d.Severity != SeverityError {
			continue
		}
		if i > 0 && b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(d.String())
	}
	return b.String()
}

// add 追加一条诊断。
func (r *Result) add(file string, line int, sev Severity, rule, format string, args ...any) {
	r.Diagnostics = append(r.Diagnostics, Diagnostic{
		File:     file,
		Line:     line,
		Severity: sev,
		Rule:     rule,
		Message:  fmt.Sprintf(format, args...),
	})
}

// merge 合并一次子扫描的结果（lint 必须尽量报全，不能被第一处问题中断）。
func (r *Result) merge(other *Result) {
	if other == nil {
		return
	}
	r.Diagnostics = append(r.Diagnostics, other.Diagnostics...)
}

// mergeErr 处理"路径存在但读不进来"的情况：把读取失败降级为一条诊断，
// 而不是整体失败，其余文件仍会被检查。
func (r *Result) mergeErr(file string, err error) {
	if err == nil {
		return
	}
	r.add(file, 0, SeverityError, RuleComposeParse, "读取文件失败: %v", err)
}

// sortDiagnostics 按文件、行号、规则稳定排序，保证输出可复现。
func (r *Result) sortDiagnostics() {
	sort.SliceStable(r.Diagnostics, func(i, j int) bool {
		a, b := r.Diagnostics[i], r.Diagnostics[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})
}

// LintComposeFile 检查单个 licore-compose.yml 文件。路径不存在或不可读时返回错误
// （显式指定的路径必须存在）；文件内容有问题时只产出 Diagnostics，不返回错误。
func LintComposeFile(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取编排文件 %s: %w", path, err)
	}
	res := &Result{}
	res.merge(lintCompose(path, data))
	res.sortDiagnostics()
	return res, nil
}

// LintBoxfile 检查单个 Boxfile。路径不存在或不可读时返回错误；
// 内容问题以 Diagnostics 形式返回，不中断后续规则。
func LintBoxfile(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Boxfile %s: %w", path, err)
	}
	res := &Result{}
	res.merge(lintBoxfile(path, data))
	res.sortDiagnostics()
	return res, nil
}

// LintProject 检查 dir 下的 licore-compose.yml（其次 licore-compose.yaml）与 Boxfile。
//
// 两个文件都不存在时返回包装了 ErrNoProject 的错误；只缺其中一个不算错误，
// 缺失的文件被静默跳过（缺 Boxfile 时跳过 compose 的 compose/missing-target，
// 因为目标可能来自尚未生成的文件）。读取失败降级为诊断而非返回错误。
func LintProject(dir string) (*Result, error) {
	if strings.TrimSpace(dir) == "" {
		dir = "."
	}
	composePath, composeFound, err := findComposeFile(dir)
	if err != nil {
		return nil, err
	}
	boxfilePath := filepath.Join(dir, BoxfileName)
	boxfileFound := fileExists(boxfilePath)
	if !composeFound && !boxfileFound {
		return nil, fmt.Errorf("目录 %s 中未找到 %s 或 %s: %w",
			dir, ComposeFileName, BoxfileName, ErrNoProject)
	}

	res := &Result{}
	if composeFound {
		data, readErr := os.ReadFile(composePath)
		if readErr != nil {
			res.mergeErr(composePath, readErr)
		} else {
			res.merge(lintComposeWithOptions(composePath, data, !boxfileFound))
		}
	}
	if boxfileFound {
		data, readErr := os.ReadFile(boxfilePath)
		if readErr != nil {
			res.mergeErr(boxfilePath, readErr)
		} else {
			res.merge(lintBoxfile(boxfilePath, data))
		}
	}
	res.sortDiagnostics()
	slog.Debug("lint 完成", "dir", dir, "diagnostics", len(res.Diagnostics),
		"compose", composeFound, "boxfile", boxfileFound)
	return res, nil
}

// LintReport 按 `path:line: severity: rule: message` 逐行输出全部诊断，
// 行序为"文件 → 行号 → 规则"（与 Result.sortDiagnostics 一致），
// 并返回 error 级与 warning 级诊断的条数。仅当写入 w 失败时返回错误。
func LintReport(w io.Writer, results ...*Result) (errorsN, warningsN int, err error) {
	all := &Result{}
	for _, r := range results {
		if r == nil {
			continue
		}
		all.Diagnostics = append(all.Diagnostics, r.Diagnostics...)
	}
	all.sortDiagnostics()
	for _, d := range all.Diagnostics {
		switch d.Severity {
		case SeverityError:
			errorsN++
		case SeverityWarning:
			warningsN++
		}
		if _, werr := fmt.Fprintln(w, d.String()); werr != nil {
			return errorsN, warningsN, fmt.Errorf("写 lint 报告: %w", werr)
		}
	}
	return errorsN, warningsN, nil
}

// findComposeFile 在 dir 下按 .yml → .yaml 顺序定位编排文件。
func findComposeFile(dir string) (path string, found bool, err error) {
	for _, name := range []string{ComposeFileName, ComposeFileNameAlt} {
		p := filepath.Join(dir, name)
		info, statErr := os.Stat(p)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return "", false, fmt.Errorf("%s 是目录而非文件", p)
			}
			return p, true, nil
		case errors.Is(statErr, fs.ErrNotExist):
		default:
			return "", false, fmt.Errorf("检查 %s: %w", p, statErr)
		}
	}
	return "", false, nil
}

// fileExists 报告路径是否为已存在的普通文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
