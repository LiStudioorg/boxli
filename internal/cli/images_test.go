// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LiStudioorg/licore/internal/store"
)

func sampleInfos() []store.ImageInfo {
	return []store.ImageInfo{
		{State: store.State{Ref: "alice/myapp:v1", PulledAt: "2026-10-01T12:00:00Z", SourceFileBytes: 12_300_000}, Architecture: "arm64", LayerCount: 3},
		{State: store.State{Ref: "alice/base:v1", PulledAt: "2026-10-01T11:30:00Z", SourceFileBytes: 5_100_000}, Architecture: "arm64", LayerCount: 1},
	}
}

func TestRenderImagesTable(t *testing.T) {
	var buf bytes.Buffer
	if err := renderImages(&buf, sampleInfos(), false, ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"REPOSITORY", "TAG", "ARCH", "LAYERS", "SIZE", "CREATED",
		"alice/myapp", "v1", "arm64", "3", "12.3 MB",
		"alice/base", "1", "5.1 MB",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 导入时间倒序：myapp（12:00）在 base（11:30）之前
	if strings.Index(out, "alice/myapp") > strings.Index(out, "alice/base") {
		t.Fatal("未按导入时间倒序")
	}
}

func TestRenderImagesQuiet(t *testing.T) {
	var buf bytes.Buffer
	if err := renderImages(&buf, sampleInfos(), true, ""); err != nil {
		t.Fatal(err)
	}
	want := "alice/myapp:v1\nalice/base:v1\n"
	if buf.String() != want {
		t.Fatalf("quiet 输出 = %q，期望 %q", buf.String(), want)
	}
}

func TestRenderImagesFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := renderImages(&buf, sampleInfos(), false, "{{.Ref}}\t{{.Layers}}层"); err != nil {
		t.Fatal(err)
	}
	want := "alice/myapp:v1\t3层\nalice/base:v1\t1层\n"
	if buf.String() != want {
		t.Fatalf("format 输出 = %q，期望 %q", buf.String(), want)
	}
	// 非法模板要报错
	if err := renderImages(&bytes.Buffer{}, sampleInfos(), false, "{{.Broken"); err == nil {
		t.Fatal("非法模板应报错")
	}
}

func TestRenderImagesEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderImages(&buf, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "暂无本地镜像") {
		t.Fatalf("空 store 提示语缺失: %q", buf.String())
	}
	// 机器可读模式：空列表无输出
	buf.Reset()
	if err := renderImages(&buf, nil, true, ""); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("quiet 空列表应无输出: %q", buf.String())
	}
	buf.Reset()
	if err := renderImages(&buf, nil, false, "{{.Ref}}"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("format 空列表应无输出: %q", buf.String())
	}
}

func TestSplitRefAndHumanBytes(t *testing.T) {
	if r, tg := splitRef("alice/myapp:v1"); r != "alice/myapp" || tg != "v1" {
		t.Fatalf("splitRef: %s %s", r, tg)
	}
	if r, tg := splitRef("nocolon"); r != "nocolon" || tg != "" {
		t.Fatalf("splitRef 无冒号: %s %s", r, tg)
	}
	cases := map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 KB", 12_300_000: "12.3 MB", 2_400_000_000: "2.4 GB"}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %s，期望 %s", in, got, want)
		}
	}
}

func TestImagesCommandEmptyStore(t *testing.T) {
	// 端到端：空数据目录退出码 0 且给出友好提示。
	root := t.TempDir()
	t.Setenv("BOXLI_HOME", root)
	cmd := newImagesCommand(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("空 store 应成功: %v", err)
	}
	quiet := newImagesCommand(&bytes.Buffer{})
	quiet.SetArgs([]string{"--quiet"})
	if err := quiet.Execute(); err != nil {
		t.Fatalf("空 store --quiet 应成功: %v", err)
	}
}
