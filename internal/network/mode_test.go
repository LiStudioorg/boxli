// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import "testing"

func TestParseNetMode(t *testing.T) {
	cases := map[string]NetMode{
		"":         ModeBridge,
		"licore0":  ModeBridge,
		"bridge":   ModeBridge,
		"host":     ModeHost,
		"none":     ModeNone,
		"custom":   ModeBridge,
		"otherxyz": ModeBridge,
	}
	for in, want := range cases {
		if got := ParseNetMode(in); got != want {
			t.Errorf("ParseNetMode(%q)=%v 期望 %v", in, got, want)
		}
	}
}

// TestClientNetConfig 覆盖客户端网络配置解析（网关、前缀、端点定位）。
func TestClientNetConfig(t *testing.T) {
	m := newTestManager(t)
	if err := m.EnsurePreset(); err != nil {
		t.Fatal(err)
	}
	// 未接入端点 → ErrEndpointNotFound。
	if _, err := m.ClientNetConfig("licore0", "nope-cid"); err == nil {
		t.Fatal("未接入端点应报错")
	}
	// 建一个不存在的网络 → Load 报错。
	if _, err := m.ClientNetConfig("ghost", "x"); err == nil {
		t.Fatal("不存在的网络应报错")
	}
	// 接入一个端点后应能解析出网关与前缀。
	if _, err := m.Connect("licore0", "cid1", "c1", ""); err != nil {
		t.Fatal(err)
	}
	cn, err := m.ClientNetConfig("licore0", "cid1")
	if err != nil {
		t.Fatalf("ClientNetConfig: %v", err)
	}
	if cn.Gateway == "" || cn.IP == "" || cn.Prefix <= 0 {
		t.Fatalf("ClientNet 字段异常: %+v", cn)
	}
}
