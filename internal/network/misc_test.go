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
		t.Fatal("licore0 应为 preset")
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
	if _, err := m.Connect("licore0", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.AllocatePorts("licore0", "c1", []*PortMapping{{HostPort: 80, ContainerPort: 80}}); err != nil {
		t.Fatal(err)
	}
	n, err := m.Load("licore0")
	if err != nil || len(n.Endpoints) == 0 {
		t.Fatalf("端点未登记: %v", err)
	}
	ins, err := m.Inspect("licore0")
	if err != nil || ins == nil {
		t.Fatalf("Inspect 失败: %v", err)
	}
	// ReleasePorts 清端口。
	if err := m.ReleasePorts("licore0", "c1"); err != nil {
		t.Fatal(err)
	}
	n2, _ := m.Load("licore0")
	for _, e := range n2.Endpoints {
		if e.ContainerID == "c1" && len(e.Ports) != 0 {
			t.Error("ReleasePorts 未清端口")
		}
	}
	// 对不存在容器 ReleasePorts 幂等。
	if err := m.ReleasePorts("licore0", "ghost"); err != nil {
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
	if _, err := m.Connect("licore0", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("licore0", "c1", "alice", ""); err == nil {
		t.Error("重复接入应报错")
	}
}

// TestPruneEndpoints 验证移除已死容器的端点。
func TestPruneEndpoints(t *testing.T) {
	m := newTestManager(t)
	if err := m.EnsurePreset(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("licore0", "c1", "alive", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Connect("licore0", "c2", "dead", ""); err != nil {
		t.Fatal(err)
	}
	// c2 已死（alive 谓词 false）。
	if err := m.PruneEndpoints("licore0", func(id string) bool { return id == "c1" }); err != nil {
		t.Fatal(err)
	}
	n, _ := m.Load("licore0")
	ids := map[string]bool{}
	for _, e := range n.Endpoints {
		ids[e.ContainerID] = true
	}
	if len(n.Endpoints) != 1 || !ids["c1"] || ids["c2"] {
		t.Fatalf("PruneEndpoints 后端点错误: %+v", n.Endpoints)
	}
}
