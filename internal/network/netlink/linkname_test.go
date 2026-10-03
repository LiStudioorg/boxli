// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package netlink

import "testing"

// TestTrimAttrString 验证内核返回的 NUL 结尾字符串属性被正确去除填充。
func TestTrimAttrString(t *testing.T) {
	cases := map[string]string{
		"lo\x00":      "lo",
		"licore0\x00": "licore0",
		"abc\x00\x00": "abc",
		"plain":       "plain",
		"":            "",
	}
	for in, want := range cases {
		if got := trimAttrString([]byte(in)); got != want {
			t.Errorf("trimAttrString(%q)=%q 期望 %q", in, got, want)
		}
	}
}

// TestLinkByNameLoop 回归：内核 IFLA_IFNAME 是 NUL 结尾，LinkByName 曾因此
// 对已存在接口误报"链路不存在"（曾导致网桥幂等与 veth 装配失败）。
func TestLinkByNameLoop(t *testing.T) {
	l, err := LinkByName("lo")
	if err != nil {
		t.Fatalf("LinkByName(lo)=%v（lo 一定存在，误报即回归 bug）", err)
	}
	if l.IfIndex <= 0 || len(l.Name) == 0 {
		t.Fatalf("lo 返回值异常: %+v", l)
	}
	// 不存在的接口应明确报错。
	if _, err := LinkByName("definitely-not-a-real-link-xyz"); err == nil {
		t.Error("不存在接口应报错")
	}
}
