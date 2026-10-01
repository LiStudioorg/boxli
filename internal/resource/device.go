// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import (
	"fmt"
	"strconv"
	"strings"
)

// DeviceRequest 是一次加速器直通请求（--gpu / --npu）。
type DeviceRequest struct {
	// Kind 是加速器种类："gpu" 或 "npu"。
	Kind string
	// Card 是 DRM/设备 /dev 路径（如 /dev/dri/card0 或 /dev/kgsl-3d0）；
	// 空表示"任意"（取第一个可用）。
	Card string
	// Reference 是设备标识，可为 PCI 地址（0000:00:02.0）、具体 /dev 路径或
	// "all"（全部）；空时用户只给了个数。
	Reference string
	// Count 是需要的设备数量（默认 1）。
	Count int
	// Devices 是解析后的具体设备条目（major:minor + /dev 路径）。
	Devices []DeviceRequestEntry
}

// DeviceRequestEntry 是解析后的单个直通设备。
type DeviceRequestEntry struct {
	// Major / Minor 是设备号（cgroup devices 白名单用）。
	Major int64
	Minor int64
	// Path 是 /dev 下的路径。
	Path string
}

// Validate 校验请求字段。
func (r *DeviceRequest) Validate() error {
	if r == nil {
		return nil
	}
	switch r.Kind {
	case "gpu", "npu":
	default:
		return fmt.Errorf("设备类型 %q 非法（仅 gpu|npu）: %w", r.Kind, ErrBadDevice)
	}
	if r.Count < 0 {
		return fmt.Errorf("设备数量 %d 非法: %w", r.Count, ErrBadDevice)
	}
	if r.Reference == "all" {
		return nil
	}
	if r.Reference != "" {
		if _, err := ParseDeviceID(r.Reference); err != nil {
			return err
		}
	}
	if r.Card != "" && !strings.HasPrefix(r.Card, "/dev/") {
		return fmt.Errorf("设备 /dev 路径 %q 非法: %w", r.Card, ErrBadDevice)
	}
	return nil
}

// ParseDeviceID 校验设备标识并返回 major+minor 的和（仅用于合法性判定；
// 具体 major/minor 由 cgroup 写入侧通过 StatDevice 单独获取）。
// 接受 "/dev/sda"（从 stat 读取）、"major:minor" 或 "/dev/char/1:3"。
func ParseDeviceID(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("空设备标识: %w", ErrBadDevice)
	}
	if strings.HasPrefix(s, "/dev/char/") {
		_, _, err := parseMajorMinor(strings.TrimPrefix(s, "/dev/char/"))
		return 0, err
	}
	if strings.HasPrefix(s, "/dev/") {
		return statDevice(s)
	}
	_, _, err := parseMajorMinor(s)
	return 0, err
}

// parseMajorMinor 解析 "major:minor"。
func parseMajorMinor(s string) (int64, int64, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("设备 %q 非法：应为 major:minor 或 /dev 路径: %w", s, ErrBadDevice)
	}
	maj, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("设备 %q major 非法: %w", s, ErrBadDevice)
	}
	min, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("设备 %q minor 非法: %w", s, ErrBadDevice)
	}
	return maj, min, nil
}
