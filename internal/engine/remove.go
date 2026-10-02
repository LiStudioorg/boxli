// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/LiStudioorg/boxli/internal/boot"
	"github.com/LiStudioorg/boxli/internal/resource"
	"github.com/LiStudioorg/boxli/internal/store"
)

// ErrContainerRunning 表示容器仍在运行，未加 --force 时拒绝删除。
var ErrContainerRunning = errors.New("boxli/engine: 容器正在运行")

// RemoveResult 是一次 rm 的结果。
type RemoveResult struct {
	// Container 是被删除的容器配置。
	Container *store.ContainerConfig
	// Stopped 表示因 -f 而先停止了运行中的容器。
	Stopped bool
}

// Remove 删除一个已停止容器。force=false 时运行中的容器直接拒绝并提示；
// force=true 时先按 Stop 语义停止（含 stopped-by-user 标记）再删除。
//
// 只删除 <root>/containers/<id>/ 整目录（配置、运行状态、日志、该容器独占的
// rootfs）；共享的镜像层缓存 layers/sha256/<hex> 不动，其他容器继续复用。
func Remove(st *store.Store, idOrName string, force bool) (*RemoveResult, error) {
	return RemoveWithContext(context.Background(), st, idOrName, force)
}

// RemoveWithContext 是 Remove 的 ctx 可取消形态（Ctrl+C 可中断 stop 等待）。
func RemoveWithContext(ctx context.Context, st *store.Store, idOrName string, force bool) (*RemoveResult, error) {
	cfg, err := st.FindContainer(idOrName)
	if err != nil {
		return nil, err
	}
	res := &RemoveResult{Container: cfg}

	running, err := containerRunning(st, cfg.ID)
	if err != nil {
		return res, err
	}
	if running {
		if !force {
			return res, fmt.Errorf("容器 %s（%s）正在运行，请先 boxli stop %s（或加 -f 强制删除）: %w",
				cfg.Name, cfg.ID, cfg.Name, ErrContainerRunning)
		}
		if _, err := StopWithContext(ctx, st, cfg.ID, 0); err != nil {
			return res, fmt.Errorf("强制删除前停止容器失败: %w", err)
		}
		res.Stopped = true
	}

	// 移除网络端点与 NAT（veth 随 netns 销毁，宿主侧 veth 由 Disconnect 清）。
	disconnectContainer(st, cfg)
	// 清理容器专属 cgroup（进程已停，组内无活进程）。
	if err := resource.Remove(cfg.ID); err != nil {
		slog.Warn("删除容器 cgroup 失败（可能已不存在）", "container", cfg.ID, "err", err)
	}
	// 目录整体删除；rootfs 在容器目录内，随之一并清理，同时释放容器名锁。
	if err := st.RemoveContainer(cfg.ID); err != nil {
		return res, fmt.Errorf("删除容器目录失败: %w", err)
	}
	return res, nil
}

// containerRunning 判断容器是否有存活的 shim（与 ps/boot 同一口径）。
func containerRunning(st *store.Store, id string) (bool, error) {
	state, ok, err := st.ReadRuntimeState(id)
	if err != nil {
		return false, err
	}
	return ok && state.Running && boot.PidAlive(state.ShimPID), nil
}
