// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"syscall"
	"testing"
)

func TestAddAttrU32U16(t *testing.T) {
	r := newReq(RTM_NEWLINK, 0, ifInfoMsg())
	r.addAttrU32(0x10, 0x01020304)
	r.addAttrU16(0x11, 0x5566)
	attrs, err := parseAttrs(r.buf[16+16:])
	if err != nil {
		t.Fatalf("parseAttrs: %v", err)
	}
	if got := getU32(attrBytes(attrs, 0x10)); got != 0x01020304 {
		t.Fatalf("u32 attr=%#x", got)
	}
	if got := getU16(attrBytes(attrs, 0x11)); got != 0x5566 {
		t.Fatalf("u16 attr=%#x", got)
	}
}

func TestOpErrorString(t *testing.T) {
	e := &OpError{Op: "add-link", Errno: syscall.EEXIST}
	if s := e.Error(); s == "" || s != "rtnetlink add-link: file exists" {
		t.Fatalf("OpError.Error()=%q", s)
	}
}

func TestOpNameAndMsgType(t *testing.T) {
	if opName(RTM_NEWLINK) == "" {
		t.Fatal("opName 空")
	}
	if msgTypeOf(newReq(RTM_DELLINK, 0, nil).buf) != RTM_DELLINK {
		t.Fatalf("msgTypeOf 未识别 RTM_DELLINK")
	}
}
