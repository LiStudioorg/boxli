// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package network

import (
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"github.com/LiStudioorg/boxli/internal/network/netlink"
)

// bridgeHostIface 返回某网络在宿主上的网桥接口名。
func bridgeHostIface(n *Network) string { return bridgeHostInterface(n.Name) }

// hostDefaultIface 探测宿主默认出口接口（用于 NAT 出口）。
func hostDefaultIface() (string, error) {
	out, err := exec.Command("ip", "route", "show", "default").Output()
	if err != nil {
		return "", fmt.Errorf("读取默认路由失败: %w", err)
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("未发现宿主默认出口接口")
}

// driverBootstrap 按驱动创建并上线宿主侧网络拓扑。
func driverBootstrap(n *Network) error {
	if n.Driver != DriverBridge {
		return nil
	}
	br := bridgeHostIface(n)
	if _, err := netlink.LinkByName(br); err == nil {
		return nil // 已存在，幂等
	}
	if err := netlink.NewLink(br, netlink.KindBridge); err != nil {
		return fmt.Errorf("创建网桥 %s: %w", br, err)
	}
	if err := netlink.LinkUp(br); err != nil {
		return fmt.Errorf("上线网桥 %s: %w", br, err)
	}
	_, ipnet, err := net.ParseCIDR(n.Subnet)
	if err != nil {
		return fmt.Errorf("网段 %s 非法: %w", n.Subnet, ErrBadNetwork)
	}
	ones, _ := ipnet.Mask.Size()
	if err := netlink.AddAddr(br, n.Gateway, ones); err != nil {
		return fmt.Errorf("为网桥添加网关 %s/%d: %w", n.Gateway, ones, err)
	}
	return nil
}

// driverTeardown 删除网络在宿主侧的网桥。
func driverTeardown(n *Network) error {
	if n.Driver != DriverBridge {
		return nil
	}
	return netlink.DelLink(bridgeHostIface(n))
}

// bridgeAttachEndpoints 为 bridge 网络实化端点（本分支仅登记端点；veth
// 对与容器命名空间注入由运行时的 infra 侧配合，见交接摘要）。
func bridgeAttachEndpoints(n *Network) error { return nil }

// bridgeDetachEndpoint 清理某容器的 veth（忽略已不存在的接口）。
func bridgeDetachEndpoint(veth string) error {
	if err := netlink.DelLink(veth); err != nil {
		slog.Debug("删除 veth 失败（可能已不存在）", "veth", veth, "err", err)
	}
	return nil
}

// nftCheck 检查 nft 是否可用；不可用返回 ErrNoFirewall。
func nftCheck() error {
	if _, err := exec.LookPath("nft"); err != nil {
		return ErrNoFirewall
	}
	return nil
}

// nftCreateTables 幂等创建 nft 表与两个链（post_nat=出口 NAT，pre_nat=DNAT）。
func nftCreateTables() error {
	if err := nftCheck(); err != nil {
		return err
	}
	// 表
	_ = runNft("add", "table", "ip", "boxli")
	// 两个链
	_ = runNft("add", "chain", "ip", "boxli", "post_nat",
		"{ type nat hook postrouting priority srcnat; policy accept; }")
	_ = runNft("add", "chain", "ip", "boxli", "pre_nat",
		"{ type nat hook prerouting priority dstnat; policy accept; }")
	return nil
}

// runNft 运行 nft 子进程并贴上下文；容忍"已存在"类错误。
func runNft(args ...string) error {
	cmd := exec.Command("nft", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := string(out)
		if strings.Contains(msg, "File exists") || strings.Contains(msg, "exists") {
			return nil
		}
		return fmt.Errorf("nft %s 失败: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(msg))
	}
	return nil
}

// applyPortRules 实化出口 MASQUERADE 与全部端口 DNAT。
func (n *Network) applyPortRules() error {
	if n.Driver != DriverBridge {
		return nil
	}
	if err := nftCreateTables(); err != nil {
		return err
	}
	// 出口 NAT
	if !n.Internal {
		if err := runNft("replace", "rule", "ip", "boxli", "post_nat",
			"ip", "saddr", n.Subnet, "masquerade"); err != nil {
			return err
		}
	}
	for _, e := range n.Endpoints {
		for _, p := range e.Ports {
			if err := n.addDnatRule(e, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteAllDnat 清空 pre_nat 链（在重放数据前调用，避免重复规则堆叠）。
func (n *Network) deleteAllDnat() {
	_ = runNft("flush", "chain", "ip", "boxli", "pre_nat")
}

func (n *Network) addDnatRule(e *Endpoint, p *PortMapping) error {
	proto := "tcp"
	if p.Proto == ProtoUDP {
		proto = "udp"
	}
	hostBind := p.HostIP
	if hostBind == "" {
		hostBind = "0.0.0.0"
	}
	return runNft("add", "rule", "ip", "boxli", "pre_nat",
		proto, "dport", strconv.Itoa(p.HostPort),
		"dnat", "to", e.IP+":"+strconv.Itoa(p.ContainerPort))
}

// removePortRules 移除全部 DNAT 规则（简化实现：flush 整链）。
func (n *Network) removePortRules(veth string) error {
	_ = veth
	n.deleteAllDnat()
	return nil
}

var _ = hostDefaultIface
var _ = netlink.KindVeth
