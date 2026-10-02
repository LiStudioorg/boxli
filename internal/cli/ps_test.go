// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LiStudioorg/boxli/internal/store"
)

// newPsTestStore 造一个含两个容器的数据目录：running（假 shim=当前进程）与
// stopped（已退出且带 stopped-by-user 标记）。
func newPsTestStore(t *testing.T) (*store.Store, string, string) {
	t.Helper()
	st := &store.Store{Root: t.TempDir()}
	mk := func(name string) string {
		id, err := store.NewContainerID()
		if err != nil {
			t.Fatal(err)
		}
		cfg := &store.ContainerConfig{
			ConfigVersion: 1, ID: id, Name: name, ImageRef: "demo:v1",
			Rootfs: filepath.Join(st.ContainerDir(id), "rootfs"), Restart: store.RestartAlways,
			Cmd:       []string{"/bin/sleep", "60"},
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := st.CreateContainer(cfg); err != nil {
			t.Fatal(err)
		}
		return id
	}
	runID := mk("running-one")
	stopID := mk("stopped-one")

	// running：shim 用本进程 PID 扮演，PidAlive 必然为真
	if err := st.WriteRuntimeState(runID, &store.RuntimeState{
		ShimPID: os.Getpid(), Running: true, ExitCode: -1,
		StartedAt: time.Now().UTC().Add(-90 * time.Second).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteRuntimeState(stopID, &store.RuntimeState{
		Running: false, ExitCode: 137, FinishedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStoppedByUser(stopID); err != nil {
		t.Fatal(err)
	}
	return st, runID, stopID
}

func runPs(t *testing.T, args ...string) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := newPsCommand(&buf)
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ps 执行失败: %v", err)
	}
	return buf.String()
}

func TestPsDefaultOnlyRunning(t *testing.T) {
	st, runID, stopID := newPsTestStore(t)
	out := runPs(t, "--data-dir", st.Root)
	if !strings.Contains(out, runID) || !strings.Contains(out, "running-one") {
		t.Fatalf("默认应列出运行中容器:\n%s", out)
	}
	if strings.Contains(out, stopID) || strings.Contains(out, "stopped-one") {
		t.Fatalf("默认不应列出已停止容器:\n%s", out)
	}
	// 表头齐全
	for _, col := range []string{"ID", "NAME", "IMAGE", "STATUS", "CREATED", "RESTART"} {
		if !strings.Contains(out, col) {
			t.Fatalf("缺少列 %s:\n%s", col, out)
		}
	}
	if !strings.Contains(out, "Up") {
		t.Fatalf("运行中容器状态应含 Up:\n%s", out)
	}
	if !strings.Contains(out, "demo:v1") || !strings.Contains(out, "always") {
		t.Fatalf("镜像/重启策略列错误:\n%s", out)
	}
}

func TestPsAllIncludesStopped(t *testing.T) {
	st, runID, stopID := newPsTestStore(t)
	out := runPs(t, "-a", "--data-dir", st.Root)
	if !strings.Contains(out, runID) || !strings.Contains(out, stopID) {
		t.Fatalf("-a 应同时列出运行中与已停止容器:\n%s", out)
	}
	if !strings.Contains(out, "Exited (137)") {
		t.Fatalf("已停止容器应显示退出码:\n%s", out)
	}
	if !strings.Contains(out, "user-stopped") {
		t.Fatalf("带停止标记的容器应标注 user-stopped:\n%s", out)
	}
}

func TestPsQuiet(t *testing.T) {
	st, runID, _ := newPsTestStore(t)
	out := runPs(t, "-q", "--data-dir", st.Root)
	if strings.TrimSpace(out) != runID {
		t.Fatalf("-q 应只输出 ID，实得 %q", out)
	}
}

func TestPsEmptyStore(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	out := runPs(t, "--data-dir", st.Root)
	if !strings.Contains(out, "暂无运行中的容器") {
		t.Fatalf("空 store 应友好提示:\n%s", out)
	}
	all := runPs(t, "-a", "--data-dir", st.Root)
	if !strings.Contains(all, "暂无容器") {
		t.Fatalf("-a 空 store 应友好提示:\n%s", all)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m30s"},
		{2*time.Hour + 5*time.Minute, "2h5m"},
		{26 * time.Hour, "1d2h"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Fatalf("humanDuration(%v) = %q 期望 %q", c.d, got, c.want)
		}
	}
}

// TestPsStatusStarting 验证 Starting 状态（装配中）被明确渲染，不显示 Up/Exited。
func TestPsStatusStarting(t *testing.T) {
	st, runID, _ := newPsTestStore(t)
	// 模拟 run -d 早期：已 fork shim（本进程）但装配未完成。
	if err := st.WriteRuntimeState(runID, &store.RuntimeState{
		ShimPID: os.Getpid(), Running: false, Status: store.StatusStarting, ExitCode: -1,
	}); err != nil {
		t.Fatal(err)
	}
	if s := psStatus(st, runID, &store.RuntimeState{Status: store.StatusStarting}, true, false, true); s != "Starting" {
		t.Fatalf("starting 应渲染为 Starting，实得 %q", s)
	}
	// Starting 的容器即使默认（非 -a）也应列出。
	rows, err := collectContainerRows(st, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == runID && r.Status == "Starting" {
			found = true
		}
	}
	if !found {
		t.Fatal("starting 容器未在默认 ps 中显示为 Starting")
	}
}
