// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"syscall"
	"testing"
)

// TestSetIFFBufWritesIfinfomsg 回归：flags/change 必须写进 ifinfomsg（偏移
// 16+8），而不是 nlmsghdr 的 seq/pid（偏移 8/12）——后者会覆盖请求 seq、
// 使内核丢包导致命令 EAGAIN 超时。
func TestSetIFFBufWritesIfinfomsg(t *testing.T) {
	// 模拟 newReq：16B 头 + 16B ifinfomsg。
	buf := make([]byte, 32)
	buf[8], buf[9], buf[10], buf[11] = 0xde, 0xad, 0xbe, 0xef // 真实的 seq（0xefbeadde）
	if buf[16] != 0 {
		t.Fatal("数组初始化异常")
	}
	setIFFBuf(buf, syscall.IFF_UP, syscall.IFF_UP)
	// nlmsghdr seq 应保持（未被覆盖）。
	gotSeq := uint32(buf[8]) | uint32(buf[9])<<8 | uint32(buf[10])<<16 | uint32(buf[11])<<24
	if gotSeq != 0xefbeadde {
		t.Fatalf("seq 被 setIFFBuf 覆盖: %#x", gotSeq)
	}
	// ifinfomsg.flags（buf[24:28]）应为 IFF_UP。
	flags := uint32(buf[24]) | uint32(buf[25])<<8 | uint32(buf[26])<<16 | uint32(buf[27])<<24
	if flags != syscall.IFF_UP {
		t.Fatalf("ifinfomsg.flags=%#x 期望 IFF_UP", flags)
	}
	// ifinfomsg.change（buf[28:32]）应为 IFF_UP。
	change := uint32(buf[28]) | uint32(buf[29])<<8 | uint32(buf[30])<<16 | uint32(buf[31])<<24
	if change != syscall.IFF_UP {
		t.Fatalf("ifinfomsg.change=%#x 期望 IFF_UP", change)
	}
}

// TestLinkUpRequestLayout 构造 LinkUp 的请求，断言头、ifinfomsg.flags/change
// 各就各位（seq 由 newReq 分配，可能恰为 1，不能与 IFF_UP=1 混淆）。
func TestLinkUpRequestLayout(t *testing.T) {
	r := newReq(RTM_NEWLINK, 0, ifInfoMsgIdx(7)) // 假设 ifindex=7
	r.addAttrString(IFLA_IFNAME, "lo")
	// 记录 setIFFBuf 前 seq，确认不变。
	seqBefore := getU32(r.buf[8:12])
	r.buf = setIFFBuf(r.buf, syscall.IFF_UP, syscall.IFF_UP)
	if got := getU32(r.buf[8:12]); got != seqBefore {
		t.Fatalf("setIFFBuf 改变了 nlmsghdr seq: %d -> %d", seqBefore, got)
	}
	// 头：type=RTM_NEWLINK(16)，flag 含 REQUEST|ACK。
	if got := getU16(r.buf[4:6]); got != RTM_NEWLINK {
		t.Fatalf("type=%d 期望 RTM_NEWLINK", got)
	}
	if got := getU16(r.buf[6:8]); got&NLM_F_REQUEST == 0 {
		t.Fatal("缺 NLM_F_REQUEST")
	}
	// ifinfomsg.ifindex（buf[16+4:16+8]）应为 7。
	if got := getU32(r.buf[20:24]); got != 7 {
		t.Fatalf("ifindex=%d 期望 7", got)
	}
	// ifinfomsg.flags（buf[24:28]）应为 IFF_UP。
	if got := getU32(r.buf[24:28]); got != syscall.IFF_UP {
		t.Fatalf("flags=%d 期望 IFF_UP", got)
	}
}

// TestSetLinkMasterReqTargetsSlave 回归：被挂网的链路（slave）必须是请求主体，
// 不要错把网桥 ifindex 当成主体（那样会"把网桥挂到网桥自己"→ EBUSY）。
func TestSetLinkMasterReqTargetsSlave(t *testing.T) {
	r := buildSetLinkMasterReq(10, "veth0", 20) // slave ifindex=10, boxli0 ifindex=20
	// ifinfomsg.ifindex（buf[16+4:16+8]）必须是 slave=10，而非 master=20。
	if got := getU32(r.buf[20:24]); got != 10 {
		t.Fatalf("ifinfomsg.ifindex=%d 期望 slave=10", got)
	}
	// IFLA_MASTER 属性值应为 master=20，且 IFLA_IFNAME 是 slave 名。
	attrs, err := parseAttrs(r.buf[32:])
	if err != nil {
		t.Fatal(err)
	}
	if got := trimAttrString(attrBytes(attrs, IFLA_IFNAME)); got != "veth0" {
		t.Fatalf("IFLA_IFNAME=%q 期望 veth0", got)
	}
	if got := getU32(attrBytes(attrs, IFLA_MASTER)); got != 20 {
		t.Fatalf("IFLA_MASTER=%d 期望 master=20", got)
	}
}

// TestBuildRouteMsgLayout 回归：struct rtmsg 字节要正确——rtm_type(byte7)=
// RTN_UNICAST、rtm_table(byte4)=main、flags(8:12)=0。此前 type 被盖成 0、
// UNICAST 错写进 flags → add-route 报 invalid argument。
func TestBuildRouteMsgLayout(t *testing.T) {
	b := buildRouteMsg(0) // 默认路由
	if b[0] != uint8(syscall.AF_INET) {
		t.Fatalf("family=%d 期望 AF_INET", b[0])
	}
	if b[1] != 0 {
		t.Fatalf("默认路由 dst_len=%d 期望 0", b[1])
	}
	if b[4] != RT_TABLE_MAIN {
		t.Fatalf("table=%d 期望 %d", b[4], RT_TABLE_MAIN)
	}
	if b[7] != RTN_UNICAST {
		t.Fatalf("type=%d 期望 RTN_UNICAST=%d（此前为 0 导致 EINVAL）", b[7], RTN_UNICAST)
	}
	if b[8]|b[9]|b[10]|b[11] != 0 {
		t.Fatalf("flags 应全 0，实得 %d %d %d %d", b[8], b[9], b[10], b[11])
	}
	// 显式前缀的路由：dst_len 应等于前缀。
	if b2 := buildRouteMsg(16); b2[1] != 16 {
		t.Fatalf("dst_len(16) 时=%d", b2[1])
	}
}
