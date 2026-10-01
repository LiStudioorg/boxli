// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Blob 是内容寻址存储里的一个对象：摘要即地址，天然全局去重。
type Blob struct {
	// Digest 是 sha256 十六进制小写。
	Digest string
	// Size 是字节数。
	Size int64
}

// BlobStore 是 blob 存储驱动接口（local / 预留 s3）。
type BlobStore interface {
	// Put 写一个 blob；内容摘要与 digest 不符返回 ErrDigestMismatch。
	Put(digest string, r io.Reader, size int64) error
	// Get 打开一个 blob。
	Get(digest string) (io.ReadCloser, int64, error)
	// Has 报告 blob 是否存在。
	Has(digest string) (bool, error)
	// Delete 删除一个 blob（调用方负责先确认无引用）。
	Delete(digest string) error
	// List 列出全部 blob digest。
	List() ([]string, error)
}

// validDigest 校验 64 位十六进制 sha256 摘要。
func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// computeDigest 计算流的 sha256 摘要（返回十六进制）。
func computeDigest(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, fmt.Errorf("计算摘要: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Registry 是逻辑仓库：tag → blob 映射 + 引用计数。
// 数据布局（<root> 是仓库根）：
//
//	<root>/blobs/sha256/<hex>    blob 原始数据（经 BlobStore 驱动）
//	<root>/tags/<name>/<version>.json   tag 元数据（指向 manifest digest）
//	<root>/manifests/<hex>.json         blob 依赖清单（一个镜像 blob 依赖哪些层/配置 blob）
type Registry struct {
	blobs BlobStore
	root  string
}

// NewRegistry 返回基于本地目录的内容寻址仓库。root 为空时沿用
// $BOXLI_HOME → ~/.boxli（复用数据根下的 hub 子树）。
func NewRegistry(root string, blobs BlobStore) (*Registry, error) {
	if blobs == nil {
		r, err := localBlobRoot(root)
		if err != nil {
			return nil, err
		}
		bs, err := NewLocalBlobStore(r)
		if err != nil {
			return nil, err
		}
		blobs = bs
	}
	if root == "" {
		defaultRoot, err := defaultHubRoot()
		if err != nil {
			return nil, err
		}
		root = defaultRoot
	}
	if err := os.MkdirAll(filepath.Join(root, "tags"), 0o755); err != nil {
		return nil, fmt.Errorf("创建仓库目录: %w", err)
	}
	return &Registry{blobs: blobs, root: root}, nil
}

// Root 返回仓库根。
func (r *Registry) Root() string { return r.root }

func (r *Registry) tagsDir() string      { return filepath.Join(r.root, "tags") }
func (r *Registry) manifestsDir() string { return filepath.Join(r.root, "manifests") }

// PutBlob 上传一个 blob（全局去重：已存在则复用）。digest 为空时按内容计算。
func (r *Registry) PutBlob(digest string, data io.Reader, size int64) (*Blob, error) {
	// 一次性读入内存：blob（manifest/镜像）体积可控，且便于重复校验去重与落盘。
	buf, err := io.ReadAll(data)
	if err != nil {
		return nil, fmt.Errorf("读取 blob: %w", err)
	}
	if digest == "" {
		var n int64
		digest, n, err = computeDigest(bytes.NewReader(buf))
		if err != nil {
			return nil, err
		}
		size = n
	} else {
		if !validDigest(digest) {
			return nil, fmt.Errorf("摘要 %q 非法: %w", digest, ErrBadDigest)
		}
	}
	exists, err := r.blobs.Has(digest)
	if err != nil {
		return nil, err
	}
	if exists {
		// 去重：已存在即返回。
		return &Blob{Digest: digest, Size: size}, nil
	}
	if err := r.blobs.Put(digest, bytes.NewReader(buf), size); err != nil {
		if errors.Is(err, ErrDigestMismatch) {
			return nil, err
		}
		return nil, fmt.Errorf("落盘 blob %s: %w", digest, err)
	}
	return &Blob{Digest: digest, Size: size}, nil
}

// GetBlob 打开一个 blob。
func (r *Registry) GetBlob(digest string) (io.ReadCloser, int64, error) {
	if !validDigest(digest) {
		return nil, 0, fmt.Errorf("摘要 %q 非法: %w", digest, ErrBadDigest)
	}
	return r.blobs.Get(digest)
}

// Tag 是把一个 manifest digest 关联到 name:version。manifest 自身也是一个 blob。
// tag 记录的 digest 必须存在。
func (r *Registry) Tag(name, version, digest string) error {
	if err := validateName(name); err != nil {
		return err
	}
	if err := validateVersion(version); err != nil {
		return err
	}
	if !validDigest(digest) {
		return fmt.Errorf("tag 摘要 %q 非法: %w", digest, ErrBadDigest)
	}
	exists, err := r.blobs.Has(digest)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("blob %s 不存在，tag 失效: %w", digest, ErrNotFound)
	}
	tagPath := r.tagPath(name, version)
	// 源 manifest：指向 digest（无依赖则空）。
	_ = r.ensureManifest(digest)
	if err := os.MkdirAll(filepath.Dir(tagPath), 0o755); err != nil {
		return fmt.Errorf("创建 tag 目录: %w", err)
	}
	data, _ := json.MarshalIndent(TagRecord{Name: name, Version: version, Digest: digest}, "", "  ")
	tmp := tagPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写 tag 文件: %w", err)
	}
	return os.Rename(tmp, tagPath)
}

