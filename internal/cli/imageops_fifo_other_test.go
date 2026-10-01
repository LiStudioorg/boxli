// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux && !android

package cli

import "errors"

// makeFIFO 在非 linux/android 平台上不可用：这些平台要么没有 FIFO，
// 要么需要 CGO/第三方包，而 AGENTS.md 禁止 CGO 与第三方依赖。
// 返回错误让调用方 t.Skip，保证仓库可交叉编译且测试全绿。
func makeFIFO(path string) error {
	return errors.New("本平台不支持创建 FIFO: " + path)
}
