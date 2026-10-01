// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package scaffold

import "errors"

// 脚手架与静态检查的哨兵错误。调用方用 errors.Is 判定，禁止匹配错误字符串。
var (
	// ErrFileExists 表示目标文件已存在且未开启覆盖。
	ErrFileExists = errors.New("boxli/scaffold: 目标文件已存在")

	// ErrNoProject 表示目录内既没有 boxli-compose.yml 也没有 boxli-compose.yaml。
	ErrNoProject = errors.New("boxli/scaffold: 目录内未找到 boxli-compose.yml")
)
