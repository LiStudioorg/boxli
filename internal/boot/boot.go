// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package boot 实现 `boxli boot` 的编排逻辑：扫描容器状态目录，按 restart
// 策略一次性拉起应自启的容器（每容器 fork 一个 shim），执行完即返回。
// 设计见 AGENTS.md《开机自启动机制》：boot 非常驻，也不直接持有容器生命周期。
package boot

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/LiStudioorg/licore/internal/shim"
	"github.com/LiStudioorg/licore/internal/store"
)

// LaunchFunc 启动一个容器的 shim 并返回 shim PID。测试注入 fake 实现；
// 为 nil 时使用默认的 shim.Reexec。
type LaunchFunc func(cfg *store.ContainerConfig) (pid int, err error)

// Skip 记录一个未拉起的容器及原因。
type Skip struct {
	Cfg    *store.ContainerConfig
	Reason string
}

// Failure 记录一个拉起失败的容器及错误。
type Failure struct {
	Cfg *store.ContainerConfig
	Err error
}

// Result 是一次 boot 的汇总。
type Result struct {
	// Started 为成功拉起的容器与其 shim PID。
	Started []*store.ContainerConfig
	// PIDs 与 Started 对齐，记录各 shim PID。
	PIDs []int
	// Skipped / Failed 为非致命跳过与失败，逐项处理不中断整体。
	Skipped []Skip
	Failed  []Failure
}

// ErrPartial 表示部分容器拉起失败（Result.Failed 非空）。
var ErrPartial = errors.New("boxli/boot: 部分容器启动失败")

// ShimLauncher 返回默认启动器：fork 脱离终端的 shim 进程。
func ShimLauncher(st *store.Store) LaunchFunc {
	return func(cfg *store.ContainerConfig) (int, error) {
		p, err := shim.Reexec(st.Root, cfg.ID)
		if err != nil {
			return 0, err
		}
		return p.Pid, nil
	}
}

// StartAll 扫描 st 下全部容器，拉起 BootEligible 且未在运行的容器。
// launch 为 nil 时用 ShimLauncher。单个容器的跳过/失败记录进 Result，
// 不中断整体扫描；仅当整体扫描失败（目录不可读等）才返回非 nil error。
// 若存在拉起失败，返回的 error 同时包装 ErrPartial。
func StartAll(st *store.Store, launch LaunchFunc) (*Result, error) {
	if launch == nil {
		launch = ShimLauncher(st)
	}
	all, err := st.ListContainers()
	if err != nil {
		return nil, err
	}
	res := &Result{}
	for _, cfg := range all {
		switch {
		case !cfg.Restart.BootEligible():
			res.Skipped = append(res.Skipped, Skip{Cfg: cfg, Reason: "restart=" + string(cfg.Restart) + " 不参与开机自启"})
		case cfg.Restart == store.RestartUnlessStoped && st.IsStoppedByUser(cfg.ID):
			res.Skipped = append(res.Skipped, Skip{Cfg: cfg, Reason: "已被用户停止（stopped-by-user）"})
		case shimRunning(st, cfg.ID):
			res.Skipped = append(res.Skipped, Skip{Cfg: cfg, Reason: "已在运行"})
		default:
			pid, err := launch(cfg)
			if err != nil {
				res.Failed = append(res.Failed, Failure{Cfg: cfg, Err: err})
				continue
			}
			res.Started = append(res.Started, cfg)
			res.PIDs = append(res.PIDs, pid)
		}
	}
	if len(res.Failed) > 0 {
		return res, fmt.Errorf("%d/%d 个容器启动失败: %w", len(res.Failed), len(all), ErrPartial)
	}
	return res, nil
}

// shimRunning 报告容器是否已有活着的 shim（runtime.json 记录且 PID 存活）。
func shimRunning(st *store.Store, id string) bool {
	stt, ok, err := st.ReadRuntimeState(id)
	if err != nil || !ok || !stt.Running {
		return false
	}
	return PidAlive(stt.ShimPID)
}

// PidAlive 报告进程是否存在（signal 0 探活；EPERM 表示存在但无权限，也算存活）。
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// 僵尸进程仍能通过 signal 0 探活，但它已经不干活了：宿主未回收时
	// 必须判死，否则 boot 会把僵死 shim 当作"已在运行"而不重启容器。
	if zombie, err := isZombie(pid); err == nil && zombie {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// isZombie 读取 /proc/<pid>/stat 判断进程是否处于僵尸态。
// 非 Linux 或读取失败时返回错误，调用方按"未知"处理（退回 signal 探活）。
func isZombie(pid int) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false, err
	}
	// 格式：pid (comm) state ...；comm 可能含空格与括号，取最后一个 ')' 之后。
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 >= len(data) {
		return false, fmt.Errorf("解析 /proc/%d/stat 失败", pid)
	}
	return data[i+2] == 'Z', nil
}

// StopAll 优雅停止全部在运行的容器：向每个存活 shim 发 SIGTERM（shim 收到后
// 转发容器 init 并退出，见 internal/shim）。供 `boxli shutdown` 使用。
// wait 是等待 shim 退出的总时限；到点仍存活的计入 Timeout。单容器错误不中断整体。
func StopAll(st *store.Store, wait time.Duration) (stopped, notRunning, timeout int, err error) {
	all, err := st.ListContainers()
	if err != nil {
		return 0, 0, 0, err
	}
	var pids []int
	for _, cfg := range all {
		stt, ok, err := st.ReadRuntimeState(cfg.ID)
		if err != nil {
			return stopped, notRunning, timeout, fmt.Errorf("读取容器 %s 状态失败: %w", cfg.ID, err)
		}
		if !ok || !stt.Running || !PidAlive(stt.ShimPID) {
			notRunning++
			continue
		}
		pids = append(pids, stt.ShimPID)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Warn("向 shim 发送 SIGTERM 失败", "pid", pid, "err", err)
		}
	}
	// 轮询等待 shim 退出（shim 内部还要等容器 init 收尾）。
	deadline := time.After(wait)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	pending := len(pids)
loop:
	for pending > 0 {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			pending = 0
			for _, pid := range pids {
				if PidAlive(pid) {
					pending++
				}
			}
		}
	}
	stopped = len(pids) - pending
	timeout = pending
	return stopped, notRunning, timeout, nil
}
