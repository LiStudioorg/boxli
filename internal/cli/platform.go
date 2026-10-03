// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "runtime"

// androidInfo 是平台探测结果的最小投影，供平台串渲染使用。
//
// 特意只保留两个字段：公共逻辑不需要 Android 版本/型号等细节，字段越少
// 跨平台桩（platform_darwin.go）越不容易漂移。
type androidInfo struct {
	// IsAndroid 表示当前是否运行在 Android 上。
	IsAndroid bool
	// Root 表示当前是否以 root（euid 0）运行，仅在 Android 上有意义。
	Root bool
}

// platformSuffix 生成 --version 的平台标识后缀，形如：
//
//	(linux/amd64)
//	(android/arm64, root)
//	(android/arm64, non-root)
//	(darwin/arm64)
//
// 平台一律**运行时探测**，不依赖 ldflags：Android 身份经 detectAndroidEnv
// 判定，其 Linux 实现复用 doctor.DetectAndroidEnv，因此与 `licore doctor`
// 的结论同源，不会出现两处不一致。
//
// 关键约定：只有真的在 Android 上才显示 "android" 字样——探测失败或非
// Android 时回落为真实 GOOS/GOARCH，绝不把 linux/darwin 说成 android。
func platformSuffix() string {
	info, err := detectAndroidEnv()
	if err != nil || !info.IsAndroid {
		// 探测不可用不是错误：退回编译期平台，保证输出永远确定。
		return "(" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	}

	suffix := "(android/" + runtime.GOARCH
	if info.Root {
		suffix += ", root"
	} else {
		suffix += ", non-root"
	}
	return suffix + ")"
}

// versionString 把版本号与平台标识组合成 --version 的完整输出值。
//
// 形如 "0.7.0 (linux/amd64)"、"0.7.0 (android/arm64, root)"。
// version 为空时用 "dev" 占位，避免输出 "(linux/amd64)" 这种缺版本的怪形态。
func versionString(version string) string {
	if version == "" {
		version = "dev"
	}
	return version + " " + platformSuffix()
}
