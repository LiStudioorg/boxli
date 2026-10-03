// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package dev

import "errors"

// 本文件集中放置 internal/dev 的哨兵错误。调用方用 errors.Is 判定，
// 包装一律用 fmt.Errorf("...: %w", err)。
var (
	// ErrNoRoots 表示 WatchSpec.Roots 为空：没有监视目标，无法工作。
	ErrNoRoots = errors.New("licore/dev: no watch roots configured")

	// ErrBadIgnore 表示某个忽略模式语法非法（例如 path.Match 无法解析的 glob）。
	ErrBadIgnore = errors.New("licore/dev: invalid ignore pattern")
)
