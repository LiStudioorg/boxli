// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package hub 实现 Boxli 的分发服务端：内容寻址的 blob 存储 + 全局去重 +
// 引用计数，配以 JWT 鉴权的 HTTP API（auth/login、tags、blobs、search），
// 以及供客户端（boxli login/pull/push/search 的底层）使用的 Client。
//
// 存储层通过 BlobStore 驱动抽象：local 驱动把 blob 落到本地目录
// （<root>/blobs/sha256/<hex>），S3 驱动预留接口（返回 ErrUnsupported）。
// 纯标准库实现：net/http、crypto（HMAC-SHA256 自研 JWT）、无 CGO、无新增依赖。
package hub
