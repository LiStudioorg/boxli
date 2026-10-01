// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package storage 实现 .boxli 层的解包与合并（docs/image-spec.md 第 2、5 节）。
//
// 两个阶段：
//
//  1. UnpackFile：把单个层（tar.gz）安全解压为内容寻址目录
//     layers/sha256/<hex>/fs。<hex> 是 tar.gz 原始字节的 SHA-256；同一层
//     在不同镜像间共享存储。解压是纯差异视图（diff view），白out 文件
//     原样保留，语义在合并阶段应用。
//  2. Merge：按 applyOrder 依次把若干层的 fs 目录叠加进目标 rootfs，
//     应用 whiteout（.wh.<name>）与 opaque 目录（.wh..wh..opq）删除语义，
//     后层覆盖前层。
//
// 安全基线：所有 tar 条目名过 image.SafeArchivePath；写路径的每个已存在
// 父组件必须是真实目录（禁止符号链接穿透）；设备节点与 FIFO 剥离；
// setuid/setgid 位剥离；同层重复条目拒绝。
// 纯 Go、无 CGO、无新第三方依赖。
package storage
