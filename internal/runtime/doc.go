// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package runtime 实现 LiCore 的容器运行时（native_linux 后端）。
//
// 进程模型：父进程（shim 角色）用 /proc/self/exe 重执行本二进制并带上
// CLONE_NEWPID|NEWNS|NEWUTS|NEWIPC（非 root 追加 CLONE_NEWUSER + uid/gid 映射）；
// 子进程以 `licore init` 参数醒来，完成 pivot_root、挂 /proc 后 exec 用户命令。
// 全程无 CGO、无 runc/containerd。macOS/Android 后端以各自文件实现同一接口。
package runtime
