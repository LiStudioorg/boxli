// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"os"
	"path/filepath"
	"testing"
)

// seedImage 直接手工铺一个镜像目录（state.json + index.json），不经 Put。
func seedImage(t *testing.T, s *Store, name, version, ref, pulledAt, arch string, layers int) {
	t.Helper()
	dir := s.ImageDir(name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := `{"ref":"` + ref + `","pulledAt":"` + pulledAt + `","sourcePath":"/tmp/x.licore","sourceSizeBytes":12345,"layersVerified":true}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(st), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"architecture":"` + arch + `","layers":[`
	for i := range layers {
		if i > 0 {
			idx += ","
		}
		idx += `{"path":"layers/00000` + string(rune('1'+i)) + `.l.tar.gz"}`
	}
	idx += "]}"
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListImagesEmpty(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	infos, err := s.ListImages()
	if err != nil {
		t.Fatalf("空 store（无 images 目录）应合法: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("应为空列表: %+v", infos)
	}
}

func TestListImagesSortAndFields(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	seedImage(t, s, "alice/myapp", "v1", "alice/myapp:v1", "2026-10-01T04:00:00Z", "arm64", 3)
	seedImage(t, s, "alice/base", "v1", "alice/base:v1", "2026-10-01T03:30:00Z", "amd64", 1)
	seedImage(t, s, "alice/myapp", "v2", "alice/myapp:v2", "2026-10-01T04:00:00Z", "arm64", 2)

	infos, err := s.ListImages()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 3 {
		t.Fatalf("期望 3 条，实得 %d", len(infos))
	}
	// 时间倒序；同时间按 Ref 字典序：myapp:v1 < myapp:v2
	wantRefs := []string{"alice/myapp:v1", "alice/myapp:v2", "alice/base:v1"}
	for i, want := range wantRefs {
		if infos[i].Ref != want {
			t.Fatalf("第 %d 条 = %s，期望 %s", i, infos[i].Ref, want)
		}
	}
	if infos[0].Architecture != "arm64" || infos[0].LayerCount != 3 {
		t.Fatalf("字段合并错误: %+v", infos[0])
	}
	if infos[0].SourceFileBytes != 12345 {
		t.Fatalf("size 错误: %+v", infos[0])
	}
}

func TestListImagesSkipsCorrupt(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	seedImage(t, s, "good/img", "v1", "good/img:v1", "2026-10-01T04:00:00Z", "amd64", 1)
	// 损坏 state.json 的镜像：跳过但不报错
	badDir := s.ImageDir("bad", "img")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// index.json 缺失：同样跳过
	noIdx := s.ImageDir("noidx", "v1")
	if err := os.MkdirAll(noIdx, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noIdx, "state.json"),
		[]byte(`{"ref":"noidx:v1","pulledAt":"2026-10-01T04:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	infos, err := s.ListImages()
	if err != nil {
		t.Fatalf("个别损坏不应导致整体失败: %v", err)
	}
	if len(infos) != 1 || infos[0].Ref != "good/img:v1" {
		t.Fatalf("期望仅 good/img:v1，实得 %+v", infos)
	}
}
