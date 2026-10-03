// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiStudioorg/licore/internal/store"
)

// newRemoveTestStore 造一个含已停止容器的数据目录，并在容器目录里放
// rootfs 与日志，验证删除会一并清理。
func newRemoveTestStore(t *testing.T, name string) (*store.Store, *store.ContainerConfig) {
	t.Helper()
	st := &store.Store{Root: t.TempDir()}
	id, err := store.NewContainerID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: id, Name: name, ImageRef: "demo:v1",
		Rootfs: filepath.Join(st.ContainerDir(id), "rootfs"), Restart: store.RestartNo,
		Cmd: []string{"/bin/sleep", "60"}, CreatedAt: "2026-10-01T10:00:00Z",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Rootfs, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Rootfs, "bin", "sh"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.ContainerDir(id), "container.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteRuntimeState(id, &store.RuntimeState{Running: false, ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	return st, cfg
}

func TestRemoveStoppedContainer(t *testing.T) {
	st, cfg := newRemoveTestStore(t, "dead")
	// 共享层缓存：删除容器不应影响它
	layerDir := filepath.Join(st.Root, "layers", "sha256", "aa")
	if err := os.MkdirAll(layerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(st, "dead", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped {
		t.Fatal("已停止容器不应触发停止流程")
	}
	if res.Container.ID != cfg.ID {
		t.Fatalf("返回容器 ID 不符: %s", res.Container.ID)
	}
	if _, err := os.Stat(st.ContainerDir(cfg.ID)); !os.IsNotExist(err) {
		t.Fatalf("容器目录应被删除，stat err=%v", err)
	}
	if _, err := os.Stat(cfg.Rootfs); !os.IsNotExist(err) {
		t.Fatalf("容器 rootfs 应随目录删除，stat err=%v", err)
	}
	if _, err := os.Stat(layerDir); err != nil {
		t.Fatalf("共享层缓存不应被删除: %v", err)
	}
	// 删除后按名字查找应报不存在
	if _, err := st.FindContainer("dead"); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("期望 ErrContainerNotFound，实得 %v", err)
	}
}

func TestRemoveRefusesRunning(t *testing.T) {
	st, cfg := newRemoveTestStore(t, "busy")
	// 用测试进程自身 PID 扮演存活 shim
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动测试进程: %v", err)
	}
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-reaped })

	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{
		Running: true, ShimPID: cmd.Process.Pid, ExitCode: -1,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := Remove(st, "busy", false)
	if !errors.Is(err, ErrContainerRunning) {
		t.Fatalf("运行中容器应拒绝删除，实得 %v", err)
	}
	if _, err := os.Stat(st.ContainerDir(cfg.ID)); err != nil {
		t.Fatalf("拒绝删除后容器目录必须保留: %v", err)
	}
	// 提示里应给出可执行的下一步
	if err == nil || !strings.Contains(err.Error(), "licore stop") {
		t.Fatalf("错误提示应指引 licore stop: %v", err)
	}
}

func TestRemoveForceStopsThenDeletes(t *testing.T) {
	st, cfg := newRemoveTestStore(t, "forced")
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动测试进程: %v", err)
	}
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-reaped })

	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{
		Running: true, ShimPID: cmd.Process.Pid, ExitCode: -1,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := Remove(st, "forced", true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stopped {
		t.Fatal("-f 删除运行中容器应先停止")
	}
	if _, err := os.Stat(st.ContainerDir(cfg.ID)); !os.IsNotExist(err) {
		t.Fatalf("容器目录应被删除，stat err=%v", err)
	}
}

func TestRemoveNotFound(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	if _, err := Remove(st, "ghost", false); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("期望 ErrContainerNotFound，实得 %v", err)
	}
}
