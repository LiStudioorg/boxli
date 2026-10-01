// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package boot 实现 `boxli boot` 的编排逻辑：扫描容器状态目录，按 restart
// 策略一次性拉起应自启的容器（每容器 fork 一个 shim），执行完即返回。
// 设计见 AGENTS.md《开机自启动机制》：boot 非常驻，也不直接持有容器生命周期。
package boot

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/LiStudioorg/boxli/internal/shim"
	"github.com/LiStudioorg/boxli/internal/store"
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
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
