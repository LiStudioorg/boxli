// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	dir := t.TempDir()
	ok := &Config{Rootfs: dir, Cmd: []string{"/bin/sh"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}

	cases := []struct {
		name string
		cfg  Config
	}{
		{"空 rootfs", Config{Cmd: []string{"x"}}},
		{"rootfs 不存在", Config{Rootfs: filepath.Join(dir, "nope"), Cmd: []string{"x"}}},
		{"rootfs 非目录", func() Config {
			f := filepath.Join(dir, "file")
			if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			return Config{Rootfs: f, Cmd: []string{"x"}}
		}()},
		{"空命令", Config{Rootfs: dir}},
		{"参数含 NUL", Config{Rootfs: dir, Cmd: []string{"a\x00b"}}},
	}
	for _, c := range cases {
		if err := c.cfg.Validate(); !errors.Is(err, ErrBadConfig) {
			t.Errorf("%s: 期望 ErrBadConfig，实得 %v", c.name, err)
		}
	}
}

func TestIsInitProcess(t *testing.T) {
	t.Setenv(envInitMarker, "")
	if IsInitProcess() {
		t.Fatal("未设置标记却判定为 init")
	}
	t.Setenv(envInitMarker, "1")
	if !IsInitProcess() {
		t.Fatal("标记为 1 应判定为 init")
	}
}

func TestEnvWithoutBoxli(t *testing.T) {
	t.Setenv("BOXLI_CHILD", "1")
	t.Setenv("BOXLI_ROOTFS", "/x")
	os.Setenv("PATH_KEEP", "yes")
	defer os.Unsetenv("PATH_KEEP")

	got := envWithoutBoxli()
	found := map[string]bool{}
	for _, kv := range got {
		found[kv] = true
	}
	if found["BOXLI_CHILD=1"] || found["BOXLI_ROOTFS=/x"] {
		t.Fatalf("内部变量未剥离: %v", got)
	}
	if !found["PATH_KEEP=yes"] {
		t.Fatalf("普通变量丢失: %v", got)
	}
}
