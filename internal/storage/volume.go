// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VolumeDriver 是卷驱动的接口。当前内置 local / tmpfs；其他驱动（nfs 等）
// 预留实现点，登记后即可接入。方法接收数据目录（<root>/volumes/<name>）。
type VolumeDriver interface {
	// Name 返回驱动名。
	Name() string
	// Create 准备卷的数据目录（含配额等）。
	Create(dataDir string, v *Volume) error
	// Remove 回收卷的数据。
	Remove(dataDir string, v *Volume) error
	// Mountpoint 返回该驱动的数据绝对路径（Create 后有效）。
	Mountpoint(dataDir string, v *Volume) string
}

// Volume 是一个命名的持久卷。
type Volume struct {
	// Name 是卷名（数据目录内唯一）。
	Name string `json:"name"`
	// Driver 是驱动类型（local / tmpfs）。
	Driver string `json:"driver"`
	// Size 是配额字节数；0 表示不限（-size flag 的落地）。
	Size int64 `json:"size,omitempty"`
	// CreatedAt 是创建时间（UTC RFC 3339）。
	CreatedAt string `json:"createdAt"`
	// Meta 是驱动自定义元数据。
	Meta map[string]string `json:"meta,omitempty"`
}

// Valid 报告卷字段是否合法。
func (v *Volume) Valid() bool {
	if v.Name == "" || strings.ContainsAny(v.Name, `/\`) || strings.HasPrefix(v.Name, ".") {
		return false
	}
	switch v.Driver {
	case DriverLocal, DriverTmpfs:
		return true
	}
	return false
}

// VolumeManager 提供卷的生命周期管理并把元数据持久化在 <root>/volumes/ 下。
// 数据目录与元数据分离：<root>/volumes/<name>/ 是数据，<root>/volumes/.meta/<name>.json 是元数据。
type VolumeManager struct {
	root string // 数据根
}

// ErrVolumeData is a sentinel-less helper placeholder.
var (
	// ErrVolumeExists 表示同名卷已存在。
	ErrVolumeExists = errors.New("licore/storage: 同名卷已存在")
	// ErrVolumeNotFound 表示按名字找不到卷。
	ErrVolumeNotFound = errors.New("licore/storage: 卷不存在")
	// ErrVolumeInUse 表示卷正被容器引用，不能删除。
	ErrVolumeInUse = errors.New("licore/storage: 卷正在被使用")
	// ErrVolumeBad 表示卷定义非法。
	ErrVolumeBad = errors.New("licore/storage: 卷定义非法")
	// ErrBadDriver 表示驱动不受支持。
	ErrBadDriver = errors.New("licore/storage: 卷驱动不受支持")
)

// NewVolumeManager 返回卷管理器。root 为空沿用 $LICORE_HOME → ~/.licore。
func NewVolumeManager(root string) (*VolumeManager, error) {
	if root == "" {
		r, err := defaultStoreRootV()
		if err != nil {
			return nil, err
		}
		root = r
	}
	return &VolumeManager{root: root}, nil
}

// Root 返回数据根。
func (m *VolumeManager) Root() string { return m.root }

// volumesRoot 返回卷根目录。
func (m *VolumeManager) volumesRoot() string { return filepath.Join(m.root, "volumes") }

// metaRoot 返回卷元数据目录。
func (m *VolumeManager) metaRoot() string { return filepath.Join(m.volumesRoot(), ".meta") }

// DataDir 返回某卷的数据目录 <root>/volumes/<name>。
func (m *VolumeManager) DataDir(name string) string { return filepath.Join(m.volumesRoot(), name) }

func (m *VolumeManager) metaPath(name string) string {
	return filepath.Join(m.metaRoot(), name+".json")
}

// Create 创建命名卷。
func (m *VolumeManager) Create(name, driver string, size int64) (*Volume, error) {
	if err := m.checkName(name); err != nil {
		return nil, err
	}
	if err := checkDriver(driver); err != nil {
		return nil, err
	}
	if _, err := m.readMeta(name); err == nil {
		return nil, fmt.Errorf("卷 %q 已存在: %w", name, ErrVolumeExists)
	}
	if size < 0 {
		return nil, fmt.Errorf("卷大小不能为负: %w", ErrVolumeBad)
	}
	v := &Volume{Name: name, Driver: driver, Size: size, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := m.writeMeta(v); err != nil {
		return nil, err
	}
	if err := drv(driver).Create(m.DataDir(name), v); err != nil {
		_ = os.Remove(m.DataDir(name))
		_ = os.Remove(m.metaPath(name))
		return nil, err
	}
	return v, nil
}

// List 返回全部卷，按名字排序。损坏元数据跳过并告警。
func (m *VolumeManager) List() ([]*Volume, error) {
	ents, err := os.ReadDir(m.metaRoot())
	if errors.Is(err, os.ErrNotExist) {
		return []*Volume{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("扫描卷元数据失败: %w", err)
	}
	out := []*Volume{}
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		v, err := m.readMeta(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			slog.Warn("跳过损坏的卷元数据", "file", e.Name(), "err", err)
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Inspect 返回单个卷。
func (m *VolumeManager) Inspect(name string) (*Volume, error) { return m.readMeta(name) }

// Remove 删除卷（数据 + 元数据）。
func (m *VolumeManager) Remove(name string) error {
	v, err := m.readMeta(name)
	if err != nil {
		return err
	}
	if err := drv(v.Driver).Remove(m.DataDir(name), v); err != nil {
		return err
	}
	if err := os.Remove(m.metaPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除卷元数据失败: %w", err)
	}
	return nil
}

// Prune 删除所有未使用的卷，返回删除数量。
func (m *VolumeManager) Prune() (int, error) {
	all, err := m.List()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, v := range all {
		if err := m.Remove(v.Name); err != nil {
			slog.Debug("Prune 跳过卷", "vol", v.Name, "err", err)
			continue
		}
		removed++
	}
	return removed, nil
}

// Mountpoint 返回卷的数据挂载点（与驱动实现一致）。
func (m *VolumeManager) Mountpoint(name string) (string, error) {
	v, err := m.readMeta(name)
	if err != nil {
		return "", err
	}
	return drv(v.Driver).Mountpoint(m.DataDir(name), v), nil
}

// checkName 校验卷名。
func (m *VolumeManager) checkName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("卷名 %q 非法: %w", name, ErrVolumeBad)
	}
	return nil
}

func (m *VolumeManager) readMeta(name string) (*Volume, error) {
	data, err := os.ReadFile(m.metaPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("卷 %q: %w", name, ErrVolumeNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("读取卷元数据失败: %w", err)
	}
	var v Volume
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("解析卷 %s 元数据失败: %w", name, ErrVolumeBad)
	}
	if !v.Valid() {
		return nil, fmt.Errorf("卷 %s 定义损坏: %w", name, ErrVolumeBad)
	}
	return &v, nil
}

func (m *VolumeManager) writeMeta(v *Volume) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化卷元数据失败: %w", err)
	}
	if err := os.MkdirAll(m.metaRoot(), 0o755); err != nil {
		return fmt.Errorf("创建卷元数据目录失败: %w", err)
	}
	tmp := filepath.Join(m.metaRoot(), ".tmp.json")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写卷元数据失败: %w", err)
	}
	if err := os.Rename(tmp, m.metaPath(v.Name)); err != nil {
		return fmt.Errorf("落位卷元数据失败: %w", err)
	}
	return nil
}

func defaultStoreRootV() (string, error) {
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
