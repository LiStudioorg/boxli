// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// 子进程通过环境变量传递初始化参数（exec 后仍然存活）。
const (
	envInitMarker       = "BOXLI_CHILD"    // 存在即表示本进程是容器 init
	envRootfs           = "BOXLI_ROOTFS"   // 新根目录（宿主机路径）
	envHostname         = "BOXLI_HOSTNAME" // 容器 UTS hostname
	envChildCmdPrefix   = "BOXLI_ARG"      // BOXLI_ARG0..N 用户命令 argv
	envChildCmdCountKey = "BOXLI_ARGC"     // argv 参数个数
	envCID              = "BOXLI_CID"      // 本次容器实例唯一 ID（旧根目录名后缀）
)

// 哨兵错误。
var (
	// ErrNotInit 表示当前进程不是被 fork 出来的容器 init。
	ErrNotInit = errors.New("boxli/runtime: 当前进程不是容器 init")
	// ErrBadConfig 表示启动配置非法。
	ErrBadConfig = errors.New("boxli/runtime: 配置非法")
	// ErrUnsupported 表示本平台尚无运行时后端（非 Linux 文件实现）。
	ErrUnsupported = errors.New("boxli/runtime: 本平台运行时未实现（阶段 2 仅支持 linux）")
)

// Config 描述一次容器启动。
type Config struct {
	// Rootfs 是容器新根目录在宿主机上的路径，必须已存在。
	Rootfs string
	// Hostname 是容器 UTS 名，空则沿用默认。
	Hostname string
	// Cmd 是容器 1 号进程 argv，必填。
	Cmd []string
	// Env 是容器环境变量（KEY=VALUE）。
	Env []string
	// Rootless 强制走 user namespace 路线；false 时按 euid 自动决定。
	Rootless bool
}

// StartResult 是一次容器启动（父/shim 侧）的结果。
type StartResult struct {
	// ChildPID 是容器 init 进程在宿主上的 PID（shim 视角）。
	ChildPID int
	// ExitCode 是容器 1 号进程的退出码（信号死亡时为 128+signum）。
	ExitCode int
}

// Validate 检查配置完备性。
func (c *Config) Validate() error {
	if c.Rootfs == "" {
		return fmt.Errorf("rootfs 为空: %w", ErrBadConfig)
	}
	fi, err := os.Stat(c.Rootfs)
	if err != nil {
		return fmt.Errorf("rootfs %s: %s: %w", c.Rootfs, err, ErrBadConfig)
	}
	if !fi.IsDir() {
		return fmt.Errorf("rootfs %s 不是目录: %w", c.Rootfs, ErrBadConfig)
	}
	if len(c.Cmd) == 0 {
		return fmt.Errorf("容器命令为空: %w", ErrBadConfig)
	}
	for _, seg := range c.Cmd {
		if strings.ContainsRune(seg, 0) {
			return fmt.Errorf("命令参数含 NUL: %w", ErrBadConfig)
		}
	}
	return nil
}

// IsInitProcess 报告当前进程是否为 fork 出来的容器 init。main.go 用它分流。
func IsInitProcess() bool { return os.Getenv(envInitMarker) == "1" }
