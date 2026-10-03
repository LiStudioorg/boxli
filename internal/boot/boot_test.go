// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package boot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/LiStudioorg/licore/internal/store"
)

// fakeLauncher 记录被拉起的容器并返回可控结果。
type fakeLauncher struct {
	started  []string
	failFor  map[string]bool
	startedN int
}

func (f *fakeLauncher) launch(cfg *store.ContainerConfig) (int, error) {
	if f.failFor[cfg.Name] {
		return 0, errors.New("模拟 fork 失败")
	}
	f.started = append(f.started, cfg.Name)
	f.startedN++
	return 900 + f.startedN, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "licore-home"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func mkContainer(t *testing.T, st *store.Store, name, restart string) *store.ContainerConfig {
	t.Helper()
	id, err := store.NewContainerID()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: id, Name: name,
		ImageRef:  "demo:v1",
		Rootfs:    filepath.Join(st.ContainerDir(id), "rootfs"),
		Restart:   store.Restart(restart),
		Cmd:       []string{"/bin/sh"},
		CreatedAt: "2026-10-01T10:00:00Z",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStartAllEmpty(t *testing.T) {
	st := openStore(t)
	res, err := StartAll(st, (&fakeLauncher{}).launch)
	if err != nil {
		t.Fatalf("空目录应无错误: %v", err)
	}
	if len(res.Started) != 0 || len(res.Skipped) != 0 || len(res.Failed) != 0 {
		t.Fatalf("空目录结果应全空: %+v", res)
	}
}

func TestStartAllPolicyMatrix(t *testing.T) {
	st := openStore(t)
	mkContainer(t, st, "always", "always")
	mkContainer(t, st, "unstop", "unless-stopped")
	stopped := mkContainer(t, st, "stopped", "unless-stopped")
	mkContainer(t, st, "onfail", "on-failure")
	mkContainer(t, st, "none", "no")
	if err := st.MarkStoppedByUser(stopped.ID); err != nil {
		t.Fatal(err)
	}

	fl := &fakeLauncher{}
	res, err := StartAll(st, fl.launch)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Started) != 2 {
		t.Fatalf("应拉起 always 与 unless-stopped 共 2 个: %v", fl.started)
	}
	names := map[string]bool{}
	for _, c := range res.Started {
		names[c.Name] = true
	}
	if !names["always"] || !names["unstop"] {
		t.Fatalf("拉起集合错误: %v", names)
	}
	if len(res.Skipped) != 3 {
		t.Fatalf("应跳过 3 个（stopped/on-failure/no）: %+v", res.Skipped)
	}
	if len(res.PIDs) != 2 {
		t.Fatalf("PID 应与 Started 对齐: %v", res.PIDs)
	}
}

func TestStartAllAlwaysIgnoresStoppedMark(t *testing.T) {
	st := openStore(t)
	a := mkContainer(t, st, "always", "always")
	if err := st.MarkStoppedByUser(a.ID); err != nil {
		t.Fatal(err)
	}
	res, err := StartAll(st, (&fakeLauncher{}).launch)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Started) != 1 || res.Started[0].Name != "always" {
		t.Fatalf("always 应无视停止标记: %+v", res.Started)
	}
}

func TestStartAllSkipsCorruptDir(t *testing.T) {
	st := openStore(t)
	ok := mkContainer(t, st, "good", "always")
	badDir := st.ContainerDir("dead0000beef")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "config.json"), []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := StartAll(st, (&fakeLauncher{}).launch)
	if err != nil {
		t.Fatalf("损坏目录不应中断整体: %v", err)
	}
	if len(res.Started) != 1 || res.Started[0].ID != ok.ID {
		t.Fatalf("应仅拉起 good: %+v", res.Started)
	}
}

func TestStartAllFailurePartial(t *testing.T) {
	st := openStore(t)
	mkContainer(t, st, "ok", "always")
	mkContainer(t, st, "boom", "always")
	fl := &fakeLauncher{failFor: map[string]bool{"boom": true}}
	res, err := StartAll(st, fl.launch)
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("存在失败时应包装 ErrPartial: %v", err)
	}
	if len(res.Started) != 1 || len(res.Failed) != 1 {
		t.Fatalf("失败不应影响其他容器: started=%v failed=%v", res.Started, res.Failed)
	}
}

func TestStartAllSkipsAlreadyRunning(t *testing.T) {
	st := openStore(t)
	cfg := mkContainer(t, st, "web", "always")
	// shim PID=1（init，必然存活）→ 视为已在运行，避免重复 shim
	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{Running: true, ShimPID: 1}); err != nil {
		t.Fatal(err)
	}
	fl := &fakeLauncher{}
	res, err := StartAll(st, fl.launch)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Started) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != "已在运行" {
		t.Fatalf("已运行容器必须跳过: %+v", res.Skipped)
	}
	// shim PID 不存在 → 僵死状态应重新拉起（重复启动不冲突）
	if err := st.WriteRuntimeState(cfg.ID, &store.RuntimeState{Running: true, ShimPID: 4194303}); err != nil {
		t.Fatal(err)
	}
	res, err = StartAll(st, fl.launch)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Started) != 1 {
		t.Fatalf("僵死状态应重新拉起: %+v", res)
	}
}

func TestPidAlive(t *testing.T) {
	if !PidAlive(os.Getpid()) {
		t.Fatal("当前进程必须判活")
	}
	if PidAlive(0) || PidAlive(-1) {
		t.Fatal("非正 PID 必须判死")
	}
	if PidAlive(4194303) {
		t.Log("警告：PID 4194303 竟存在（线程上限配置特殊），忽略")
	}
}

// TestPidAliveZombie 锁定僵尸进程判死语义：kill(pid,0) 对僵尸仍成功，
// 但僵死 shim 必须被视为"未运行"，否则 boot 会跳过重启。
func TestPidAliveZombie(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动测试进程: %v", err)
	}
	pid := cmd.Process.Pid
	// 子进程已退出但父进程未 Wait：此时为僵尸态。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if z, err := isZombie(pid); err == nil && z {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	z, err := isZombie(pid)
	if err != nil {
		t.Skipf("本环境无法读取 /proc/<pid>/stat: %v", err)
	}
	if !z {
		t.Skip("未能进入僵尸态，跳过")
	}
	if PidAlive(pid) {
		t.Fatal("僵尸进程必须判死")
	}
	_, _ = cmd.Process.Wait() // 回收
	if PidAlive(pid) {
		t.Fatal("回收后必须判死")
	}
}
