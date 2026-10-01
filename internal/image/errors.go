// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package image

import "errors"

// 规范第 4 节定义的拒绝性错误。调用方用 errors.Is 判定，禁止靠错误字符串匹配。
var (
	// ErrBadManifest 表示 index.json 缺失、非法 JSON、mediaType / specVersion 不匹配，
	// 或必填字段缺失、取值非法。
	ErrBadManifest = errors.New("boxli/image: 清单非法")

	// ErrUnsafePath 表示归档条目包含绝对路径、".." 段或符号链接逃逸。
	ErrUnsafePath = errors.New("boxli/image: 不安全的归档路径")

	// ErrLayerMissing 表示 index 声明的层在归档中不存在。
	ErrLayerMissing = errors.New("boxli/image: 层缺失")

	// ErrConfigMissing 表示 index 声明的 config blob 在归档中不存在。
	ErrConfigMissing = errors.New("boxli/image: config blob 缺失")

	// ErrSizeMismatch 表示 index 声明的 sizeBytes 与 tar 头不符。
	ErrSizeMismatch = errors.New("boxli/image: 层大小不符")

	// ErrDigestMismatch 表示层内容摘要与 index 声明不符。
	ErrDigestMismatch = errors.New("boxli/image: 层摘要不符")

	// ErrBadApplyOrder 表示 applyOrder 不是从 1 开始的严格递增序列。
	ErrBadApplyOrder = errors.New("boxli/image: applyOrder 非法")

	// ErrArchMismatch 表示镜像的 os / architecture 与当前平台不匹配。
	ErrArchMismatch = errors.New("boxli/image: 平台不匹配")

	// ErrUnsafeLayer 表示层内含设备节点、指向外部的硬链接等不安全条目。
	ErrUnsafeLayer = errors.New("boxli/image: 不安全的层内容")

	// ErrIndexTooLarge 表示 index.json 超出体积上限。
	ErrIndexTooLarge = errors.New("boxli/image: 清单超出大小上限")
)
