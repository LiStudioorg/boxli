// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// ParseCIDR 解析 "172.18.0.0/16" 并返回网段、掩码长度。
func ParseCIDR(s string) (string, string, error) {
	if _, _, e := net.ParseCIDR(s); e != nil {
		return "", "", e
	}
	return s, "", nil
}

// netIP4Bytes 解析点分 IPv4 字符串，返回 4 字节；格式错误返回 nil。
func netIP4Bytes(s string) []byte {
	out := net.ParseIP(s).To4()
	return out // 非法时为 nil
}

// defaultStoreRoot 复用 store.Open 的数据目录规则：$BOXLI_HOME → ~/.boxli。
func defaultStoreRoot() (string, error) {
	root := os.Getenv("BOXLI_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("确定数据目录失败: %w", err)
		}
		root = filepath.Join(home, ".boxli")
	}
	return root, nil
}

// allocateIP 在网段内分配一个可用 IP（跳过网络地址、网关）。
// 返回 ErrNoIPAvailable 当耗尽。
func (n *Network) allocateIP(want string) (string, error) {
	if n.Subnet == "" {
		return "", fmt.Errorf("网络 %s 未定义子网: %w", n.Name, ErrBadNetwork)
	}
	_, ipnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return "", fmt.Errorf("网络 %s 子网非法: %w", n.Name, ErrBadNetwork)
	}
	base := ipnet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("网络 %s 仅支持 IPv4: %w", n.Name, ErrBadNetwork)
	}
	networkAddr := ipnet.IP.To4()
	ones, bits := ipnet.Mask.Size()
	hosts := 1 << uint(bits-ones-1) // 去掉网络地址与广播
	if hosts < 2 {
		return "", fmt.Errorf("网络 %s 子网过小: %w", n.Name, ErrBadNetwork)
	}

	used := map[uint]bool{}
	for _, e := range n.Endpoints {
		ip := net.ParseIP(e.IP).To4()
		if ip != nil {
			ipu := uint(ip[0])<<24 | uint(ip[1])<<16 | uint(ip[2])<<8 | uint(ip[3])
			used[ipu] = true
		}
	}
	gw := net.ParseIP(n.Gateway).To4()

	// 要求指定 IP 时必须落在子网内且未被占用。
	if want != "" {
		ip := net.ParseIP(want).To4()
		if ip == nil {
			return "", fmt.Errorf("非法 IP %q: %w", want, ErrBadNetwork)
		}
		if !ipnet.Contains(ip) {
			return "", fmt.Errorf("IP %q 不在子网 %s 内: %w", want, n.Subnet, ErrBadNetwork)
		}
		ipu := uint(ip[0])<<24 | uint(ip[1])<<16 | uint(ip[2])<<8 | uint(ip[3])
		netu := uint(networkAddr[0])<<24 | uint(networkAddr[1])<<16 | uint(networkAddr[2])<<8 | uint(networkAddr[3])
		if ipu == netu {
			return "", fmt.Errorf("IP %q 是网络地址: %w", want, ErrBadNetwork)
		}
		if used[ipu] {
			return "", fmt.Errorf("IP %q 已被占用: %w", want, ErrNoIPAvailable)
		}
		return want, nil
	}

	// 自动分配：从 .2 起扫描。
	netu := uint(networkAddr[0])<<24 | uint(networkAddr[1])<<16 | uint(networkAddr[2])<<8 | uint(networkAddr[3])
	start := netu + 2
	if gw != nil {
		gwu := uint(gw[0])<<24 | uint(gw[1])<<16 | uint(gw[2])<<8 | uint(gw[3])
		used[gwu] = true
	}
	end := netu + uint(hosts) - 1 // 广播地址前一位
	for i := start; i <= end; i++ {
		if used[i] {
			continue
		}
		return fmt.Sprintf("%d.%d.%d.%d", byte(i>>24), byte(i>>16), byte(i>>8), byte(i)), nil
	}
	return "", fmt.Errorf("子网 %s 无可用 IP: %w", n.Subnet, ErrNoIPAvailable)
}

// epVethName 为 containerID 生成网桥侧 veth 名（Linux 接口名 ≤15 字符）。
func epVethName(netName, containerID string) string {
	return "veth" + shortID(containerID, 7)
}

// epPeerName 生成容器侧 veth 名。
func epPeerName(netName, containerID string) string {
	return "vpe" + shortID(containerID, 7)
}

// shortID 截取容器 ID 指定长度。
func shortID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// bridgeHostInterface 返回某 bridge 网络在宿主上的接口名。
func bridgeHostInterface(netName string) string {
	return ifaceName(netName)
}

// ifaceName 把网络名收敛为合法 Linux 接口名（≤15，去非法字符）。
func ifaceName(name string) string {
	out := make([]byte, 0, len(name))
	for _, c := range name {
		c := byte(c)
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			out = append(out, c)
		}
		if len(out) == 15 {
			break
		}
	}
	if len(out) == 0 {
		out = []byte{'b'}
	}
	return string(out)
}
