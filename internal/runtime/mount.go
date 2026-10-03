// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"fmt"
	"strconv"

	"github.com/LiStudioorg/licore/internal/store"
)

// 卷挂载通过内部环境变量从父进程传给容器 init，由 RunInit 在 pivot_root
// 之前把源 bind/tmpfs 挂到 rootfs 内的目标路径。envWithoutBoxli 统一剥离
// BOXLI_* 前缀，故这些键不会泄漏进用户命令行。
const (
	envMountCount = "BOXLI_MOUNT_COUNT"  // 挂载条数
	envMountSrcF  = "BOXLI_MOUNT_SRC_%d" // 源绝对路径
	envMountDstF  = "BOXLI_MOUNT_DST_%d" // 容器内目标
	envMountROF   = "BOXLI_MOUNT_RO_%d"  // 1=只读
)

// MountEnv 把已解析的挂载列表编码为一组环境变量（追加到 runtime.Config.Env）。
func MountEnv(mounts []store.Mount) []string {
	if len(mounts) == 0 {
		return nil
	}
	out := []string{envMountCount + "=" + strconv.Itoa(len(mounts))}
	for i, m := range mounts {
		ro := "0"
		if m.ReadOnly {
			ro = "1"
		}
		out = append(out,
			fmt.Sprintf(envMountSrcF, i)+"="+m.Source,
			fmt.Sprintf(envMountDstF, i)+"="+m.Target,
			fmt.Sprintf(envMountROF, i)+"="+ro,
		)
	}
	return out
}

// parseMountEnv 从环境变量切片解析挂载列表；无 COUNT 时返回 nil。
func parseMountEnv(env []string) ([]store.Mount, bool) {
	get := func(key string) (string, bool) {
		prefix := key + "="
		for _, e := range env {
			if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
				return e[len(prefix):], true
			}
		}
		return "", false
	}
	cs, ok := get(envMountCount)
	if !ok {
		return nil, false
	}
	n, err := strconv.Atoi(cs)
	if err != nil || n <= 0 {
		return nil, false
	}
	mounts := make([]store.Mount, 0, n)
	for i := 0; i < n; i++ {
		src, ok1 := get(fmt.Sprintf(envMountSrcF, i))
		dst, ok2 := get(fmt.Sprintf(envMountDstF, i))
		if !ok1 || !ok2 {
			break
		}
		ro, _ := get(fmt.Sprintf(envMountROF, i))
		mounts = append(mounts, store.Mount{Source: src, Target: dst, ReadOnly: ro == "1"})
	}
	return mounts, len(mounts) > 0
}
