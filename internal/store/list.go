// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ImageInfo 是 `boxli images` 展示一行所需的信息：state.json 的运行期
// 字段 + index.json 的清单字段（架构、层数）。
type ImageInfo struct {
	State
	// Architecture 取自 index.json 的 architecture 字段。
	Architecture string
	// LayerCount 取自 index.json 的 layers 数量。
	LayerCount int
}

// ListImages 扫描 <root>/images/**/state.json，返回全部可解析的镜像。
// 个别镜像的 state.json / index.json 损坏时跳过并 slog.Warn，不影响整体
// listing；目录不存在视为空清单（首次使用时合法）。
func (s *Store) ListImages() ([]ImageInfo, error) {
	root := s.ImagesRoot()
	var infos []ImageInfo
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipAll // 尚无 images 目录
			}
			return err
		}
		if d.IsDir() || d.Name() != "state.json" {
			return nil
		}
		dir := filepath.Dir(p)
		version := filepath.Base(dir)
		// dir = <imagesRoot>/<name>/<version>；name 允许含 "/"（规范 §3.1），
		// 用 Rel 反推相对路径后剥掉末级 version。
		rel, rerr := filepath.Rel(root, dir)
		if rerr != nil {
			slog.Warn("镜像目录路径异常，跳过", "dir", dir, "err", rerr)
			return nil
		}
		name := strings.TrimSuffix(rel, string(filepath.Separator)+version)
		if name == rel || name == "." || name == "" {
			slog.Warn("镜像目录层级异常，跳过", "dir", dir)
			return nil
		}
		info, err := s.readImageInfo(name, version)
		if err != nil {
			slog.Warn("跳过损坏的镜像状态", "dir", dir, "err", err)
			return nil
		}
		infos = append(infos, *info)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描镜像目录失败: %w", err)
	}
	// 导入时间倒序；同一时刻按引用字典序保证稳定输出。
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].PulledAt != infos[j].PulledAt {
			return infos[i].PulledAt > infos[j].PulledAt
		}
		return infos[i].Ref < infos[j].Ref
	})
	return infos, nil
}

// readImageInfo 读取一个镜像的 state.json 与 index.json 并合并。
func (s *Store) readImageInfo(name, version string) (*ImageInfo, error) {
	st, err := s.ReadState(name, version)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.ImageDir(name, version), "index.json"))
	if err != nil {
		return nil, fmt.Errorf("读取 index.json 失败: %w", err)
	}
	var meta struct {
		Architecture string `json:"architecture"`
		Layers       []struct {
			Path string `json:"path"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("解析 index.json 失败: %w", err)
	}
	return &ImageInfo{
		State:        *st,
		Architecture: meta.Architecture,
		LayerCount:   len(meta.Layers),
	}, nil
}
