// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/LiStudioorg/boxli/internal/boot"
	"github.com/LiStudioorg/boxli/internal/shim"
	"github.com/LiStudioorg/boxli/internal/store"
)

// StopResult 是一次 stop 的结果。
type StopResult struct {
	// Container 是被停止的容器配置。
	Container *store.ContainerConfig
	// WasRunning 表示停止前容器是否在运行。
	WasRunning bool
	// Forced 表示容器未在宽限期内退出，已 SIGKILL。
	Forced bool
	// Waited 是实际等待时长。
	Waited time.Duration
}

// DefaultStopTimeout 是 stop 等待容器退出的默认宽限，必须大于
// shim.GraceHold：shim 转发 SIGTERM 后还有一层自己的强杀宽限，stop 若
// 抢在它之前强杀，就会与 shim 写终态竞态并让退出码丢失。
const DefaultStopTimeout = shim.GraceHold + 5*time.Second

// stopPollInterval 是等待 shim 退出时的轮询间隔（容器数量级小，轮询足够）。
const stopPollInterval = 100 * time.Millisecond

// Stop 停止一个容器（用 background ctx 调用 StopWithContext）。
//  1. 先写 stopped-by-user 标记——即便容器已停止也写，保证 unless-stopped
//     容器下次开机不被拉起（用户意图优先于当前运行状态）；
//  2. 若 shim 存活则 SIGTERM（shim 会把 SIGTERM 转发给容器 init 并自行退出）；
//  3. 宽限期内等 shim 退出，超时对 shim 与 init 双 SIGKILL，并补写状态。
//
// 已停止的容器不算错误（幂等）：只写标记并返回 WasRunning=false。
func Stop(st *store.Store, idOrName string, timeout time.Duration) (*StopResult, error) {
	return StopWithContext(context.Background(), st, idOrName, timeout)
}

// StopWithContext 是 Stop 的 ctx 可取消形态：ctx 取消时尽快返回
// ctx.Err()，供 stop/rm 支持 Ctrl+C，避免在 shim/netlink 卡住时永久挂起。
func StopWithContext(ctx context.Context, st *store.Store, idOrName string, timeout time.Duration) (*StopResult, error) {
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	cfg, err := st.FindContainer(idOrName)
	if err != nil {
		return nil, err
	}
	res := &StopResult{Container: cfg}

	// 1. 先落用户意图标记：后续任何失败都不会让 unless-stopped 意外复活。
	if err := st.MarkStoppedByUser(cfg.ID); err != nil {
		return res, err
	}

	state, ok, err := st.ReadRuntimeState(cfg.ID)
	if err != nil {
		return res, err
	}
	if !ok || !state.Running || !boot.PidAlive(state.ShimPID) {
		// 已停止或 shim 已丢失：收尾补齐状态，标记保留。
		if ok && state.Running {
			state.Running = false
			state.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			if err := st.WriteRuntimeState(cfg.ID, state); err != nil {
				return res, err
			}
		}
		slog.Debug("容器未在运行，仅记录停止标记", "id", cfg.ID)
		return res, nil
	}

	res.WasRunning = true
	shimPID, initPID := state.ShimPID, state.InitPID
	start := time.Now()
	if err := syscall.Kill(shimPID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return res, fmt.Errorf("向 shim %d 发送 SIGTERM 失败: %w", shimPID, err)
	}
	slog.Debug("已向 shim 发送 SIGTERM", "id", cfg.ID, "shimPid", shimPID)

	// 2. 等 shim 退出：正常的 shim 会在容器 init 收尾后写状态并退出。
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			res.Waited = time.Since(start)
			return res, ctx.Err()
		}
		if !boot.PidAlive(shimPID) {
			break
		}
		stt, _, rerr := st.ReadRuntimeState(cfg.ID)
		if rerr == nil && !stt.Running {
			break
		}
		select {
		case <-ctx.Done():
			res.Waited = time.Since(start)
			return res, ctx.Err()
		case <-time.After(stopPollInterval):
		}
	}
	res.Waited = time.Since(start)

	// 3. 超时强杀：先杀 shim（生命周期持有者），再杀容器 init（防孤儿残留）。
	// 只在 shim 未退出时执行——正常停止路径下 init 已随 SIGTERM 收尾，
	// 误杀可能命中已复用的 PID。
	if boot.PidAlive(shimPID) {
		res.Forced = true
		slog.Warn("容器未在宽限期内退出，强制终止", "id", cfg.ID, "shimPid", shimPID, "timeout", timeout.String())
		_ = syscall.Kill(shimPID, syscall.SIGKILL)
		if initPID > 0 && initPID != shimPID && boot.PidAlive(initPID) {
			_ = syscall.Kill(initPID, syscall.SIGKILL)
		}
	}

	// 4. shim 被强杀时来不及写终态：补写 running=false，避免 ps/boot 误判。
	if stt, ok, rerr := st.ReadRuntimeState(cfg.ID); rerr == nil && ok && stt.Running {
		stt.Running = false
		stt.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err := st.WriteRuntimeState(cfg.ID, stt); err != nil {
			return res, err
		}
	}
	return res, nil
}
