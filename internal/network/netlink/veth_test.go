// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"testing"
)

// mustAttr 解出属性值；不存在则 Fatal。
func mustAttr(t *testing.T, m map[uint16][]byte, k uint16) []byte {
	t.Helper()
	v, ok := m[k]
	if !ok {
		t.Fatalf("缺少属性 %d", k)
	}
	return v
}

// TestBuildVethReqPeerIsAttribute 回归 Bug 7：VETH_INFO_PEER 内部必须是一个
// IFLA_IFNAME 属性（不是裸字符串），否则内核解析失败丢弃请求 → EAGAIN。
func TestBuildVethReqPeerIsAttribute(t *testing.T) {
	r := buildVethReq("vethab", "vpeab")
	buf := r.buf
	if len(buf) < 32 {
		t.Fatalf("请求过短: %d", len(buf))
	}
	// nlmsghdr16 + ifinfomsg16 之后是顶层属性。
	outer, err := parseAttrs(buf[32:])
	if err != nil {
		t.Fatal(err)
	}
	linkinfo := mustAttr(t, outer, IFLA_LINKINFO)
	li, err := parseAttrs(linkinfo)
	if err != nil {
		t.Fatalf("解析 IFLA_LINKINFO: %v", err)
	}
	data := mustAttr(t, li, IFLA_INFO_DATA)
	d, err := parseAttrs(data)
	if err != nil {
		t.Fatalf("解析 IFLA_INFO_DATA: %v", err)
	}
	peer := mustAttr(t, d, VETH_INFO_PEER)
	// peer 值 = ifinfomsg(16) + IFLA_IFNAME 属性。
	if len(peer) < 17 {
		t.Fatalf("VETH_INFO_PEER 过短: %d", len(peer))
	}
	peerAttrs, err := parseAttrs(peer[16:])
	if err != nil {
		t.Fatalf("解析 VETH_INFO_PEER 内部: %v", err)
	}
	peerName := string(trimAttrString(mustAttr(t, peerAttrs, IFLA_IFNAME)))
	if peerName != "vpeab" {
		t.Fatalf("peer 名不是 IFLA_IFNAME 属性，实得 %q", peerName)
	}
}
