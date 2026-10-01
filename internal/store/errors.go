// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "errors"

// ErrExists 表示目标目录下已存在同名镜像且未指定 --force。
var ErrExists = errors.New("boxli/store: 镜像已存在")
