// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import "errors"

// 哨兵错误，调用方用 errors.Is 判定。
var (
	// ErrUnsupported 表示能力依赖的宿主/驱动未就绪（如 S3 驱动）。
	ErrUnsupported = errors.New("licore/hub: 该能力暂不支持")
	// ErrBadRequest 表示请求格式非法。
	ErrBadRequest = errors.New("licore/hub: 请求非法")
	// ErrNotFound 表示资源不存在（tag / blob / 镜像）。
	ErrNotFound = errors.New("licore/hub: 资源不存在")
	// ErrExists 表示资源已存在且不允许覆盖。
	ErrExists = errors.New("licore/hub: 资源已存在")
	// ErrUnauthorized 表示鉴权失败或令牌无效/过期。
	ErrUnauthorized = errors.New("licore/hub: 未授权")
	// ErrDigestMismatch 表示上传 blob 内容摘要与声明不一致。
	ErrDigestMismatch = errors.New("licore/hub: blob 摘要不符")
	// ErrBadDigest 表示摘要格式非法。
	ErrBadDigest = errors.New("licore/hub: 摘要格式非法")
	// ErrRefcountBusy 表示 blob 被引用，不能删除。
	ErrRefcountBusy = errors.New("licore/hub: blob 仍被引用")
)
