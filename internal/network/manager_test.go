// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"errors"
	"testing"
)

// 测试用临时管理器（数据层，不触碰宿主网桥）。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestListCreatesPreset(t *testing.T) {
	m := newTestManager(t)
	nets, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, n := range nets {
		if n.Name == PresetBridgeName {
			found = true
			if n.Driver != DriverBridge || n.Subnet != PresetBridgeSubnet {
				t.Errorf("预置网络定义不符: %+v", n)
			}
		}
	}
	if !found {
		t.Fatalf("List 未创建预置网络 %s", PresetBridgeName)
	}
}

func TestCreateAndLoadRoundtrip(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("mybr", DriverBridge, "10.5.0.0/24", "10.5.0.1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	n, err := m.Load("mybr")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if n.Driver != DriverBridge || n.Gateway != "10.5.0.1" {
		t.Errorf("roundtrip 不符: %+v", n)
	}
	// 重复创建应报已存在。
	if _, err := m.Create("mybr", DriverBridge, "10.5.0.0/24", "10.5.0.1"); !errors.Is(err, ErrNetworkExists) {
		t.Errorf("期望 ErrNetworkExists，got %v", err)
	}
}

func TestCreateRejectsBadName(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("bad/name", DriverBridge, "10.0.0.0/24", "10.0.0.1"); !errors.Is(err, ErrBadNetwork) {
		t.Errorf("期望 ErrBadNetwork，got %v", err)
	}
}

func TestConnectDisconnectAllocatesIP(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("br", DriverBridge, "172.20.0.0/16", "172.20.0.1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	ep, err := m.Connect("br", "aaa111", "web", "")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if ep.IP == "" {
		t.Fatal("未分配 IP")
	}
	// 再次接入同一容器应报已存在。
	if _, err := m.Connect("br", "aaa111", "web", ""); !errors.Is(err, ErrEndpointExists) {
		t.Errorf("期望 ErrEndpointExists，got %v", err)
	}
	// 第二个容器应得到不同 IP。
	ep2, err := m.Connect("br", "bbb222", "db", "")
	if err != nil {
		t.Fatalf("Connect2: %v", err)
	}
	if ep2.IP == ep.IP {
		t.Errorf("重复分配同一 IP %s", ep.IP)
	}
	// 断开后再接应复用。
	if err := m.Disconnect("br", "bbb222"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if err := m.Disconnect("br", "bbb222"); !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("重复断开应报 NotFound，got %v", err)
	}
}

func TestAllocatePortsRoundtrip(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("br", DriverBridge, "172.21.0.0/16", "172.21.0.1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Connect("br", "ccc", "app", ""); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ports := []*PortMapping{{HostPort: 8080, ContainerPort: 80, Proto: ProtoTCP}}
	if err := m.AllocatePorts("br", "ccc", ports); err != nil {
		t.Fatalf("AllocatePorts: %v", err)
	}
	n, _ := m.Load("br")
	if len(n.Endpoints) != 1 || len(n.Endpoints[0].Ports) != 1 || n.Endpoints[0].Ports[0].HostPort != 8080 {
		t.Errorf("端口映射未持久化: %+v", n.Endpoints)
	}
	// 非法映射
	if err := m.AllocatePorts("br", "ccc", []*PortMapping{{HostPort: 70000, ContainerPort: 80}}); !errors.Is(err, ErrBadPortMapping) {
		t.Errorf("期望 ErrBadPortMapping，got %v", err)
	}
}

func TestPresetCannotBeRemoved(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.List(); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := m.Remove(PresetBridgeName); err == nil {
		t.Errorf("删除预置网络应被拒绝，got %v", err)
	}
}

func TestDNSResolver(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("br", DriverBridge, "172.22.0.0/16", "172.22.0.1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := m.Connect("br", "d1", "alice", ""); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := NewDNSResolver(m)
	ip, err := r.Resolve("alice")
	if err != nil || ip == "" {
		t.Fatalf("Resolve(alice): %q, %v", ip, err)
	}
	if _, err := r.Resolve("nobody"); !errors.Is(err, ErrEndpointNotFound) {
		t.Errorf("期望解析失败，got %v", err)
	}
	if es := r.Entries(); len(es) == 0 {
		t.Error("Entries 为空")
	}
}
