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
	"time"

	"github.com/LiStudioorg/boxli/internal/network"
	"github.com/LiStudioorg/boxli/internal/resource"
)

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
	return StartWith(cfg, onChildStart, nil)
}

// StartWith 是 Start 的完整形态：支持自定义 stdio 与停止信号转发。
func StartWith(cfg *Config, onChildStart func(pid int), opts *StartOptions) (*StartResult, error) {
	if opts == nil {
		opts = &StartOptions{}
	}
	grace := opts.Grace
	if grace <= 0 {
		grace = 10 * time.Second
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rootfsAbs, err := filepath.Abs(cfg.Rootfs)
	if err != nil {
		return nil, fmt.Errorf("rootfs 绝对路径: %w", err)
	}

	// namespace 规划：探测内核能力后决定实际使用的组合。
	// 三种降级路径（root+无 userns / rootless / 无任何隔离）在此统一判定，
	// 失败返回 ErrNoNamespaces / ErrNotRoot 而非裸 EPERM。
	wantNet := false
	if mode, _, _, _, _, _, _, ok := parseNetEnv(cfg.Env); ok && *mode != network.ModeHost {
		wantNet = true
	}
	nsPlan, err := resolveNSPlan(wantNet)
	if err != nil {
		return nil, err
	}
	// cfg.Rootless 是调用方**强制**要求 userns；若内核不允许则明确报错，
	// 不静默忽略（静默忽略会让 rootless 请求变成"以 root 跑"，是安全问题）。
	if cfg.Rootless && !nsPlan.UseUserNamespace && os.Geteuid() == 0 {
		probe := probeNamespaces()
		if !probe.UserNSAllowed {
			return nil, fmt.Errorf("%w：调用方要求 rootless（user namespace），"+
				"但内核不允许创建 user namespace（%s 为 0 或 /proc/self/ns/user 缺失）",
				ErrUnsupported, maxUserNS)
		}
	}
	rootless := nsPlan.UseUserNamespace

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
	cmd.Stdin = firstNonNil(opts.Stdin, os.Stdin)
	cmd.Stdout = firstNonNil(opts.Stdout, os.Stdout)
	cmd.Stderr = firstNonNil(opts.Stderr, cmd.Stdout.(*os.File))

	// namespace 位与 userns 决策统一来自 nsPlan（见 nsplan_linux.go）。
	// 这里不再自行拼 CLONE_NEWNET，避免两处判断不一致。
	flags := nsPlan.Flags
	sys := &syscall.SysProcAttr{Cloneflags: flags}
	if rootless {
		sys.Cloneflags |= syscall.CLONE_NEWUSER
		sys.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		sys.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
		sys.GidMappingsEnableSetgroups = false
	}
	cmd.SysProcAttr = sys

	slog.Debug("启动容器 init", "rootfs", rootfsAbs, "cmd", cfg.Cmd, "rootless", rootless)
	if err := cmd.Start(); err != nil {
		// 权限不足（EPERM / EACCES）时给出 Android-有 Root 的清晰指引，不 panic。
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("fork 容器 init 失败：namespace 创建失败，权限不足。Android 需 Root，无 Root 环境官方不支持: %w", err)
		}
		return nil, fmt.Errorf("fork 容器 init 失败: %w", err)
	}
	if onChildStart != nil {
		onChildStart(cmd.Process.Pid)
	}
	// 容器已 fork（netns 就绪）：装配 veth 并把容器侧移入其 netns。
	if mode, cid, name, _, _, _, _, ok := parseNetEnv(cfg.Env); ok && *mode == network.ModeBridge {
		if err := network.AttachVeth(name, cid, cmd.Process.Pid); err != nil {
			// veth 装配失败：杀掉已 fork 的 init，避免启动一个无网络的可疑容器。
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return nil, fmt.Errorf("装配容器网络失败: %w", err)
		}
	}
	// 资源限制：把容器 init 写入其 cgroup.procs，令 CPU/内存/pids/io 限制生效。
	// 失败仅告警（无 cgroups v2 或无 root 时不影响容器启动）。
	if cid := cgroupIDFromEnv(cfg.Env); cid != "" {
		if err := resource.AddPID(cid, cmd.Process.Pid); err != nil {
			slog.Warn("把容器进程写入 cgroup 失败（可能需要 root 或 cgroups v2）", "container", cid, "err", err)
		}
	}
	res := &StartResult{ChildPID: cmd.Process.Pid}
	if opts.StopCh != nil {
		stopDone := make(chan struct{})
		go func(pid int) {
			select {
			case <-opts.StopCh:
				_ = syscall.Kill(pid, syscall.SIGTERM)
				timer := time.NewTimer(grace)
				defer timer.Stop()
				select {
				case <-timer.C:
					_ = syscall.Kill(pid, syscall.SIGKILL)
				case <-stopDone:
				}
			case <-stopDone:
			}
		}(cmd.Process.Pid)
		err := cmd.Wait()
		close(stopDone)
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				res.ExitCode = normalizeExitCode(ee)
				return res, nil
			}
			return res, fmt.Errorf("等待容器 init 失败: %w", err)
		}
		return res, nil
	}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = normalizeExitCode(ee)
			return res, nil
		}
		return res, fmt.Errorf("等待容器 init 失败: %w", err)
	}
	return res, nil
}

// normalizeExitCode 统一退出码语义：正常退出原样返回；信号死亡换算 128+signum。
func normalizeExitCode(ee *exec.ExitError) int {
	code := ee.ExitCode()
	if code >= 0 {
		return code
	}
	if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}

func firstNonNil(fallback *os.File, def *os.File) *os.File {
	if fallback != nil {
		return fallback
	}
	return def
}
