// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package doctor

import (
	"os"
	"path/filepath"
	"runtime"
)

// 非 Linux 平台的诊断占位实现。
//
// 存在的意义有两条：一是保证全仓库在 linux/darwin/android 上都能编译与
// `go vet` 通过（AGENTS.md《平台后端：build tags 分文件》）；二是让
// macOS（vm_darwin）接入各自后端前，`licore doctor` 仍然输出**同一组检查
// ID**，只是等级为 StatusSkip 并说明"本平台暂不支持"，而不是直接报错或
// 缺失检查项。无 Root Android 官方不支持，不在本列表。

// defaultChecker 返回非 Linux 平台的占位检查器：ID 与 Linux 完全一致，
// 全部为 StatusSkip。
func defaultChecker() Checker { return stubChecker{} }

// hostKernel 非 Linux 平台取不到 Linux 内核版本，返回平台名。
func hostKernel() string { return runtime.GOOS }

// hostPlatform 返回 "GOOS/GOARCH"。
func hostPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// absPath 返回路径的绝对形式。
func absPath(p string) (string, error) { return filepath.Abs(p) }

// stubChecker 是非 Linux 平台的默认检查器。
type stubChecker struct{ version string }

// Name 实现 Checker。
func (stubChecker) Name() string { return "stub" }

// WithVersion 实现 versionedChecker：记录版本号供 binary.version 使用。
func (s stubChecker) WithVersion(version string) Checker {
	s.version = version
	return s
}

// Smoke 实现 smokeProvider：非 Linux 平台没有容器冒烟测试。
func (stubChecker) Smoke() SmokeFunc { return nil }

// Checks 实现 Checker：返回与 Linux 相同的检查 ID，全部标记为跳过。
func (s stubChecker) Checks() []Check {
	out := make([]Check, 0, len(checkOrder))
	for _, id := range checkOrder {
		out = append(out, s.stubCheck(id))
	}
	return out
}

// stubCheck 按检查 ID 给出该平台下的跳过说明。
func (s stubChecker) stubCheck(id string) Check {
	c := Check{
		ID:     id,
		Title:  titleOf(id),
		Status: StatusSkip,
		Detail: "本平台暂不支持（" + hostPlatform() + " 后端尚未接入）",
	}
	// 少数几个检查在任何平台都有意义，尽量给出真实信息。
	switch id {
	case CheckStorageDataDir:
		if dir := resolveDataDir(""); dir != "" {
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				c.Detail = dir + " 存在（可写性检查待 " + runtime.GOOS + " 后端接入）"
				return c
			}
			c.Detail = dir + " 尚未创建；" + runtime.GOOS + " 后端的完整检查待接入"
		}
	case CheckArchHost:
		c.Detail = hostPlatform() + "，平台后端 " + platformBackend()
		c.Hint = "macOS 走 vm_darwin（轻量虚拟机），待其阶段落地；无 Root Android 官方不支持"
	case CheckBinaryVersion:
		if exe, err := os.Executable(); err == nil {
			c.Detail = "可执行文件 " + exe
			if s.version != "" {
				c.Detail += "；版本 " + s.version
			} else {
				c.Detail += "；版本未知（未注入）"
				c.Hint = "从发布包运行，或用 `-ldflags -X main.version=<v>` 重新编译"
			}
		}
	}
	return c
}

// platformBackend 返回当前平台对应的运行时后端名（AGENTS.md 表格）。
func platformBackend() string {
	switch runtime.GOOS {
	case "darwin":
		return "vm_darwin"
	case "android":
		return "native_linux（需 Root；无 Root 官方不支持）"
	case "linux":
		return "native_linux"
	}
	return "未登记"
}
