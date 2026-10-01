// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Boxli —— 自研生态的轻量级容器引擎。
//
// 阶段 0：仅打印版本信息，尚无任何容器运行时代码。
package main

import (
	"fmt"
	"runtime"
)

// version 为当前开发版本号，正式发布时通过 -ldflags 注入。
var version = "0.0.0-dev"

func main() {
	fmt.Printf("boxli %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
}
