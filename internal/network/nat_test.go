// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package network

import (
	"strings"
	"testing"
)

// 断言生成的 nft 命令为"add rule"形式、不含 replace——replace 语法
// 已被 nft parse 证实报 "syntax error, unexpected ip, expecting handle"。
func TestNatRuleArgsUseAddRule(t *testing.T) {
	masq := natMasqArgs("172.18.0.0/16")
	joined := strings.Join(masq, " ")
	if strings.Contains(joined, "replace") {
		t.Fatalf("masq 命令用了 replace（语法错误）: %s", joined)
	}
	if !strings.HasPrefix(joined, "add rule ip licore post_nat") {
		t.Fatalf("masq 命令结构错误: %s", joined)
	}
	if !strings.Contains(joined, "ip saddr 172.18.0.0/16 masquerade") {
		t.Fatalf("masq 命令缺 masquerade: %s", joined)
	}

	ep := &Endpoint{IP: "172.18.0.2"}
	dnat := dnatRuleArgs(ep, &PortMapping{HostPort: 8080, ContainerPort: 80})
	j := strings.Join(dnat, " ")
	if strings.HasPrefix(j, "replace") {
		t.Fatalf("dnat 命令用了 replace: %s", j)
	}
	if !strings.HasPrefix(j, "add rule ip licore pre_nat") || !strings.Contains(j, "dnat to 172.18.0.2:80") {
		t.Fatalf("dnat 命令结构错误: %s", j)
	}
}

// 宿主本机发起的流量必须被 out_nat（output hook）DNAT，且伪装回环源地址，
// 否则 `curl localhost:<发布端口>` 是 connection refused / timed out。
// 两者缺一不可，断言规则形状与关键限定条件。
func TestNatHostOriginatedRules(t *testing.T) {
	lb := strings.Join(natLoopbackMasqArgs("172.18.0.0/16"), " ")
	if !strings.HasPrefix(lb, "add rule ip licore post_nat") {
		t.Fatalf("回环伪装链错误: %s", lb)
	}
	if !strings.Contains(lb, "ip saddr 127.0.0.0/8 ip daddr 172.18.0.0/16 masquerade") {
		t.Fatalf("回环伪装缺源/目的限定: %s", lb)
	}

	ep := &Endpoint{IP: "172.18.0.2"}
	out := strings.Join(dnatOutRuleArgs(ep, &PortMapping{HostPort: 18080, ContainerPort: 8080}), " ")
	if !strings.HasPrefix(out, "add rule ip licore out_nat") {
		t.Fatalf("宿主本机 DNAT 必须挂 out_nat: %s", out)
	}
	if !strings.Contains(out, "ip daddr 127.0.0.0/8") {
		t.Fatalf("宿主本机 DNAT 必须限定回环目的（否则劫持访问外网同端口的流量）: %s", out)
	}
	if !strings.Contains(out, "tcp dport 18080 dnat to 172.18.0.2:8080") {
		t.Fatalf("宿主本机 DNAT 目标错误: %s", out)
	}

	udp := strings.Join(dnatOutRuleArgs(ep, &PortMapping{HostPort: 5353, ContainerPort: 53, Proto: ProtoUDP}), " ")
	if !strings.Contains(udp, "udp dport 5353") {
		t.Fatalf("UDP 协议名未生效: %s", udp)
	}
}
