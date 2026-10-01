// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package network

// 非 Linux 平台：veth 装配与容器内网络配置不支持，返回 ErrUnsupported。

// AttachVeth 非 Linux 不支持。
func AttachVeth(netName, containerID string, childPID int) error { return ErrUnsupported }

// ConfigurePeer 非 Linux 不支持。
func ConfigurePeer(netName, containerID, ip, gateway string, prefix int, hostname string) error {
	return ErrUnsupported
}
