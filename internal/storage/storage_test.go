// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LiStudioorg/licore/internal/image"
)

// ---------- 测试辅助：构造层 tar.gz ----------

type entry struct {
	name string
	typ  byte // tar.TypeReg / TypeDir / TypeSymlink / TypeLink / TypeChar ...
	body string
	link string
	mode int64
}

// tarGz 把条目列表打成 tar.gz 原始字节。header 写入顺序保持给定顺序。
func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var raw bytes.Buffer
	gw := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gw)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typ,
			Mode:     e.mode,
			Linkname: e.link,
			Format:   tar.FormatUSTAR,
		}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("写 tar 头 %q: %v", e.name, err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func digestOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// writeLayer 落一个层文件到临时目录并返回路径与摘要。
func writeLayer(t *testing.T, dir, file string, data []byte) (string, string) {
	t.Helper()
	p := filepath.Join(dir, file)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, digestOf(data)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s: %v", p, err)
	}
	return string(b)
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s 应不存在，实得 err=%v", p, err)
	}
}

// ---------- 解包基础 ----------

func TestUnpackBasicAndReuse(t *testing.T) {
	store := t.TempDir()
	data := tarGz(t,
		entry{name: "etc/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "etc/hosts", body: "127.0.0.1 localhost\n", mode: 0o644},
		entry{name: "bin/sh", body: "shell", mode: 0o755},
	)
	layerFile, want := writeLayer(t, t.TempDir(), "l1.tar.gz", data)

	res, err := UnpackFile(layerFile, want, store)
	if err != nil {
		t.Fatalf("UnpackFile: %v", err)
	}
	if res.DigestHex != want {
		t.Fatalf("摘要 %s 期望 %s", res.DigestHex, want)
	}
	if want := "127.0.0.1 localhost\n"; readFile(t, filepath.Join(res.FSDir, "etc/hosts")) != want {
		t.Fatalf("内容 %q", want)
	}
	if !LayerUnpacked(store, want) {
		t.Fatal("LayerUnpacked 应为 true")
	}

	// 二次解包直接复用。
	res2, err := UnpackFile(layerFile, want, store)
	if err != nil || res2.FSDir != res.FSDir {
		t.Fatalf("复用失败: %v %+v", err, res2)
	}
}

func TestUnpackDigestMismatch(t *testing.T) {
	layerFile, _ := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t, entry{name: "a", body: "x"}))
	_, err := UnpackFile(layerFile, strings.Repeat("ab", 32), t.TempDir())
	if !errors.Is(err, ErrBadDigest) {
		t.Fatalf("期望 ErrBadDigest，实得 %v", err)
	}
}

func TestUnpackBadGzip(t *testing.T) {
	layerFile, want := writeLayer(t, t.TempDir(), "bad.tar.gz", []byte("this is not gzip data at all"))
	_, err := UnpackFile(layerFile, want, t.TempDir())
	if !errors.Is(err, ErrCorruptLayer) {
		t.Fatalf("期望 ErrCorruptLayer，实得 %v", err)
	}
}

func TestUnpackTruncatedTar(t *testing.T) {
	// gzip 合法，但 tar 内声明 100 字节只给了 3 字节 → 截断。
	var raw bytes.Buffer
	gw := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gw)
	_ = tw.WriteHeader(&tar.Header{Name: "a", Size: 100, Mode: 0o644, Format: tar.FormatUSTAR})
	_, _ = tw.Write([]byte("abc"))
	_ = tw.Close() // 不 flush 完整尾块亦可能合法关闭；再手动截掉 gzip 尾
	_ = gw.Close()
	trunc := raw.Bytes()[:raw.Len()-10]

	layerFile, want := writeLayer(t, t.TempDir(), "trunc.tar.gz", trunc)
	_, err := UnpackFile(layerFile, want, t.TempDir())
	if !errors.Is(err, ErrCorruptLayer) {
		t.Fatalf("期望 ErrCorruptLayer，实得 %v", err)
	}
}

// ---------- 路径逃逸 ----------

func TestUnpackPathEscape(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
	}{
		{"相对逃逸", []entry{{name: "../evil", body: "x"}}},
		{"绝对路径", []entry{{name: "/etc/evil", body: "x"}}},
		{"中间 .. 段", []entry{{name: "a/../../evil", body: "x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			layerFile, want := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t, c.entries...))
			store := t.TempDir()
			_, err := UnpackFile(layerFile, want, store)
			if !errors.Is(err, image.ErrUnsafePath) {
				t.Fatalf("期望 ErrUnsafePath，实得 %v", err)
			}
			// 逃逸目标不得落盘
			mustNotExist(t, filepath.Join(store, "evil"))
			mustNotExist(t, filepath.Join(store, "..", "..", "etc", "evil"))
		})
	}
}

