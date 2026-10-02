// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestVolMgr(t *testing.T) *VolumeManager {
	t.Helper()
	m, err := NewVolumeManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewVolumeManager: %v", err)
	}
	return m
}

func TestVolumeCreateInspectRemove(t *testing.T) {
	m := newTestVolMgr(t)
	v, err := m.Create("data", DriverLocal, 1024)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v.Driver != DriverLocal || v.Size != 1024 {
		t.Errorf("元数据不符: %+v", v)
	}
	// 数据目录在 volumes/<name> 下且已创建。
	if _, err := os.Stat(filepath.Join(m.Root(), "volumes", "data")); err != nil {
		t.Fatalf("数据目录未创建: %v", err)
	}
	got, err := m.Inspect("data")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if got.Name != "data" {
		t.Errorf("Inspect 返回名不符: %s", got.Name)
	}
	// 重复创建报已存在。
	if _, err := m.Create("data", DriverLocal, 0); !errors.Is(err, ErrVolumeExists) {
		t.Errorf("期望 ErrVolumeExists，got %v", err)
	}
	if err := m.Remove("data"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := m.Inspect("data"); !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("删除后应 NotFound，got %v", err)
	}
}

func TestVolumeRejectsBadNameAndDriver(t *testing.T) {
	m := newTestVolMgr(t)
	if _, err := m.Create("bad/name", DriverLocal, 0); !errors.Is(err, ErrVolumeBad) {
		t.Errorf("期望 ErrVolumeBad，got %v", err)
	}
	if _, err := m.Create("v", "nfs", 0); !errors.Is(err, ErrBadDriver) {
		t.Errorf("期望 ErrBadDriver，got %v", err)
	}
}

func TestVolumeListAndPrune(t *testing.T) {
	m := newTestVolMgr(t)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := m.Create(n, DriverLocal, 0); err != nil {
			t.Fatalf("Create %s: %v", n, err)
		}
	}
	all, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("期望 3 个卷，got %d", len(all))
	}
	n, err := m.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("Prune 期望删 3 个，got %d", n)
	}
	if rest, _ := m.List(); len(rest) != 0 {
		t.Errorf("Prune 后仍有卷: %d", len(rest))
	}
}

func TestVolumeClone(t *testing.T) {
	m := newTestVolMgr(t)
	if _, err := m.Create("orig", DriverLocal, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 写入数据。
	if err := os.WriteFile(filepath.Join(m.Root(), "volumes", "orig", "hello.txt"), []byte("world"), 0o644); err != nil {
		t.Fatalf("写数据: %v", err)
	}
	c, err := m.Clone("orig", "copy")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if c.Name != "copy" || c.Driver != DriverLocal {
		t.Errorf("Clone 元数据不符: %+v", c)
	}
	data, err := os.ReadFile(filepath.Join(m.Root(), "volumes", "copy", "hello.txt"))
	if err != nil || string(data) != "world" {
		t.Errorf("Clone 数据未复制: %q, %v", data, err)
	}
	// 克隆同名目标应报已存在。
	if _, err := m.Clone("orig", "copy"); !errors.Is(err, ErrVolumeExists) {
		t.Errorf("期望 ErrVolumeExists，got %v", err)
	}
}

func TestVolumeSnapshot(t *testing.T) {
	m := newTestVolMgr(t)
	if _, err := m.Create("sv", DriverLocal, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(m.Root(), "volumes", "sv", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("写数据: %v", err)
	}
	path, err := m.Snapshot("sv")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "f.txt")); err != nil {
		t.Errorf("快照未含数据文件: %v", err)
	}
}

func TestVolumeMountpoint(t *testing.T) {
	m := newTestVolMgr(t)
	if _, err := m.Create("mp", DriverLocal, 0); err != nil {
		t.Fatalf("Create: %v", err)
	}
	mp, err := m.Mountpoint("mp")
	if err != nil {
		t.Fatalf("Mountpoint: %v", err)
	}
	if want := filepath.Join(m.Root(), "volumes", "mp"); mp != want {
		t.Errorf("挂载点 %q != %q", mp, want)
	}
}

// TestVolumeValidAndDrivers 覆盖卷字段校验、驱动名 getter 与默认存储根。
func TestVolumeValidAndDrivers(t *testing.T) {
	good := []*Volume{{Name: "datavol", Driver: DriverLocal}, {Name: "m", Driver: DriverTmpfs}}
	for _, v := range good {
		if !v.Valid() {
			t.Errorf("预期合法: %+v", v)
		}
	}
	bad := []*Volume{
		{Name: "", Driver: DriverLocal},
		{Name: "a/b", Driver: DriverLocal},
		{Name: "..x", Driver: DriverLocal},
		{Name: "x", Driver: "nosuchdrv"},
		{Name: "x"},
	}
	for _, v := range bad {
		if v.Valid() {
			t.Errorf("预期非法: %+v", v)
		}
	}
	// 驱动名 getter。
	if dt := drv(DriverTmpfs); dt != nil && dt.Name() != DriverTmpfs {
		t.Errorf("tmpfs driver Name()=%q", dt.Name())
	}
	// checkDriver。
	if err := checkDriver(DriverLocal); err != nil {
		t.Errorf("checkDriver(local)=%v", err)
	}
	if err := checkDriver("nope"); err == nil {
		t.Error("checkDriver(unknown) 应报错")
	}
	// 默认存储根非空。
	if r, err := defaultStoreRootV(); err != nil || r == "" {
		t.Errorf("defaultStoreRootV=%q err=%v", r, err)
	}
}
