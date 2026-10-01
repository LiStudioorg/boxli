// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"strings"
	"testing"
)

func TestParseCIDR(t *testing.T) {
	if _, _, err := ParseCIDR("172.18.0.0/16"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseCIDR("not-a-cidr"); err == nil {
		t.Error("非法 CIDR 应报错")
	}
}

func TestNetIP4Bytes(t *testing.T) {
	if b := netIP4Bytes("1.2.3.4"); b == nil || b[0] != 1 || b[3] != 4 {
		t.Error("netIP4Bytes 解析错误")
	}
	if b := netIP4Bytes("bad"); b != nil {
		t.Error("非法 IP 应返回 nil")
	}
	if b := netIP4Bytes("::1"); b != nil {
		t.Error("IPv6 应返回 nil（仅支持 IPv4）")
	}
}

func TestIfaceName(t *testing.T) {
	if got := ifaceName("boxli0"); got != "boxli0" {
		t.Errorf("ifaceName(boxli0)=%s", got)
	}
	// 超长与非法字符收敛。
	long := ifaceName(strings.Repeat("a", 30))
	if len(long) > 15 {
		t.Errorf("接口名过长: %s", long)
	}
	if got := ifaceName("---"); got == "" {
		t.Error("纯非法名应退化为非空")
	}
	if got := HostBridgeIface("my/net"); got == "" || len(got) > 15 {
		t.Errorf("HostBridgeIface 结果异常: %q", got)
	}
}

func TestVethNames(t *testing.T) {
	h, p := "veth"+shortID("123456789012", 7), "vpe"+shortID("123456789012", 7)
	for _, n := range []string{h, p} {
		if n == "" || len(n) > 15 {
			t.Fatalf("veth 名异常: %s", n)
		}
	}
	if got := epVethName("boxli0", "123456789012"); got != "veth"+shortID("123456789012", 7) {
		t.Fatalf("epVethName 不一致: %s", got)
	}
	if epPeerName("boxli0", "123") != epPeerName("boxli0", "123") {
		t.Error("epPeerName 不确定")
	}
}

func TestPortMappingValidate(t *testing.T) {
	if err := (&PortMapping{ContainerPort: 80}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (&PortMapping{ContainerPort: 0}).Validate(); err == nil {
		t.Error("容器端口 0 应报错")
	}
	if err := (&PortMapping{ContainerPort: 70000}).Validate(); err == nil {
		t.Error("容器端口越界应报错")
	}
	p := &PortMapping{ContainerPort: 80}
	if p.Proto != "" {
		t.Fatal("默认 Proto 应为空")
	}
	_ = p.Validate()
	if p.Proto != ProtoTCP {
		t.Fatalf("Validate 应给默认 tcp，实得 %s", p.Proto)
	}
}

func TestValidateSubnet(t *testing.T) {
	if err := validateSubnet("172.18.0.0/16", "172.18.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ s, g string }{
		{"", "172.18.0.1"}, {"172.18.0.0/16", ""}, {"bad", "x"}, {"172.18.0.0/16", "bad"},
	} {
		if err := validateSubnet(c.s, c.g); err == nil {
			t.Errorf("validateSubnet(%q,%q) 应报错", c.s, c.g)
		}
	}
}
