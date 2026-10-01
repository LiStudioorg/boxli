// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"testing"
)

// TestAllocateIPEdge 验证 IP 分配的边界：want IP 校验、子网过小、耗尽。
func TestAllocateIPEdge(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("tiny", DriverBridge, "192.168.0.0/30", "192.168.0.1"); err != nil {
		t.Fatal(err)
	}
	n, _ := m.Load("tiny")
	// /30 过小（不足主机位）。
	if _, err := n.allocateIP(""); err == nil {
		t.Error("/30 应报子网过小或耗尽")
	}

	// want IP 必须落在子网内。
	if _, err := n.allocateIP("10.0.0.9"); err == nil {
		t.Error("子网外 want IP 应报错")
	}
}

// TestConnectHostNone 验证 host/none 网络接入后无 IP 分配、可断开。
func TestConnectHostNone(t *testing.T) {
	m := newTestManager(t)
	for _, d := range []Driver{DriverHost, DriverNone} {
		if _, err := m.Create("n"+string(d[0]), d, "", ""); err != nil {
			t.Fatal(err)
		}
		name := "n" + string(d[0])
		ep, err := m.Connect(name, "c1", "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		if ep.IP != "" {
			t.Fatalf("%s 不应分配 IP: %s", d, ep.IP)
		}
		if err := m.Disconnect(name, "c1"); err != nil {
			t.Fatal(err)
		}
		// 重复断开应报错。
		if err := m.Disconnect(name, "c1"); err == nil {
			t.Error("重复断开应报错")
		}
	}
}

// TestAllocatePortsValidation 验证端口映射校验与未接入容器报错。
func TestAllocatePortsValidation(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create("br", DriverBridge, "172.23.0.0/16", "172.23.0.1"); err != nil {
		t.Fatal(err)
	}
	// 未接入容器分配端口应报错。
	if err := m.AllocatePorts("br", "ghost", []*PortMapping{{HostPort: 80, ContainerPort: 80}}); err == nil {
		t.Error("未接入容器分配端口应报错")
	}
	// 非法端口应报错。
	if _, err := m.Connect("br", "c1", "alice", ""); err != nil {
		t.Fatal(err)
	}
	bad := []*PortMapping{{HostPort: 80, ContainerPort: 0}}
	if err := m.AllocatePorts("br", "c1", bad); err == nil {
		t.Error("非法容器端口应报错")
	}
}

// TestApplyNATUnknownNet 验证对不存在网络 ApplyNAT 报错。
func TestApplyNATUnknownNet(t *testing.T) {
	m := newTestManager(t)
	if err := m.ApplyNAT("ghost"); err == nil {
		t.Error("不存在网络 ApplyNAT 应报错")
	}
}
