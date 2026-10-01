// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import "testing"

func TestStringAndIsPreset(t *testing.T) {
	n := New("br0", DriverBridge)
	if n.IsPreset() {
		t.Fatal("br0 不应是 preset")
	}
	pre := New(PresetBridgeName, DriverBridge)
	if !pre.IsPreset() {
		t.Fatal("boxli0 应为 preset")
	}
	if s := n.String(); s == "" {
		t.Error("String 为空")
	}
}

func TestManagerRoot(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m.Root() == "" {
		t.Error("Root 为空")
	}
}

func TestInspectAndRelease(t *testing.T) {
	m := newTestManager(t)
	if err := m.EnsurePreset(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("boxli0", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AllocatePorts("boxli0", "c1", []*PortMapping{{HostPort: 80, ContainerPort: 80}}); err != nil {
		t.Fatal(err)
	}
	n, err := m.Load("boxli0")
	if err != nil || len(n.Endpoints) == 0 {
		t.Fatalf("端点未登记: %v", err)
	}
	ins, err := m.Inspect("boxli0")
	if err != nil || ins == nil {
		t.Fatalf("Inspect 失败: %v", err)
	}
	// ReleasePorts 清端口。
	if err := m.ReleasePorts("boxli0", "c1"); err != nil {
		t.Fatal(err)
	}
	n2, _ := m.Load("boxli0")
	for _, e := range n2.Endpoints {
		if e.ContainerID == "c1" && len(e.Ports) != 0 {
			t.Error("ReleasePorts 未清端口")
		}
	}
	// 对不存在容器 ReleasePorts 幂等。
	if err := m.ReleasePorts("boxli0", "ghost"); err != nil {
		t.Errorf("ghost ReleasePorts 应幂等成功: %v", err)
	}
}

func TestDNSEntryHostsLine(t *testing.T) {
	e := DNSEntry{Name: "web", IP: "172.18.0.2"}
	if got := e.HostsLine(); got != "172.18.0.2 web" {
		t.Fatalf("HostsLine = %q", got)
	}
}

func TestConnectDuplicate(t *testing.T) {
	m := newTestManager(t)
	if err := m.EnsurePreset(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("boxli0", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("boxli0", "c1", "alice", ""); err == nil {
		t.Error("重复接入应报错")
	}
}
