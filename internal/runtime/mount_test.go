// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"testing"

	"github.com/LiStudioorg/licore/internal/store"
)

func TestMountEnvRoundTrip(t *testing.T) {
	mounts := []store.Mount{
		{Source: "/host/data", Target: "/data", ReadOnly: true},
		{Source: "/host/tmp", Target: "/mnt/tmp"},
	}
	env := MountEnv(mounts)
	got, ok := parseMountEnv(env)
	if !ok {
		t.Fatal("解析失败")
	}
	if len(got) != 2 {
		t.Fatalf("挂载数错误: %d", len(got))
	}
	if got[0].Source != "/host/data" || got[0].Target != "/data" || !got[0].ReadOnly {
		t.Fatalf("首条挂载错误: %+v", got[0])
	}
	if got[1].Source != "/host/tmp" || got[1].Target != "/mnt/tmp" || got[1].ReadOnly {
		t.Fatalf("次条挂载错误: %+v", got[1])
	}
}

func TestMountEnvEmpty(t *testing.T) {
	if env := MountEnv(nil); env != nil {
		t.Fatalf("空挂载应返回 nil，实得 %v", env)
	}
	if _, ok := parseMountEnv([]string{"FOO=1"}); ok {
		t.Fatal("无明显 COUNT 不应解析成功")
	}
}

func TestSafeContainerTarget(t *testing.T) {
	if err := safeContainerTarget("/data"); err != nil {
		t.Fatalf("/data 应合法: %v", err)
	}
	if err := safeContainerTarget("/a/b/c"); err != nil {
		t.Fatalf("/a/b/c 应合法: %v", err)
	}
	for _, bad := range []string{"data", "/data/../etc", "//data", ""} {
		if err := safeContainerTarget(bad); err == nil {
			t.Fatalf("%q 应报错", bad)
		}
	}
}