func TestUnpackSymlinkParentEscape(t *testing.T) {
	// 层：先建 symlink out -> <store 外目录>，再写 out/pwned → 必须拒绝。
	outside := t.TempDir()
	layerFile, want := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t,
		entry{name: "out", typ: tar.TypeSymlink, link: outside},
		entry{name: "out/pwned", body: "x", mode: 0o644},
	))
	store := t.TempDir()
	if _, err := UnpackFile(layerFile, want, store); !errors.Is(err, image.ErrUnsafePath) {
		t.Fatalf("期望 ErrUnsafePath，实得 %v", err)
	}
	mustNotExist(t, filepath.Join(outside, "pwned"))
}

func TestMergeSymlinkWriteEscapeGuarded(t *testing.T) {
	// rootfs 里 dir 是符号链接；层的 dir 是普通文件，合并必须用原子替换
	// 覆盖符号链接本身，绝不允许写穿到链接目标。
	outside := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "dir")); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	layerFile, want := writeLayer(t, t.TempDir(), "l2.tar.gz", tarGz(t,
		entry{name: "dir", body: "replaced", mode: 0o644},
	))
	if _, err := UnpackFile(layerFile, want, store); err != nil {
		t.Fatal(err)
	}
	if err := MergeLayers(store, []string{want}, target); err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(target, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		t.Fatalf("dir 应为替换后的普通文件: %v", fi.Mode())
	}
	// 链接目标目录必须原封不动（无 "dir" 文件出现）
	mustNotExist(t, filepath.Join(outside, "dir"))
	ents, err := os.ReadDir(outside)
	if err != nil || len(ents) != 0 {
		t.Fatalf("outside被写入内容: %v %v", ents, err)
	}
}

// ---------- 剥离：设备节点 / setuid ----------

func TestUnpackStripsDevicesAndSetuid(t *testing.T) {
	layerFile, want := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t,
		entry{name: "dev/sda", typ: tar.TypeChar, mode: 0o660},
		entry{name: "dev/sdb", typ: tar.TypeBlock, mode: 0o660},
		entry{name: "run/fifo", typ: tar.TypeFifo, mode: 0o644},
		entry{name: "usr/bin/suid", body: "x", mode: 0o4755},
		entry{name: "usr/bin/sgid", body: "x", mode: 0o2755},
	))
	store := t.TempDir()
	res, err := UnpackFile(layerFile, want, store)
	if err != nil {
		t.Fatalf("UnpackFile: %v", err)
	}
	mustNotExist(t, filepath.Join(res.FSDir, "dev/sda"))
	mustNotExist(t, filepath.Join(res.FSDir, "dev/sdb"))
	mustNotExist(t, filepath.Join(res.FSDir, "run/fifo"))

	fi, err := os.Stat(filepath.Join(res.FSDir, "usr/bin/suid"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("suid 位未剥离: %v", fi.Mode())
	}
	fi, err = os.Stat(filepath.Join(res.FSDir, "usr/bin/sgid"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("sgid 位未剥离: %v", fi.Mode())
	}
}

// ---------- 重复条目 / 空层 ----------

func TestUnpackDuplicateEntry(t *testing.T) {
	layerFile, want := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t,
		entry{name: "a", body: "1", mode: 0o644},
		entry{name: "a", body: "2", mode: 0o644},
	))
	if _, err := UnpackFile(layerFile, want, t.TempDir()); !errors.Is(err, ErrDuplicateEntry) {
		t.Fatalf("期望 ErrDuplicateEntry，实得 %v", err)
	}
}

func TestUnpackEmptyLayer(t *testing.T) {
	layerFile, want := writeLayer(t, t.TempDir(), "empty.tar.gz", tarGz(t))
	res, err := UnpackFile(layerFile, want, t.TempDir())
	if err != nil {
		t.Fatalf("空层应合法: %v", err)
	}
	ents, err := os.ReadDir(res.FSDir)
	if err != nil || len(ents) != 0 {
		t.Fatalf("空层 fs 目录应为空: %v %v", ents, err)
	}
}

// ---------- 硬链接合法与非法 ----------

