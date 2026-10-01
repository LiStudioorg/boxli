// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import "errors"

// 解包/合并阶段的哨兵错误；路径逃逸类错误复用 image.ErrUnsafePath，
// 调用方一律 errors.Is 判定。
var (
	// ErrCorruptLayer 表示层数据损坏：gzip 非法、tar 截断、类型冲突、
	// 硬链接前向引用缺失等无法安全恢复的输入。
	ErrCorruptLayer = errors.New("boxli/storage: 层数据损坏")

	// ErrDuplicateEntry 表示同一层内出现重复条目路径（语义不确定，直接拒绝）。
	ErrDuplicateEntry = errors.New("boxli/storage: 层内重复条目")

	// ErrBadDigest 表示声明摘要与层内容实算摘要不一致。
	ErrBadDigest = errors.New("boxli/storage: 层摘要不符")

	// ErrLayerMissingLocal 表示合并所需层尚未解包进本地存储。
	ErrLayerMissingLocal = errors.New("boxli/storage: 本地缺少已解包的层")
)
