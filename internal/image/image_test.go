// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildLayer 构造一个 tar.gz 层，返回内容与 sha256 摘要。
func buildLayer(t *testing.T, files map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写层条目失败: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("写层内容失败: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭层 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭层 gzip 失败: %v", err)
	}
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	return data, "sha256:" + hex.EncodeToString(sum[:])
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildConfig 构造合法 config blob 内容。
func buildConfig(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(&Config{
		Entrypoint: []string{"/bin/sh"},
		Env:        map[string]string{"LANG": "C.UTF-8"},
		WorkingDir: "/app",
	})
	if err != nil {
		t.Fatalf("序列化 config 失败: %v", err)
	}
	return b
}

// manifestBuilder 按测试给出的层与 config 生成清单 JSON（os/arch 取当前宿主）。
func manifestBuilder(t *testing.T, layers []Layer, cfgData []byte) []byte {
	t.Helper()
	m := Manifest{
		MediaType:     MediaTypeManifest,
		SpecVersion:   SpecVersionV1,
		SchemaVersion: SchemaVersionV1,
		Architecture:  runtime.GOARCH,
		OS:            runtime.GOOS,
		Created:       "2026-10-01T08:00:00Z",
		Name:          "alice/myapp",
		Version:       "1.0.0",
		Config:        ConfigRef{Digest: digestOf(cfgData), SizeBytes: int64(len(cfgData))},
		Layers:        layers,
	}
	b, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("序列化清单失败: %v", err)
	}
	return b
}