// TagRecord 是 tag 元数据。
type TagRecord struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

// Resolve 查询 tag 指向的 digest；不存在返回 ErrNotFound。
func (r *Registry) Resolve(name, version string) (*TagRecord, error) {
	data, err := os.ReadFile(r.tagPath(name, version))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("tag %s:%s: %w", name, version, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 tag: %w", err)
	}
	var rec TagRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("解析 tag 元数据: %w", err)
	}
	return &rec, nil
}

// ListTags 列出某名字下的全部版本。
func (r *Registry) ListTags(name string) ([]string, error) {
	dir := filepath.Join(r.tagsDir(), name)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 tag 目录: %w", err)
	}
	var out []string
	for _, e := range ents {
		// tag 元数据是 <version>.json 文件。
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}

// DeleteTag 删除 tag 并回收其引用计数归零的 blob。
func (r *Registry) DeleteTag(name, version string) error {
	rec, err := r.Resolve(name, version)
	if err != nil {
		return err
	}
	if err := os.Remove(r.tagPath(name, version)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除 tag: %w", err)
	}
	r.gcIfUnreferenced(rec.Digest)
	return nil
}

// Unreferenced 报告 digest 是否仍被某 tag 引用。
func (r *Registry) Unreferenced(digest string) (bool, error) {
	refs, err := r.References(digest)
	if err != nil {
		return false, err
	}
	return len(refs) == 0, nil
}

// References 返回全部仍引用该 digest 的 tag 描述。
func (r *Registry) References(digest string) ([]TagRecord, error) {
	var out []TagRecord
	names, err := r.allNames()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		versions, _ := r.ListTags(name)
		for _, v := range versions {
			rec, err := r.Resolve(name, v)
			if err != nil {
				continue
			}
			if rec.Digest == digest {
				out = append(out, *rec)
			}
		}
	}
	return out, nil
}

func (r *Registry) allNames() ([]string, error) {
	set := map[string]bool{}
	err := filepath.WalkDir(r.tagsDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipDir
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		// 去掉 tags/ 前缀与版本文件名，剩的即镜像名（可能含 /）。
		rel, rerr := filepath.Rel(r.tagsDir(), p)
		if rerr != nil {
			return nil
		}
		rel = strings.TrimSuffix(filepath.ToSlash(rel), "/"+d.Name())
		if rel != "" {
			set[rel] = true
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("扫描仓库: %w", err)
	}
	var out []string
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Registry) tagPath(name, version string) string {
	return filepath.Join(r.tagsDir(), name, version+".json")
}

// gcIfUnreferenced 若 blob 不再被任何 tag 引用则删除。
func (r *Registry) gcIfUnreferenced(digest string) {
	unref, err := r.Unreferenced(digest)
	if err != nil || !unref {
		return
	}
	if err := r.blobs.Delete(digest); err != nil {
		slog.Debug("回收 blob 失败", "digest", digest[:8], "err", err)
	}
}

// ensureManifest 仅为占位（依赖图维护后续扩展）。
func (r *Registry) ensureManifest(digest string) error { return nil }

// Search 按 name 关键字搜索仓库里的镜像集合（返回 name:latest 或首个版本）。
func (r *Registry) Search(query string) ([]SearchResult, error) {
	names, err := r.allNames()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []SearchResult
	for _, name := range names {
		if q != "" && !strings.Contains(strings.ToLower(name), q) {
			continue
		}
		versions, err := r.ListTags(name)
		if err != nil {
			continue
		}
		for _, v := range versions {
			rec, _ := r.Resolve(name, v)
			out = append(out, SearchResult{Name: name, Version: v, Digest: rec.Digest})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SearchResult 是一次搜索命中。
type SearchResult struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

func validateName(n string) error {
	if n == "" || len(n) > 255 || strings.HasPrefix(n, ".") || strings.Contains(n, "..") {
		return fmt.Errorf("镜像名 %q 非法: %w", n, ErrBadRequest)
	}
	// 名字允许包含 /（namespace/repo 路径），但每段需为非空常规字符。
	for _, seg := range strings.Split(n, "/") {
		if seg == "" || strings.HasPrefix(seg, ".") {
			return fmt.Errorf("镜像名 %q 非法: %w", n, ErrBadRequest)
		}
	}
	return nil
}

func validateVersion(v string) error {
	if v == "" || len(v) > 128 {
		return fmt.Errorf("版本 %q 非法: %w", v, ErrBadRequest)
	}
	return nil
}
