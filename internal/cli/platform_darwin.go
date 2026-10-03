// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build darwin

package cli

// detectAndroidEnv 是 Android 探测入口的测试缝（darwin 桩）。
//
// macOS 不存在 Android 身份，恒返回"非 Android"，因此 --version 永远显示
// 真实 GOOS/GOARCH（darwin/arm64 等）。保留同名函数是为了让 platform.go 的
// 公共逻辑不出现任何 runtime.GOOS 分支——按 AGENTS.md，平台差异一律靠
// build tag 分文件承载。
func detectAndroidEnv() (androidInfo, error) {
	return androidInfo{}, nil
}
