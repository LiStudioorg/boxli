// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package shim

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/LiStudioorg/boxli/internal/store"
)

// 重执行 shim 的环境变量：main 分流标记 + 参数传递（与 runtime 的 BOXLI_* 同族）。
const (
	EnvMarker    = "BOXLI_SHIM"       // 存在即表示本进程应以 shim 身份运行
	EnvStoreRoot = "BOXLI_STORE_ROOT" // 数据目录
	EnvContainer = "BOXLI_CONTAINER"  // 容器 ID

	markerValue = "1"
	logFileName = "container.log"  // 容器与 shim 的合并日志（append）
	GraceHold   = 10 * time.Second // init 收到 SIGTERM 后强杀宽限
)

// ErrShimNotRequested 表示当前进程没有 shim 标记。
var ErrShimNotRequested = errors.New("boxli/shim: 本进程不是以 shim 身份启动的")

// IsShimProcess 报告当前进程是否被以 shim 身份重执行（main 分流用）。
func IsShimProcess() bool { return os.Getenv(EnvMarker) == markerValue }

// LogPath 返回容器日志文件路径 <root>/containers/<id>/container.log。
func LogPath(storeRoot, id string) string {
	return filepath.Join(storeRoot, "containers", id, logFileName)
}

// shouldRestart 判定 shim 是否按策略重启（AGENTS.md《容器自启动标志》）。
func shouldRestart(r store.Restart, exitCode int) bool {
	switch r {
	case store.RestartAlways, store.RestartUnlessStoped:
		return true
	case store.RestartOnFailure:
		return exitCode != 0
	case store.RestartNo:
		return false
	}
	return false
}

// restartBackoff 是重启退避：第 1/2/3/4 次分别等 1s、2s、4s、8s，此后封顶 30s。
func restartBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Second
	case attempt == 2:
		return 2 * time.Second
	case attempt == 3:
		return 4 * time.Second
	case attempt == 4:
		return 8 * time.Second
	default:
		return 30 * time.Second
	}
}
