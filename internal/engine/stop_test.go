// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/LiStudioorg/licore/internal/boot"
	"github.com/LiStudioorg/licore/internal/store"
)

func newStopTestStore(t *testing.T, name string, restart store.Restart) (*store.Store, *store.ContainerConfig) {
	t.Helper()
	st := &store.Store{Root: t.TempDir()}
	id, err := store.NewContainerID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: id, Name: name, ImageRef: "demo:v1",
		Rootfs:  filepath.Join(st.ContainerDir(id), "rootfs"),
		Restart: restart, Cmd: []string{"/bin/sleep", "60"},
		CreatedAt: "2026-10-01T10:00:00Z",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	return st, cfg
}

func TestStopNotRunning(t *testing.T) {
	st, cfg := newStopTestStore(t, "idle", store.RestartUnlessStoped)
	res, err := Stop(st, "idle", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.WasRunning || res.Forced {
		t.Fatalf("未运行容器不应等待/强杀: %+v", res)
	}
	// 标记必须落下：unless-stopped 下次开机不再拉起的关键
	if !st.IsStoppedByUser(cfg.ID) {
		t.Fatal("stop 必须写 stopped-by-user 标记")
	}
	if st.BootEligible(cfg) {
		t.Fatal("标记后 unless-stopped 不应再自启")
	}
}

func TestStopAlreadyStoppedStateUnchanged(t *testing.T) {
	st, cfg := newStopTestStore(t, "done", store.RestartNo)
	// 已停止且 shim 已消失，但 running 仍为 true（崩溃遗留）→ 应补写 running=false
	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{
		Running: true, ShimPID: 4194303, ExitCode: 0,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Stop(st, cfg.ID, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.WasRunning {
		t.Fatal("shim 已消失不应视为运行中")
	}
	stt, _, err := st.ReadRuntimeState(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stt.Running {
		t.Fatal("崩溃遗留的 running=true 应被补写为 false")
	}
}

func TestStopNotFound(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	if _, err := Stop(st, "ghost", time.Second); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("期望 ErrContainerNotFound，实得 %v", err)
	}
}

// TestStopRunningShim 用真实进程扮演 shim（响应 SIGTERM 退出），验证：
// 发送 SIGTERM、等其退出、状态清理，且不触发强杀。
func TestStopRunningShim(t *testing.T) {
	st, cfg := newStopTestStore(t, "web", store.RestartUnlessStoped)
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动测试进程: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	// 模拟 shim：收到 SIGTERM 即退出（并像真实 shim 一样先把 running 置 false）
	go func() {
		_ = cmd.Wait()
		stt, ok, _ := st.ReadRuntimeState(cfg.ID)
		if ok {
			stt.Running = false
			_ = st.WriteRuntimeState(cfg.ID, stt)
		}
	}()
	// InitPID 留 0：真实场景里它是 shim 的子进程，此处不伪造宿主 PID，
	// 避免强杀路径误伤测试进程自身。
	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{
		Running: true, ShimPID: cmd.Process.Pid, ExitCode: -1,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Stop(st, "web", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasRunning {
		t.Fatal("应判定为运行中")
	}
	if res.Forced {
		t.Fatal("shim 响应 SIGTERM，不应强杀")
	}
	if boot.PidAlive(cmd.Process.Pid) {
		t.Fatal("shim 应已退出")
	}
	stt, _, err := st.ReadRuntimeState(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stt.Running {
		t.Fatal("停止后 running 应为 false")
	}
	if !st.IsStoppedByUser(cfg.ID) {
		t.Fatal("停止标记应保留")
	}
}

// TestStopForcesStubbornShim 验证超时强杀：假 shim 忽略 SIGTERM，超时后应被
// SIGKILL 且状态被补写。
func TestStopForcesStubbornShim(t *testing.T) {
	if os.Getenv("LICORE_TEST_STUBBORN") == "1" {
		// 测试辅助进程：忽略 SIGTERM 常驻。
		signalIgnore()
		time.Sleep(30 * time.Second)
		return
	}
	st, cfg := newStopTestStore(t, "stubborn", store.RestartAlways)
	helper := exec.Command(os.Args[0], "-test.run=TestStopForcesStubbornShim")
	helper.Env = append(os.Environ(), "LICORE_TEST_STUBBORN=1")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _, _ = helper.Process.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = helper.Process.Kill(); <-reaped })
	// 等辅助进程装好信号处理，否则 SIGTERM 会在默认动作下直接杀死它。
	time.Sleep(300 * time.Millisecond)

	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{
		Running: true, ShimPID: helper.Process.Pid, ExitCode: -1,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := Stop(st, cfg.ID, 700*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Forced {
		t.Fatal("忽略 SIGTERM 的 shim 应被强杀")
	}
	// 强杀是异步的：给内核一点时间回收。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && boot.PidAlive(helper.Process.Pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if boot.PidAlive(helper.Process.Pid) {
		t.Fatal("强杀后 shim 不应存活")
	}
	stt, _, err := st.ReadRuntimeState(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stt.Running {
		t.Fatal("强杀后 running 应被补写为 false")
	}
	if !st.IsStoppedByUser(cfg.ID) {
		t.Fatal("停止标记应保留")
	}
}

// signalIgnore 让当前进程接收但不响应 SIGTERM/SIGINT（模拟不退让的 shim）。
// 用 signal.Notify 接管而非 signal.Ignore：后者在部分运行环境下语义不一致。
func signalIgnore() {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for range ch { // 收到即丢弃
		}
	}()
}
