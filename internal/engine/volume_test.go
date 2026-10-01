// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LiStudioorg/boxli/internal/storage"
	"github.com/LiStudioorg/boxli/internal/store"
)

func TestParseVolumes(t *testing.T) {
	cases := []struct {
		raw       string
		target    string
		source    string
		readOnly  bool
		anonymous bool
	}{
		{"/data", "/data", "", false, true},
		{"/host:/data", "/data", "/host", false, false},
		{"/host:/data:ro", "/data", "/host", true, false},
	}
	for _, c := range cases {
		got, err := parseVolumes([]string{c.raw})
		if err != nil {
			t.Fatalf("parseVolumes(%q) 错误: %v", c.raw, err)
		}
		m := got[0]
		if m.Target != c.target || m.Source != c.source || m.ReadOnly != c.readOnly || m.Anonymous != c.anonymous {
			t.Fatalf("parseVolumes(%q)=%+v 期望 target=%q source=%q ro=%v anon=%v",
				c.raw, m, c.target, c.source, c.readOnly, c.anonymous)
		}
	}
}

func TestParseVolumesBad(t *testing.T) {
	for _, raw := range []string{"a:b:rw", "a:b:c:d", "relpath"} {
		if _, err := parseVolumes([]string{raw}); err == nil {
			t.Fatalf("parseVolumes(%q) 应报错", raw)
		}
	}
}

func TestWireVolumesBindAndAnonymous(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: "abc123def456", Name: "web", ImageRef: "x:v1",
		Restart: store.RestartNo, Cmd: []string{"/bin/sh"}, Rootfs: "/tmp/x",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	hostDir := filepath.Join(st.Root, "hostdir")
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mounts := []*volMount{{Source: hostDir, Target: "/data"}, {Target: "/anon", Anonymous: true}}
	if err := wireVolumesBeforeStart(st, cfg, mounts); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Mounts) != 2 {
		t.Fatalf("解析后挂载数错误: %d", len(cfg.Mounts))
	}
	if cfg.Mounts[0].Source != hostDir || cfg.Mounts[0].Target != "/data" || cfg.Mounts[0].ReadOnly {
		t.Fatalf("bind 挂载解析错误: %+v", cfg.Mounts[0])
	}
	// 匿名卷源应是卷数据目录（存在）。
	if cfg.Mounts[1].Source == "" || cfg.Mounts[1].Target != "/anon" {
		t.Fatalf("匿名卷解析错误: %+v", cfg.Mounts[1])
	}
	// config.json 应重写包含挂载。
	got, err := st.LoadContainer(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Mounts) != 2 {
		t.Fatalf("config.json 未回写挂载: %+v", got.Mounts)
	}
}

func TestWireVolumesNamedVolume(t *testing.T) {
	st := &store.Store{Root: t.TempDir()}
	// 预建一个命名卷。
	vm, err := storage.NewVolumeManager(st.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vm.Create("mydata", storage.DriverLocal, 0); err != nil {
		t.Fatal(err)
	}
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: "abc123def456", Name: "web", ImageRef: "x:v1",
		Restart: store.RestartNo, Cmd: []string{"/bin/sh"}, Rootfs: "/tmp/x",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if err := wireVolumesBeforeStart(st, cfg, []*volMount{{Source: "mydata", Target: "/data"}}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Mounts) != 1 || cfg.Mounts[0].Source == "" || cfg.Mounts[0].Source != filepath.Join(st.Root, "volumes", "mydata") {
		t.Fatalf("命名卷解析错误: %+v", cfg.Mounts)
	}
	// 不存在的命名卷应报错。
	if err := wireVolumesBeforeStart(st, cfg, []*volMount{{Source: "ghost", Target: "/x"}}); err == nil {
		t.Fatal("不存在的命名卷应报错")
	}
}
