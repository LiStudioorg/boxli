// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// ErrNotRoot 表示既非 root 又未显式允许 rootless，无法启动容器。
var ErrNotRoot = errors.New("boxli/runtime: 需要 root，或启用 user namespace（自动于 euid!=0）")

// newCID 生成本次容器实例 ID：宿主 PID + 随机后缀，保证同一 rootfs 上
// 并发容器的旧根目录名互不冲突。
func newCID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.Itoa(os.Getpid())
	}
	return strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(b[:])
}

// Start 以容器方式重执行当前二进制（/proc/self/exe + `init` 参数），
// 创建 PID/Mount/UTS/IPC namespace（非 root 追加 USER），等待其退出并返回结果。
// onChildStart 在子进程启动后、等待前被调用（可传 nil），用于内存采样等观测。
func Start(cfg *Config, onChildStart func(pid int)) (*StartResult, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rootfsAbs, err := filepath.Abs(cfg.Rootfs)
	if err != nil {
		return nil, fmt.Errorf("rootfs 绝对路径: %w", err)
	}
	rootless := cfg.Rootless || os.Geteuid() != 0

	env := append(os.Environ(),
		envInitMarker+"=1",
		envRootfs+"="+rootfsAbs,
		envCID+"="+newCID(),
	)
	if cfg.Hostname != "" {
		env = append(env, envHostname+"="+cfg.Hostname)
	}
	env = append(env, cfg.Env...)
	env = append(env,
		envChildCmdCountKey+"="+strconv.Itoa(len(cfg.Cmd)),
	)
	for i, arg := range cfg.Cmd {
		env = append(env, envChildCmdPrefix+strconv.Itoa(i)+"="+arg)
	}

	cmd := exec.Command("/proc/self/exe", "init")
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	flags := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC)
	sys := &syscall.SysProcAttr{Cloneflags: flags}
	if rootless {
		sys.Cloneflags |= syscall.CLONE_NEWUSER
		sys.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		sys.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
		sys.GidMappingsEnableSetgroups = false
	} else if os.Geteuid() != 0 {
		return nil, ErrNotRoot
	}
	cmd.SysProcAttr = sys

	slog.Debug("启动容器 init", "rootfs", rootfsAbs, "cmd", cfg.Cmd, "rootless", rootless)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("fork 容器 init 失败: %w", err)
	}
	if onChildStart != nil {
		onChildStart(cmd.Process.Pid)
	}
	res := &StartResult{ChildPID: cmd.Process.Pid}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code := ee.ExitCode()
			if code < 0 {
				// 信号死亡：换算为 128+signum（shell 惯例）。
				if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					code = 128 + int(ws.Signal())
				} else {
					code = 1
				}
			}
			res.ExitCode = code
			return res, nil
		}
		return res, fmt.Errorf("等待容器 init 失败: %w", err)
	}
	return res, nil
}
