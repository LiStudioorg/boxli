// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import (
	"errors"
	"testing"
)

// TestAddPIDMissingCgroup 验证组不存在时 AddPID 明确报错（正常路径需 root +
// cgroups v2，仅在此断言错误分支的契约）。
func TestAddPIDMissingCgroup(t *testing.T) {
	if !Available() {
		t.Skip("cgroups v2 不可用，跳过")
	}
	err := AddPID("does-not-exist-container", 1)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("期望 ErrUnsupported 包装，实得 %v", err)
	}
}

func TestNewCgroupPath(t *testing.T) {
	c := NewCgroup("cid123")
	if c.ContainerID != "cid123" {
		t.Fatalf("ContainerID 错误: %s", c.ContainerID)
	}
	if c.Path == "" || c.Root == "" {
		t.Fatalf("空路径: %+v", c)
	}
}
