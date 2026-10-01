// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import "testing"

func TestParseNetMode(t *testing.T) {
	cases := map[string]NetMode{
		"":         ModeBridge,
		"boxli0":   ModeBridge,
		"bridge":   ModeBridge,
		"mybridge": ModeBridge, // 自定义名称一律走桥接
		"host":     ModeHost,
		"none":     ModeNone,
	}
	for in, want := range cases {
		if got := ParseNetMode(in); got != want {
			t.Errorf("ParseNetMode(%q)=%s 期望 %s", in, got, want)
		}
	}
}

func TestClientNetConfig(t *testing.T) {
	m := newTestManager(t)
	// 创建 bridge 并接入容器，验证 ClientNetConfig 返回 IP/网关/前缀。
	if _, err := m.Create("br", DriverBridge, "172.22.0.0/16", "172.22.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("br", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	cn, err := m.ClientNetConfig("br", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if cn.Prefix != 16 || cn.Gateway != "172.22.0.1" || cn.IP == "" || cn.Name != "br" {
		t.Fatalf("ClientNetConfig 错误: %+v", cn)
	}
	// 未接入的容器应报错。
	if _, err := m.ClientNetConfig("br", "nobody"); err == nil {
		t.Error("未接入的容器应报错")
	}
}

func TestHostBridgeIface(t *testing.T) {
	if got := HostBridgeIface("boxli0"); got == "" {
		t.Error("HostBridgeIface 返回空")
	}
}
