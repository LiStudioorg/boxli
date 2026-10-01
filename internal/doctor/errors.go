// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package doctor

import "errors"

// 哨兵错误。doctor 的诊断过程本身几乎不会失败：单个检查缺失文件、权限
// 不足、命令不存在都必须降级成 StatusSkip / StatusWarn 的 Check，而不是
// 向上返回错误。这里的哨兵只描述"调用方用法错误"这一类问题。
var (
	// ErrNilReport 表示把 nil Report 交给了渲染函数。
	ErrNilReport = errors.New("boxli/doctor: Report 为空")
	// ErrNilWriter 表示渲染目标 io.Writer 为空。
	ErrNilWriter = errors.New("boxli/doctor: 输出目标为空")
)
