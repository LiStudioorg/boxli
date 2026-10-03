// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package shim

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LiStudioorg/licore/internal/store"
)

func TestShouldRestart(t *testing.T) {
	cases := []struct {
		r    store.Restart
		code int
		want bool
	}{
		{store.RestartNo, 0, false},
		{store.RestartNo, 1, false},
		{store.RestartAlways, 0, true},
		{store.RestartAlways, 137, true},
		{store.RestartUnlessStoped, 0, true},
		{store.RestartOnFailure, 0, false},
		{store.RestartOnFailure, 1, true},
		{store.RestartOnFailure, 137, true},
		{store.Restart("bogus"), 0, false},
	}
	for _, c := range cases {
		if got := shouldRestart(c.r, c.code); got != c.want {
			t.Errorf("shouldRestart(%s,%d)=%v 期望 %v", c.r, c.code, got, c.want)
		}
	}
}

func TestRestartBackoff(t *testing.T) {
	want := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := restartBackoff(i + 1); got != w {
			t.Errorf("restartBackoff(%d)=%v 期望 %v", i+1, got, w)
		}
	}
}

func TestIsShimProcess(t *testing.T) {
	t.Setenv(EnvMarker, "")
	if IsShimProcess() {
		t.Fatal("未设置标记应判定非 shim")
	}
	t.Setenv(EnvMarker, "1")
	if !IsShimProcess() {
		t.Fatal("标记为 1 应判定为 shim")
	}
}

func TestLogPath(t *testing.T) {
	got := LogPath("/data", "abc123")
	want := filepath.Join("/data", "containers", "abc123", "container.log")
	if got != want {
		t.Fatalf("LogPath = %s 期望 %s", got, want)
	}
}

func TestRunFromEnvGuards(t *testing.T) {
	// 非 shim 进程调用必须立即拒绝，不触碰文件系统。
	t.Setenv(EnvMarker, "")
	if err := RunFromEnv(t.Context()); err == nil {
		t.Fatal("期望 ErrShimNotRequested")
	}
	// 是 shim 但参数缺失。
	t.Setenv(EnvMarker, "1")
	t.Setenv(EnvStoreRoot, "")
	t.Setenv(EnvContainer, "")
	if err := RunFromEnv(t.Context()); err == nil {
		t.Fatal("缺失环境变量应报错")
	}
}

// TestRunFromEnvEnforcement 覆盖 RunFromEnv 的早退分支（无需真实 fork）。
func TestRunFromEnvEnforcement(t *testing.T) {
	// 非 shim 进程（未设 LICORE_SHIM）→ ErrShimNotRequested。
	os.Unsetenv(EnvMarker)
	if err := RunFromEnv(context.Background()); !errors.Is(err, ErrShimNotRequested) {
		t.Fatalf("非 shim 期望 ErrShimNotRequested，实得 %v", err)
	}
	// 设了 marker 但缺 store root / container id。
	os.Setenv(EnvMarker, markerValue)
	t.Cleanup(func() { os.Unsetenv(EnvMarker) })
	os.Unsetenv(EnvStoreRoot)
	if err := RunFromEnv(context.Background()); err == nil {
		t.Fatal("缺 EnvStoreRoot 应报错")
	}
}