// buildLiCore 在 dir 下写一个 .licore 外层 tar 文件。
func buildLiCore(t *testing.T, dir, name string, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for ename, data := range entries {
		hdr := &tar.Header{Name: ename, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// fullValid 生成一个完全合法的 .licore 文件路径。
func fullValid(t *testing.T) string {
	t.Helper()
	layerData, layerDigest := buildLayer(t, map[string]string{"etc/hello": "hi"})
	cfg := buildConfig(t)
	layers := []Layer{{Path: "layers/000001.base.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)), ApplyOrder: 1}}
	idx := manifestBuilder(t, layers, cfg)
	algo, hx, _ := strings.Cut(digestOf(cfg), ":")
	return buildLiCore(t, t.TempDir(), "test.licore", map[string][]byte{
		IndexName:                   idx,
		"layers/000001.base.tar.gz": layerData,
		BlobsDir + algo + "-" + hx:  cfg,
	})
}

func TestOpenFileAndVerify(t *testing.T) {
	path := fullValid(t)
	loaded, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile 失败: %v", err)
	}
	if loaded.Config == nil || len(loaded.Config.Entrypoint) != 1 || loaded.Config.Entrypoint[0] != "/bin/sh" {
		t.Fatalf("Config 解析异常: %+v", loaded.Config)
	}
	if err := loaded.VerifyLayers(); err != nil {
		t.Fatalf("VerifyLayers 失败: %v", err)
	}
	if err := loaded.CheckPlatform(); err != nil {
		t.Fatalf("CheckPlatform 失败: %v", err)
	}
	if loaded.Manifest.Ref() != "alice/myapp:1.0.0" {
		t.Fatalf("Ref() = %q", loaded.Manifest.Ref())
	}
}

func TestOpenFileRejectsTamperedLayer(t *testing.T) {
	layerData, layerDigest := buildLayer(t, map[string]string{"a": "aaa"})
	cfg := buildConfig(t)
	layers := []Layer{{Path: "layers/000001.base.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)), ApplyOrder: 1}}
	idx := manifestBuilder(t, layers, cfg)
	tampered := append([]byte{}, layerData...)
	tampered[len(tampered)-3] ^= 0xFF // 保持大小不变，破坏内容
	algo, hx, _ := strings.Cut(digestOf(cfg), ":")
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{
		IndexName:                   idx,
		"layers/000001.base.tar.gz": tampered,
		BlobsDir + algo + "-" + hx:  cfg,
	})
	loaded, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile 失败: %v", err)
	}
	if err := loaded.VerifyLayers(); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("VerifyLayers err = %v, want ErrDigestMismatch", err)
	}
}

func TestOpenFileMissingIndex(t *testing.T) {
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{"layers/x": []byte("x")})
	if _, err := OpenFile(path); !errors.Is(err, ErrBadManifest) {
		t.Fatalf("err = %v, want ErrBadManifest", err)
	}
}

func TestOpenFileLayerMissing(t *testing.T) {
	layerData, layerDigest := buildLayer(t, map[string]string{"a": "aaa"})
	cfg := buildConfig(t)
	layers := []Layer{
		{Path: "layers/000001.base.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)), ApplyOrder: 1},
		{Path: "layers/000002.ghost.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)), ApplyOrder: 2},
	}
	idx := manifestBuilder(t, layers, cfg)
	algo, hx, _ := strings.Cut(digestOf(cfg), ":")
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{
		IndexName:                   idx,
		"layers/000001.base.tar.gz": layerData,
		BlobsDir + algo + "-" + hx:  cfg,
	})
	if _, err := OpenFile(path); !errors.Is(err, ErrLayerMissing) {
		t.Fatalf("err = %v, want ErrLayerMissing", err)
	}
}

func TestOpenFileSizeMismatch(t *testing.T) {
	layerData, layerDigest := buildLayer(t, map[string]string{"a": "aaa"})
	cfg := buildConfig(t)
	layers := []Layer{{Path: "layers/000001.base.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)) + 1, ApplyOrder: 1}}
	idx := manifestBuilder(t, layers, cfg)
	algo, hx, _ := strings.Cut(digestOf(cfg), ":")
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{
		IndexName:                   idx,
		"layers/000001.base.tar.gz": layerData,
		BlobsDir + algo + "-" + hx:  cfg,
	})
	if _, err := OpenFile(path); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("err = %v, want ErrSizeMismatch", err)
	}
}

func TestOpenFileConfigBlobMissing(t *testing.T) {
	layerData, layerDigest := buildLayer(t, map[string]string{"a": "aaa"})
	cfg := buildConfig(t)
	layers := []Layer{{Path: "layers/000001.base.tar.gz", Digest: layerDigest, SizeBytes: int64(len(layerData)), ApplyOrder: 1}}
	idx := manifestBuilder(t, layers, cfg)
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{
		IndexName:                   idx,
		"layers/000001.base.tar.gz": layerData, // 故意不放 config blob
	})
	if _, err := OpenFile(path); !errors.Is(err, ErrConfigMissing) {
		t.Fatalf("err = %v, want ErrConfigMissing", err)
	}
}

func TestOpenFileUnsafeEntry(t *testing.T) {
	path := buildLiCore(t, t.TempDir(), "t.licore", map[string][]byte{"../evil": []byte("x")})
	if _, err := OpenFile(path); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("err = %v, want ErrUnsafePath", err)
	}
}

func TestParseManifestRejects(t *testing.T) {
	good := digestOf([]byte("cfg"))
	base := func(mut func(map[string]any)) []byte {
		m := map[string]any{
			"mediaType":     MediaTypeManifest,
			"specVersion":   SpecVersionV1,
			"schemaVersion": float64(SchemaVersionV1),
			"architecture":  "amd64",
			"os":            "linux",
			"created":       "2026-10-01T08:00:00Z",
			"name":          "alice/myapp",
			"version":       "1.0.0",
			"config":        map[string]any{"digest": good, "sizeBytes": float64(3)},
			"layers": []any{map[string]any{
				"path": "layers/000001.base.tar.gz", "digest": digestOf([]byte("x")),
				"sizeBytes": float64(10), "applyOrder": float64(1),
			}},
		}
		mut(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cases := []struct {
		name string
		mut  func(map[string]any)
		want error
	}{
		{"bad mediaType", func(m map[string]any) { m["mediaType"] = "application/json" }, ErrBadManifest},
		{"bad specVersion", func(m map[string]any) { m["specVersion"] = "licore/image-spec/v99" }, ErrBadManifest},
		{"bad schemaVersion", func(m map[string]any) { m["schemaVersion"] = float64(9) }, ErrBadManifest},
		{"bad arch", func(m map[string]any) { m["architecture"] = "sparc64" }, ErrBadManifest},
		{"invalid os", func(m map[string]any) { m["os"] = "windows" }, ErrBadManifest},
		{"created not UTC", func(m map[string]any) { m["created"] = "2026-10-01T08:00:00+08:00" }, ErrBadManifest},
		{"uppercase name", func(m map[string]any) { m["name"] = "Alice/MyApp" }, ErrBadManifest},
		{"digest wrong algo", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["digest"] = "md5:" + strings.Repeat("0", 32)
		}, ErrBadManifest},
		{"digest uppercase hex", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["digest"] = "sha256:" + strings.ToUpper(strings.Repeat("a", 64))
		}, ErrBadManifest},
		{"apply order not 1", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["applyOrder"] = float64(2)
		}, ErrBadApplyOrder},
		{"layer path traversal", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["path"] = "layers/../evil.tar.gz"
		}, ErrUnsafePath},
		{"layer path outside layers/", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["path"] = "etc/passwd"
		}, ErrBadManifest},
		{"unknown top-level field", func(m map[string]any) { m["futureField"] = true }, ErrBadManifest},
		{"unknown layer field", func(m map[string]any) {
			m["layers"].([]any)[0].(map[string]any)["future"] = 1
		}, ErrBadManifest},
		{"bad annotation prefix", func(m map[string]any) {
			m["annotations"] = map[string]any{"docker.version": "20.10"}
		}, ErrBadManifest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest(base(tc.mut))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.want)
			}
		})
	}
}

func TestParseConfigRejectsUnknownKey(t *testing.T) {
	if _, err := ParseConfig([]byte(`{"entrypoint":["/x"],"gpuCount":8}`)); !errors.Is(err, ErrBadManifest) {
		t.Fatalf("err = %v, want ErrBadManifest", err)
	}
	c, err := ParseConfig([]byte(`{"cmd":["x"]}`))
	if err != nil || len(c.Cmd) != 1 {
		t.Fatalf("最小合法 config 解析失败: %v", err)
	}
}

func TestSafeArchivePath(t *testing.T) {
	for _, ok := range []string{"a/b.tar.gz", "layers/000001.x.tar.gz", "blobs/sha256-abc"} {
		if err := SafeArchivePath(ok); err != nil {
			t.Errorf("SafeArchivePath(%q) 意外失败: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/abs", "./rel", "../up", "a/../b", "a//b", `a\b`, "a/./b", "/"} {
		if err := SafeArchivePath(bad); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("SafeArchivePath(%q) err = %v, want ErrUnsafePath", bad, err)
		}
	}
}