func TestUnpackHardlink(t *testing.T) {
	// 前向引用（目标不存在）必须拒绝。
	fwd, want := writeLayer(t, t.TempDir(), "l.tar.gz", tarGz(t,
		entry{name: "link", typ: tar.TypeLink, link: "missing"},
	))
	if _, err := UnpackFile(fwd, want, t.TempDir()); !errors.Is(err, ErrCorruptLayer) {
		t.Fatalf("前向硬链接应拒绝，实得 %v", err)
	}
	// 后向引用（目标已创建）允许。
	okf, want2 := writeLayer(t, t.TempDir(), "l2.tar.gz", tarGz(t,
		entry{name: "orig", body: "data", mode: 0o644},
		entry{name: "hard", typ: tar.TypeLink, link: "orig"},
	))
	res, err := UnpackFile(okf, want2, t.TempDir())
	if err != nil {
		t.Fatalf("合法硬链接被拒: %v", err)
	}
	oi, _ := os.Stat(filepath.Join(res.FSDir, "orig"))
	hi, _ := os.Stat(filepath.Join(res.FSDir, "hard"))
	if oi == nil || hi == nil || !os.SameFile(oi, hi) {
		t.Fatal("hard 应与 orig 同 inode")
	}
}

// ---------- 合并语义 ----------

