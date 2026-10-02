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
	if !strings.HasPrefix(joined, "add rule ip boxli post_nat") {
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
	if !strings.HasPrefix(j, "add rule ip boxli pre_nat") || !strings.Contains(j, "dnat to 172.18.0.2:80") {
		t.Fatalf("dnat 命令结构错误: %s", j)
	}
}
