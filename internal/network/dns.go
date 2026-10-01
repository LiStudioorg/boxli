// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"fmt"
	"net"
	"strings"
)

// DNSEntry 是内置 DNS 的一条解析记录。
type DNSEntry struct {
	Name string
	IP   string
}

// DNSResolver 是 Boxli 内置 DNS：解析容器名 → 网桥 IP。
// 本实现通过查询全部网络的端点表完成解析，输入为网络名 + 容器名。
type DNSResolver struct {
	manager *Manager
}

// NewDNSResolver 返回内置 DNS 解析器。
func NewDNSResolver(m *Manager) *DNSResolver { return &DNSResolver{manager: m} }

// Resolve 在整个网络的端点表里按容器名/短名解析 IP。
// 返回 ErrNetworkNotFound 当容器名匹配不到任何网络端点。
func (r *DNSResolver) Resolve(name string) (string, error) {
	// 内容名可能形如 "alice.mybox0"（容器名.网络名）。
	netName := ""
	cn := name
	if i := strings.LastIndex(name, "."); i > 0 {
		netName = name[:i]
		cn = name[i+1:]
	}
	networks, err := r.manager.List()
	if err != nil {
		return "", err
	}
	for _, n := range networks {
		if netName != "" && n.Name != netName {
			continue
		}
		for _, e := range n.Endpoints {
			if e.ContainerName == cn || e.ContainerName == name {
				if e.IP != "" {
					return e.IP, nil
				}
			}
		}
	}
	return "", fmt.Errorf("无法解析容器名 %q: %w", name, ErrEndpointNotFound)
}

// Entries 返回全部网络的解析表（含网络名限定形式）。
func (r *DNSResolver) Entries() []DNSEntry {
	networks, err := r.manager.List()
	if err != nil {
		return nil
	}
	var out []DNSEntry
	for _, n := range networks {
		for _, e := range n.Endpoints {
			if e.IP == "" {
				continue
			}
			out = append(out, DNSEntry{Name: e.ContainerName, IP: e.IP})
			if n.Name != "" && e.ContainerName != "" {
				out = append(out, DNSEntry{Name: e.ContainerName + "." + n.Name, IP: e.IP})
			}
		}
	}
	return out
}

// HostsLine 生成 /etc/hosts 里的一行，供容器注入（内置 DNS 之外的兜底）。
func (d DNSEntry) HostsLine() string {
	if !net.ParseIP(d.IP).IsUnspecified() {
		return d.IP + " " + d.Name
	}
	return d.IP + " " + d.Name
}
