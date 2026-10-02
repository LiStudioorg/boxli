// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package shim

import (
	"context"
	"errors"
	"testing"

	"github.com/LiStudioorg/boxli/internal/runtime"
	"github.com/LiStudioorg/boxli/internal/store"
)

// TestRunMarksStoppedWhenInitExits 验证：init 进程退出后，shim 必须把
// runtime.json 里的 Running 置为 false（不出现"Up 但进程已死"的假状态）。
func TestRunMarksStoppedWhenInitExits(t *testing.T) {
	// 用 fake StartWith 模拟 init 立即退出（code=7），且不触发重启（restart=no）。
	old := startWithFn
	startWithFn = func(_ *runtime.Config, onChild func(int), _ *runtime.StartOptions) (*runtime.StartResult, error) {
		if onChild != nil {
			onChild(42)
		}
		return &runtime.StartResult{ChildPID: 42, ExitCode: 7}, nil
	}
	defer func() { startWithFn = old }()

	st := &store.Store{Root: t.TempDir()}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: "abc123def456", Name: "web", ImageRef: "x:v1",
		Rootfs: "/tmp/x", Restart: store.RestartNo, Cmd: []string{"/x"},
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), &Options{Store: st, Cfg: cfg}); err != nil {
		t.Fatal(err)
	}
	state, ok, err := st.ReadRuntimeState(cfg.ID)
	if err != nil || !ok {
		t.Fatalf("读状态失败: ok=%v err=%v", ok, err)
	}
	if state.Running {
		t.Fatal("init 退出后 Running 仍为 true（假 Up）")
	}
	if state.ExitCode != 7 {
		t.Fatalf("退出码未回写: %d", state.ExitCode)
	}
	_ = cfg.Env
}

// TestRunMarksStoppedWhenStartFails 验证：StartWith 返回错误（init 根本没起来，
// 如网络 veth 装配失败）时，shim 不得留下 Running=true 的假状态。
func TestRunMarksStoppedWhenStartFails(t *testing.T) {
	old := startWithFn
	startWithFn = func(_ *runtime.Config, _ func(int), _ *runtime.StartOptions) (*runtime.StartResult, error) {
		return nil, errors.New("配置容器网络失败") // 仿"容器侧 veth 未就绪"
	}
	defer func() { startWithFn = old }()

	st := &store.Store{Root: t.TempDir()}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: "abc123def456", Name: "web", ImageRef: "x:v1",
		Rootfs: "/tmp/x", Restart: store.RestartNo, Cmd: []string{"/x"},
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), &Options{Store: st, Cfg: cfg}); err == nil {
		t.Fatal("启动失败 shim 应返回错误")
	}
	state, ok, _ := st.ReadRuntimeState(cfg.ID)
	if !ok || state.Running {
		t.Fatal("启动失败后 Running 不得为 true（假 Up）")
	}
}
