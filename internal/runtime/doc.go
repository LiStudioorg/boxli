// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package runtime 实现 Boxli 的容器运行时，负责容器的创建、启动、停止与回收。
//
// 平台后端通过 build tags 分文件实现：native_linux / proot_android / vm_darwin。
// 阶段 0：占位文件，尚无实现。
package runtime
