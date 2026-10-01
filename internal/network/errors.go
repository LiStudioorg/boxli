// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import "errors"

// 哨兵错误：调用方用 errors.Is 判定，文案面向用户。
var (
	// ErrNotRoot 表示当前进程缺少 CAP_NET_ADMIN（通常是非 root），
	// 无法创建网桥 / veth / nftables 规则。
	ErrNotRoot = errors.New("boxli/network: 需要 root 或 CAP_NET_ADMIN 权限")
	// ErrUnsupported 表示当前平台没有网络实现（非 Linux）。
	ErrUnsupported = errors.New("boxli/network: 本平台网络未实现（阶段 3 仅支持 linux）")
	// ErrNetworkNotFound 表示按名字找不到网络。
	ErrNetworkNotFound = errors.New("boxli/network: 网络不存在")
	// ErrNetworkExists 表示同名网络已存在。
	ErrNetworkExists = errors.New("boxli/network: 同名网络已存在")
	// ErrBadNetwork 表示网络定义非法（名字 / 子网 / 驱动等）。
	ErrBadNetwork = errors.New("boxli/network: 网络定义非法")
	// ErrNoIPAvailable 表示子网内已无可用地址。
	ErrNoIPAvailable = errors.New("boxli/network: 子网内无可用 IP")
	// ErrEndpointNotFound 表示容器未接入该网络。
	ErrEndpointNotFound = errors.New("boxli/network: 容器未接入该网络")
	// ErrEndpointExists 表示容器已接入该网络。
	ErrEndpointExists = errors.New("boxli/network: 容器已接入该网络")
	// ErrBadPortMapping 表示 -p 端口映射参数非法。
	ErrBadPortMapping = errors.New("boxli/network: 端口映射参数非法")
	// ErrNoFirewall 表示既没有可用的 nft，也没有可用的 iptables。
	ErrNoFirewall = errors.New("boxli/network: 未找到可用的 nft / iptables")
)
