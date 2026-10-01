// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import "testing"

func TestExecOptionsValidate(t *testing.T) {
	if err := (&ExecOptions{Cmd: nil}).validate(); err == nil {
		t.Error("空命令应报错")
	}
	if err := (&ExecOptions{Cmd: []string{"/bin/sh"}}).validate(); err != nil {
		t.Fatalf("合法命令应通过: %v", err)
	}
	if err := (&ExecOptions{Cmd: []string{"/bin/sh", "a\x00b"}}).validate(); err == nil {
		t.Error("含 NUL 参数应报错")
	}
}

// TestApplyExecUserBad 验证非法 uid/gid 格式在解析阶段即报错，不触摸系统调用。
func TestApplyExecUserBad(t *testing.T) {
	for _, bad := range []string{"abc", ":2", "abc:2", "1:xyz"} {
		if err := applyExecUser(bad); err == nil {
			t.Fatalf("applyExecUser(%q) 应报错", bad)
		}
	}
	// 空串为合法（保持当前身份）。
	if err := applyExecUser(""); err != nil {
		t.Fatalf("applyExecUser(\"\") 不应报错: %v", err)
	}
}
