// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import "strings"

// envCgroupID 是从父进程（engine/shim）传给 runtime.StartWith 的容器 ID
// 标记：存在即表示该容器已建 cgroup，fork 出 init 后需将其 PID 写入所在组。
// envWithoutLiCore 统一剥离 LICORE_* 前缀，故不会泄漏进用户命令行。
const envCgroupID = "LICORE_CGROUP_ID"

// CgroupEnv 生成标注容器所属 cgroup 的环境变量（追加到 runtime.Config.Env）。
// 若 limits 为空或未启用 cgroup，则由调用方决定是否附带；StartWith 读到
// 才尝试写 PID。
func CgroupEnv(containerID string) []string {
	return []string{envCgroupID + "=" + containerID}
}

// cgroupIDFromEnv 从环境变量切片读取容器 cgroup ID；不存在返回 ""。
func cgroupIDFromEnv(env []string) string {
	prefix := envCgroupID + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}
