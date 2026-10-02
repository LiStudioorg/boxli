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