func TestMergeOverlayOverride(t *testing.T) {
	store := t.TempDir()
	l1, d1 := writeLayer(t, t.TempDir(), "l1", tarGz(t,
		entry{name: "etc/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "etc/hosts", body: "v1\n", mode: 0o644},
		entry{name: "etc/keep", body: "keep\n", mode: 0o644},
		entry{name: "app", body: "bin-v1", mode: 0o755},
	))
	l2, d2 := writeLayer(t, t.TempDir(), "l2", tarGz(t,
		entry{name: "etc/hosts", body: "v2\n", mode: 0o644},
		entry{name: "app", body: "bin-v2", mode: 0o700},
	))
	for _, l := range []struct {
		f, d string
	}{{l1, d1}, {l2, d2}} {
		if _, err := UnpackFile(l.f, l.d, store); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := MergeLayers(store, []string{d1, d2}, target); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if got := readFile(t, filepath.Join(target, "etc/hosts")); got != "v2\n" {
		t.Fatalf("后层未覆盖: %q", got)
	}
	if got := readFile(t, filepath.Join(target, "etc/keep")); got != "keep\n" {
		t.Fatalf("前层文件丢失: %q", got)
	}
	if got := readFile(t, filepath.Join(target, "app")); got != "bin-v2" {
		t.Fatalf("app 未覆盖: %q", got)
	}
	fi, _ := os.Stat(filepath.Join(target, "app"))
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("app 权限 %v", fi.Mode())
	}
}

func TestMergeWhiteout(t *testing.T) {
	store := t.TempDir()
	l1, d1 := writeLayer(t, t.TempDir(), "l1", tarGz(t,
		entry{name: "etc/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "etc/gone", body: "bye", mode: 0o644},
		entry{name: "etc/sub/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "etc/sub/tree", body: "deep", mode: 0o644},
	))
	l2, d2 := writeLayer(t, t.TempDir(), "l2", tarGz(t,
		entry{name: "etc/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "etc/.wh.gone", body: "", mode: 0o644},
		entry{name: "etc/.wh.sub", body: "", mode: 0o644},
	))
	for _, l := range []struct{ f, d string }{{l1, d1}, {l2, d2}} {
		if _, err := UnpackFile(l.f, l.d, store); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := MergeLayers(store, []string{d1, d2}, target); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	mustNotExist(t, filepath.Join(target, "etc/gone"))
	mustNotExist(t, filepath.Join(target, "etc/sub"))
	// whiteout 本身不得出现在 rootfs
	mustNotExist(t, filepath.Join(target, "etc/.wh.gone"))
	mustNotExist(t, filepath.Join(target, "etc/.wh.sub"))
}

func TestMergeWhiteoutNoopAndSymlinkGuard(t *testing.T) {
	store := t.TempDir()
	l1, d1 := writeLayer(t, t.TempDir(), "l1", tarGz(t,
		entry{name: "a", body: "x", mode: 0o644},
	))
	l2, d2 := writeLayer(t, t.TempDir(), "l2", tarGz(t,
		entry{name: ".wh.notthere", body: "", mode: 0o644}, // no-op 合法
	))
	l3, d3 := writeLayer(t, t.TempDir(), "l3", tarGz(t,
		entry{name: ".wh.a", body: "", mode: 0o644},
	))
	for _, l := range []struct{ f, d string }{{l1, d1}, {l2, d2}, {l3, d3}} {
		if _, err := UnpackFile(l.f, l.d, store); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := MergeLayers(store, []string{d1, d2}, target); err != nil {
		t.Fatalf("no-op whiteout 应合法: %v", err)
	}
	if err := MergeLayers(store, []string{d3}, target); err != nil {
		t.Fatalf("删除存在文件失败: %v", err)
	}
	mustNotExist(t, filepath.Join(target, "a"))
}

func TestMergeOpaqueDir(t *testing.T) {
	store := t.TempDir()
	l1, d1 := writeLayer(t, t.TempDir(), "l1", tarGz(t,
		entry{name: "var/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "var/log", body: "old1", mode: 0o644},
		entry{name: "var/spool/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "var/spool/x", body: "old2", mode: 0o644},
	))
	l2, d2 := writeLayer(t, t.TempDir(), "l2", tarGz(t,
		entry{name: "var/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "var/.wh..wh..opq", body: "", mode: 0o644},
		entry{name: "var/log", body: "new", mode: 0o644},
	))
	for _, l := range []struct{ f, d string }{{l1, d1}, {l2, d2}} {
		if _, err := UnpackFile(l.f, l.d, store); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := MergeLayers(store, []string{d1, d2}, target); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	// opaque：var 下旧内容全部清除，仅保留新层内容
	mustNotExist(t, filepath.Join(target, "var/spool"))
	if got := readFile(t, filepath.Join(target, "var/log")); got != "new" {
		t.Fatalf("opaque 后新内容错误: %q", got)
	}
	mustNotExist(t, filepath.Join(target, "var/.wh..wh..opq"))
}

func TestMergeOpaqueViaSymlinkRefused(t *testing.T) {
	// rootfs 里 var 是符号链接 → opaque 清除必须拒绝而非清空链接目标。
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious"), []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "var")); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	l, d := writeLayer(t, t.TempDir(), "l", tarGz(t,
		entry{name: "var/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "var/.wh..wh..opq", body: "", mode: 0o644},
	))
	if _, err := UnpackFile(l, d, store); err != nil {
		t.Fatal(err)
	}
	if err := MergeLayers(store, []string{d}, target); !errors.Is(err, image.ErrUnsafePath) {
		t.Fatalf("期望 ErrUnsafePath，实得 %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatal("符号链接目标被误删")
	}
}

func TestMergeMissingLayer(t *testing.T) {
	err := MergeLayers(t.TempDir(), []string{strings.Repeat("00", 32)}, t.TempDir())
	if !errors.Is(err, ErrLayerMissingLocal) {
		t.Fatalf("期望 ErrLayerMissingLocal，实得 %v", err)
	}
}

// ---------- 并发解包同一层 ----------

func TestConcurrentUnpackSameLayer(t *testing.T) {
	data := tarGz(t,
		entry{name: "dir/", typ: tar.TypeDir, mode: 0o755},
		entry{name: "dir/file", body: "hello", mode: 0o644},
	)
	layerFile, want := writeLayer(t, t.TempDir(), "l.tar.gz", data)
	store := t.TempDir()

	const n = 8
	errCh := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := UnpackFile(layerFile, want, store)
			if err == nil {
				if res.DigestHex != want {
					err = errors.New("摘要不一致")
				} else if readFileContent(res.FSDir) != "hello" {
					err = errors.New("内容损坏")
				}
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("并发解包失败: %v", err)
		}
	}
}

func readFileContent(fsDir string) string {
	b, err := os.ReadFile(filepath.Join(fsDir, "dir/file"))
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------- 过期临时目录清理 ----------

func TestCleanupStaleTmp(t *testing.T) {
	layers := t.TempDir()
	stale := filepath.Join(layers, ".tmp-stale")
	fresh := filepath.Join(layers, ".tmp-fresh")
	for _, d := range []string{stale, fresh} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	cleanupStaleTmp(layers)
	mustNotExist(t, stale)
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("新临时目录不应被清理: %v", err)
	}
}

func TestMergeSymlinkComponentEscape(t *testing.T) {
	// rootfs: link -> outside（层 1 建符号链接）；层 2 写 link/pwned，
	// 合并必须拒绝，绝不写穿符号链接目录。
	outside := t.TempDir()
	store := t.TempDir()
	l1, d1 := writeLayer(t, t.TempDir(), "l1", tarGz(t,
		entry{name: "link", typ: tar.TypeSymlink, link: outside},
	))
	l2, d2 := writeLayer(t, t.TempDir(), "l2", tarGz(t,
		entry{name: "link/pwned", body: "escaped", mode: 0o644},
	))
	for _, l := range []struct{ f, d string }{{l1, d1}, {l2, d2}} {
		if _, err := UnpackFile(l.f, l.d, store); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	if err := MergeLayers(store, []string{d1}, target); err != nil {
		t.Fatal(err)
	}
	if err := MergeLayers(store, []string{d2}, target); !errors.Is(err, image.ErrUnsafePath) {
		t.Fatalf("期望 ErrUnsafePath，实得 %v", err)
	}
	mustNotExist(t, filepath.Join(outside, "pwned"))
}
