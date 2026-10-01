// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/LiStudioorg/boxli/internal/storage"
	"github.com/LiStudioorg/boxli/internal/store"
)

// volMount 是一次 -v 的解析结果（引擎侧）。
type volMount struct {
	Source    string // 宿主源绝对路径或命名卷名；空表示匿名卷
	Target    string // 容器内挂载点
	ReadOnly  bool   // :ro
	Anonymous bool   // 匿名卷（Source 为空）
}

// parseVolumes 解析 -v 参数列表："TARGET"（匿名）、"SRC:TARGET"、"SRC:TARGET:ro"。
func parseVolumes(raw []string) ([]*volMount, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]*volMount, 0, len(raw))
	for _, s := range raw {
		m, err := parseVolume(s)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func parseVolume(raw string) (*volMount, error) {
	parts := strings.Split(raw, ":")
	m := &volMount{}
	switch len(parts) {
	case 1:
		m.Target = parts[0]
		m.Anonymous = true
	case 2:
		m.Source = parts[0]
		m.Target = parts[1]
	case 3:
		m.Source = parts[0]
		m.Target = parts[1]
		if parts[2] != "ro" {
			return nil, fmt.Errorf("run: 非法 -v 选项 %q（仅支持 ro）", parts[2])
		}
		m.ReadOnly = true
	default:
		return nil, fmt.Errorf("run: 非法 -v %q", raw)
	}
	if !filepath.IsAbs(m.Target) {
		return nil, fmt.Errorf("run: -v %q 容器路径 %q 必须是绝对路径", raw, m.Target)
	}
	return m, nil
}

// wireVolumesBeforeStart 把 -v 卷解析并落盘：bind 直接取宿主绝对路径；
// 命名卷解析其数据目录；匿名卷自动创建（anon_<id>）。产出已解析的
// store.Mount 列表回写进 config.json，运行时据此挂载。
func wireVolumesBeforeStart(st *store.Store, cfg *store.ContainerConfig, mounts []*volMount) error {
	if len(mounts) == 0 {
		cfg.Mounts = nil
		return st.WriteContainerConfig(cfg)
	}
	vm, err := storage.NewVolumeManager(st.Root)
	if err != nil {
		return err
	}
	var resolved []store.Mount
	for _, m := range mounts {
		src := m.Source
		switch {
		case m.Anonymous:
			// 自动创建匿名卷并复用。
			name := "anon_" + cfg.ID
			if _, err := vm.Create(name, storage.DriverLocal, 0); err != nil && !errors.Is(err, storage.ErrVolumeExists) {
				return err
			}
			mp, err := vm.Mountpoint(name)
			if err != nil {
				return err
			}
			src = mp
		case len(m.Source) > 1 && m.Source[0] == '/':
			// bind 挂载：宿主绝对路径即源。
		default:
			// 命名卷：校验存在并取数据目录。
			mp, err := vm.Mountpoint(m.Source)
			if err != nil {
				return fmt.Errorf("run: 卷 %q 不存在，请先 boxli volume create: %w", m.Source, err)
			}
			src = mp
		}
		if !filepath.IsAbs(src) {
			return fmt.Errorf("run: 卷源 %q 不是绝对路径", src)
		}
		resolved = append(resolved, store.Mount{Source: src, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	cfg.Mounts = resolved
	return st.WriteContainerConfig(cfg)
}
