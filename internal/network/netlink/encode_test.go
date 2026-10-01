// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import (
	"bytes"
	"testing"
)

func TestAttrAlign(t *testing.T) {
	cases := map[int]int{0: 0, 1: 4, 4: 4, 5: 8, 8: 8, 9: 12}
	for in, want := range cases {
		if got := attrAlign(in); got != want {
			t.Errorf("attrAlign(%d)=%d 期望 %d", in, got, want)
		}
	}
}

func TestByteSupports(t *testing.T) {
	// putU16/getU16 round-trip。
	b := make([]byte, 2)
	putU16(b, 0xabcd)
	if getU16(b) != 0xabcd {
		t.Fatal("putU16/getU16 不一致")
	}
	// putU32/getU32 round-trip。
	b4 := make([]byte, 4)
	putU32(b4, 0x12345678)
	if getU32(b4) != 0x12345678 {
		t.Fatal("putU32/getU32 不一致")
	}
}

func TestNetIP4(t *testing.T) {
	want := []byte{1, 2, 3, 4}
	got := netIP4("1.2.3.4")
	if !bytes.Equal(got, want) {
		t.Fatalf("netIP4(1.2.3.4)=%v 期望 %v", got, want)
	}
	if netIP4("bad") != nil {
		t.Error("非法 IP 应返回 nil")
	}
	if netIP4("1.2.3") != nil {
		t.Error("段数不足应返回 nil")
	}
}

func TestCstrAndU32(t *testing.T) {
	if string(cstr("x")) != "x\x00" {
		t.Error("cstr 缺 NUL")
	}
	if len(u32(0x1234)) != 4 {
		t.Error("u32 应 4 字节")
	}
}

func TestNestedAttrsAndAttrBytes(t *testing.T) {
	inner := nestedAttrs([]Attr{{Type: 1, Data: []byte("ab")}})
	if len(inner) < 8 {
		t.Fatalf("嵌套属性过短: %d", len(inner))
	}
	outer := nestedAttrs([]Attr{{Type: 2, Data: inner}})
	parsed, err := parseAttrs(outer)
	if err != nil {
		t.Fatal(err)
	}
	if attrBytes(parsed, 2) == nil {
		t.Error("未解析出嵌套属性")
	}
}

func TestIfInfoMsg(t *testing.T) {
	if len(ifInfoMsg()) != 16 {
		t.Fatal("ifinfomsg 应 16 字节")
	}
	if len(ifInfoMsgIdx(3)) != 16 {
		t.Fatal("ifInfoMsgIdx 应 16 字节")
	}
}

func TestParseAttrsBad(t *testing.T) {
	// 属性长度越界应报错。
	if _, err := parseAttrs([]byte{0x02, 0x00, 1, 0, 0}); err == nil {
		t.Error("非法属性长度应报错")
	}
}
