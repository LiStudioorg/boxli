// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"strconv"
	"testing"
)

func TestChildCmdline(t *testing.T) {
	argv := []string{"/bin/sh", "-c", "echo hi"}
	for i, a := range argv {
		t.Setenv(envChildCmdPrefix+strconv.Itoa(i), a)
	}
	t.Setenv(envChildCmdCountKey, strconv.Itoa(len(argv)))

	got, err := childCmdline()
	if err != nil {
		t.Fatalf("childCmdline: %v", err)
	}
	if len(got) != len(argv) {
		t.Fatalf("参数个数 %d，期望 %d", len(got), len(argv))
	}
	for i := range argv {
		if got[i] != argv[i] {
			t.Fatalf("argv[%d]=%q 期望 %q", i, got[i], argv[i])
		}
	}
}

func TestChildCmdlineBad(t *testing.T) {
	// argc 非数字
	t.Setenv(envChildCmdCountKey, "abc")
	if _, err := childCmdline(); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("argc=abc 应报 ErrBadConfig，实得 %v", err)
	}
	// argc 超出实际提供的参数
	t.Setenv(envChildCmdCountKey, "3")
	for i := range 2 {
		t.Setenv(envChildCmdPrefix+strconv.Itoa(i), "x")
	}
	if _, err := childCmdline(); !errors.Is(err, ErrBadConfig) {
		t.Fatalf("缺参数应报 ErrBadConfig，实得 %v", err)
	}
}
