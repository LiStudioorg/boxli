// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package cli

import "github.com/LiStudioorg/licore/internal/doctor"

// detectAndroidEnv 是 Android 探测入口的测试缝（Linux/Android 实现）。
//
// 生产环境恒为 doctor.DetectAndroidEnv 的适配；测试通过替换它模拟 Android
// 与非 Android 平台，无需真机。约定：缝只在测试中改写，默认值与真实实现一致。
//
// 按 AGENTS.md《平台后端：build tags 分文件》，探测只能出现在带 linux tag
// 的文件里（doctor 的 AndroidEnv 本身也只在 linux tag 下编译），darwin 提供
// 同签名的桩，见 platform_darwin.go。
var detectAndroidEnv = func() (androidInfo, error) {
	env, err := doctor.DetectAndroidEnv()
	if err != nil {
		return androidInfo{}, err
	}
	if env == nil {
		return androidInfo{}, nil
	}
	return androidInfo{IsAndroid: env.IsAndroid, Root: env.Root}, nil
}
