// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Boxli —— 自研生态的轻量级容器引擎。
package main

import (
	"os"

	"github.com/LiStudioorg/licore/internal/cli"
)

// version 为当前版本号，正式发布时通过 -ldflags -X 注入。
var version = "0.0.0-dev"

func main() {
	cli.Version = version
	os.Exit(cli.Execute())
}
