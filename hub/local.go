// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// localBlobStore 把 blob 落到 <root>/blobs/sha256/<hex>。
type localBlobStore struct {
	root string
}

// NewLocalBlobStore 返回本地文件系统 blob 驱动。
func NewLocalBlobStore(blobRoot string) (*localBlobStore, error) {
	if blobRoot == "" {
		var err error
		blobRoot, err = defaultBlobRoot()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(blobRoot, 0o755); err != nil {
		return nil, fmt.Errorf("创建 blob 根目录: %w", err)
	}
	return &localBlobStore{root: blobRoot}, nil
}

func (l *localBlobStore) blobPath(digest string) string {
	return filepath.Join(l.root, "sha256", digest)
}

func (l *localBlobStore) Put(digest string, r io.Reader, size int64) error {
	if !validDigest(digest) {
		return fmt.Errorf("摘要 %q 非法: %w", digest, ErrBadDigest)
	}
	dir := filepath.Dir(l.blobPath(digest))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 sha256 目录: %w", err)
	}
	tmp := l.blobPath(digest) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("打开临时 blob: %w", err)
	}
	// 边写边算摘要，最后与实际 digest 比对。
	dig, n, err := computeDigest(io.TeeReader(r, f))
	if err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if size >= 0 && n != size {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("blob 长度 %d != 声明 %d: %w", n, size, ErrBadRequest)
	}
	if dig != digest {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("blob 摘要 %s != 声明 %s: %w", dig, digest, ErrDigestMismatch)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭 blob: %w", err)
	}
	if err := os.Rename(tmp, l.blobPath(digest)); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落位 blob: %w", err)
	}
	return nil
}

func (l *localBlobStore) Get(digest string) (io.ReadCloser, int64, error) {
	p := l.blobPath(digest)
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, fmt.Errorf("blob %s: %w", digest, ErrNotFound)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("打开 blob: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, fmt.Errorf("读取 blob 信息: %w", err)
	}
	return f, fi.Size(), nil
}

func (l *localBlobStore) Has(digest string) (bool, error) {
	_, err := os.Stat(l.blobPath(digest))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("检查 blob: %w", err)
	}
}

func (l *localBlobStore) Delete(digest string) error {
	if err := os.Remove(l.blobPath(digest)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除 blob: %w", err)
	}
	return nil
}

func (l *localBlobStore) List() ([]string, error) {
	shaDir := filepath.Join(l.root, "sha256")
	ents, err := os.ReadDir(shaDir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描 sha256 目录: %w", err)
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && validDigest(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Root 返回 blob 根。
func (l *localBlobStore) Root() string { return l.root }

// 目录确定辅助。
func defaultHubRoot() (string, error) {
	return defaultDataRoot()
}

func localBlobRoot(dataRoot string) (string, error) {
	if dataRoot == "" {
		return defaultBlobRoot()
	}
	return filepath.Join(dataRoot, "hub", "blobs"), nil
}

func defaultBlobRoot() (string, error) {
	root, err := defaultDataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "hub", "blobs"), nil
}

func defaultDataRoot() (string, error) {
	root := os.Getenv("LICORE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("确定数据目录失败: %w", err)
		}
		root = filepath.Join(home, ".licore")
	}
	return root, nil
}
