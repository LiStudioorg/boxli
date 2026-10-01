// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import "testing"

func TestCgroupEnv(t *testing.T) {
	env := CgroupEnv("mycontainer123")
	if len(env) != 1 || env[0] != "BOXLI_CGROUP_ID=mycontainer123" {
		t.Fatalf("CgroupEnv 错误: %v", env)
	}
	if got := cgroupIDFromEnv(env); got != "mycontainer123" {
		t.Fatalf("cgroupIDFromEnv 回读错误: %q", got)
	}
	if got := cgroupIDFromEnv([]string{"FOO=1"}); got != "" {
		t.Fatalf("无标记应回空: %q", got)
	}
}
