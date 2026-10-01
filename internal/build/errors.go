// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package build

import "errors"

// Boxfile 解析的拒绝性错误。调用方用 errors.Is 判定，禁止靠错误字符串匹配。
var (
	// ErrBadBoxfile 表示 Boxfile 整体非法：文件无法读取、含空字节，或触发体积 / 行数上限。
	ErrBadBoxfile = errors.New("boxli/build: Boxfile 非法")

	// ErrNoFrom 表示 Boxfile 中没有任何 FROM 指令。
	ErrNoFrom = errors.New("boxli/build: Boxfile 缺少 FROM")

	// ErrFromNotFirst 表示 FROM 不是第一条指令，或出现了多次。
	ErrFromNotFirst = errors.New("boxli/build: FROM 必须唯一且位于首条")

	// ErrBadInstruction 表示某条指令非法：未知操作符、缺少参数、参数不满足该指令的形式要求。
	ErrBadInstruction = errors.New("boxli/build: 指令非法")

	// ErrNoContext 表示构建上下文缺失、不是目录或不可读。COPY 的源路径依赖构建上下文。
	ErrNoContext = errors.New("boxli/build: 构建上下文非法")
)
