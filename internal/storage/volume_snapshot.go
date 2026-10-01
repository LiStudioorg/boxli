// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Snapshot 把卷的数据复制到 .snapshots/<name>.backup-<ts> 下（只读快照）。
// 快照不注册为可挂载卷，仅作为灾备/回滚源。
func (m *VolumeManager) Snapshot(name string) (string, error) {
	if _, err := m.readMeta(name); err != nil {
		return "", err
	}
	src := m.DataDir(name)
	snapDir := filepath.Join(m.volumesRoot(), ".snapshots")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return "", fmt.Errorf("创建快照目录失败: %w", err)
	}
	ts := time.Now().UTC().Format("20060102T150405")
	dst := filepath.Join(snapDir, name+"."+ts)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", fmt.Errorf("创建快照目录 %q 失败: %w", dst, err)
	}
	if err := copyTree(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// Clone 把现有卷完整复制到一个新卷（数据 + 元数据），返回新卷。
// 保留驱动与配额。
func (m *VolumeManager) Clone(from, to string) (*Volume, error) {
	if err := m.checkName(to); err != nil {
		return nil, err
	}
	if _, err := m.readMeta(to); err == nil {
		return nil, fmt.Errorf("目标卷 %q 已存在: %w", to, ErrVolumeExists)
	}
	src, err := m.readMeta(from)
	if err != nil {
		return nil, err
	}
	newv := &Volume{Name: to, Driver: src.Driver, Size: src.Size, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := m.writeMeta(newv); err != nil {
		return nil, err
	}
	if err := drv(src.Driver).Create(m.DataDir(to), newv); err != nil {
		_ = os.Remove(m.metaPath(to))
		return nil, err
	}
	if err := copyTree(m.DataDir(from), m.DataDir(to)); err != nil {
		_ = os.Remove(m.metaPath(to))
		return nil, err
	}
	// 合并源卷自定义元数据。
	if len(src.Meta) > 0 {
		newv.Meta = src.Meta
		_ = m.writeMeta(newv)
	}
	return newv, nil
}

// copyTree 递归复制 src → dst（保留目录结构，普通文件与符号链接；跳过套接字/FIFO/设备）。
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil || rel == "." {
			return rerr
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("复制目录 %q: %w", rel, err)
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			link, lerr := os.Readlink(p)
			if lerr != nil {
				return fmt.Errorf("读取符号链接 %q: %w", rel, lerr)
			}
			if err := os.Symlink(link, target); err != nil {
				return fmt.Errorf("复制符号链接 %q: %w", rel, err)
			}
			return nil
		case d.Type().IsRegular():
			return copyFile(p, target)
		default:
			// 跳过设备/FIFO/套接字。
			return nil
		}
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开 %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	fi, err := in.Stat()
	if err != nil {
		return fmt.Errorf("读取 %q 信息: %w", src, err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return fmt.Errorf("创建 %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("复制 %q→%q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("关闭 %q: %w", dst, err)
	}
	return nil
}
