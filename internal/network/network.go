// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package network 实现 Boxli 自研容器网络：容器间通信与对外 NAT 出口，
// 以及 bridge / host / none 三种网络模式的创建、管理与端口映射。
// 阶段 3 落地于本包；非 Linux 平台由 *_nonlinux.go 提供占位（返回
// ErrUnsupported），保证全仓库可交叉编译。
package network

import (
	"fmt"
	"time"
)

// Driver 是网络驱动类型。
type Driver string

// 内置驱动。
const (
	DriverBridge Driver = "bridge" // 自建 veth + 网桥 + nftables NAT
	DriverHost   Driver = "host"   // 直接复用宿主网络栈
	DriverNone   Driver = "none"   // 完全隔离，无网络
)

// Valid 报告驱动值是否合法。
func (d Driver) Valid() bool {
	switch d {
	case DriverBridge, DriverHost, DriverNone:
		return true
	}
	return false
}

// Proto 是端口映射协议。
type Proto string

const (
	ProtoTCP Proto = "tcp"
	ProtoUDP Proto = "udp"
)

// PortMapping 是一条 -p 端口映射。
type PortMapping struct {
	// HostIP 是宿主机监听地址（默认 0.0.0.0）。
	HostIP string `json:"hostIP,omitempty"`
	// HostPort 是宿主机端口（0 表示随机）。
	HostPort int `json:"hostPort"`
	// ContainerPort 是容器内端口。
	ContainerPort int `json:"containerPort"`
	// Proto 是协议 tcp/udp。
	Proto Proto `json:"proto,omitempty"`
}

// Endpoint 是容器接入网络的一个端点。
type Endpoint struct {
	// ContainerID 是容器 ID。
	ContainerID string `json:"containerId"`
	// ContainerName 是容器名（用于内置 DNS 解析）。
	ContainerName string `json:"containerName"`
	// IP 是分配给该容器的地址。
	IP string `json:"ip"`
	// Ports 是该容器在本网络上的端口映射（仅 bridge）。
	Ports []*PortMapping `json:"ports,omitempty"`
}

// Network 是一个已定义网络的完整描述（持久化于 <root>/networks/<name>.json）。
type Network struct {
	// Name 是网络名。
	Name string `json:"name"`
	// Driver 是驱动类型。
	Driver Driver `json:"driver"`
	// Subnet 是网段 CIDR（如 172.18.0.0/16），仅 bridge 有意义。
	Subnet string `json:"subnet,omitempty"`
	// Gateway 是网关地址（如 172.18.0.1），仅 bridge。
	Gateway string `json:"gateway,omitempty"`
	// Internal 表示不提供对外 NAT 出口。
	Internal bool `json:"internal,omitempty"`
	// CreatedAt 是创建时间（UTC RFC 3339）。
	CreatedAt string `json:"createdAt"`
	// Endpoints 是当前接入的容器端点。
	Endpoints []*Endpoint `json:"endpoints,omitempty"`
}

// New 构造一个未持久化的网络定义。
func New(name string, d Driver) *Network {
	return &Network{Name: name, Driver: d, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
}

// IsPreset 报告该网络是否为内置预置网络（boxli0）。
func (n *Network) IsPreset() bool { return n.Name == PresetBridgeName }

// String 面向用户的可读摘要。
func (n *Network) String() string {
	return fmt.Sprintf("%s\t%s\t%s", n.Name, n.Driver, n.Subnet)
}

// ErrDataDir 表示无法确定数据目录（Manager 构造失败）。
var ErrDataDir = fmt.Errorf("boxli/network: 无法确定数据目录")
