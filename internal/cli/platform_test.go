// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// withFakeAndroid 用探测缝模拟（或否定）Android 身份。
//
// 全部用例都不依赖真机：平台串只吃探测结果与 GOARCH。
func withFakeAndroid(t *testing.T, isAndroid, root bool, err error) {
	t.Helper()
	prev := detectAndroidEnv
	detectAndroidEnv = func() (androidInfo, error) {
		if err != nil {
			return androidInfo{}, err
		}
		return androidInfo{IsAndroid: isAndroid, Root: root}, nil
	}
	t.Cleanup(func() { detectAndroidEnv = prev })
}

// TestPlatformSuffixNonAndroid 覆盖需求 5：非 Android 平台必须显示真实
// GOOS/GOARCH，且**绝不能**出现 "android" 字样。
func TestPlatformSuffixNonAndroid(t *testing.T) {
	withFakeAndroid(t, false, false, nil)

	got := platformSuffix()
	want := "(" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got != want {
		t.Fatalf("platformSuffix() = %q，期望 %q", got, want)
	}
	if strings.Contains(got, "android") {
		t.Fatalf("非 Android 平台不该出现 android 字样：%q", got)
	}

	// linux 宿主上也必须是 linux/...，不得因为"编译支持 Android"就误报。
	if runtime.GOOS == "linux" && !strings.HasPrefix(got, "(linux/") {
		t.Fatalf("linux 宿主应显示 linux/，实际 %q", got)
	}
}

// TestPlatformSuffixAndroid 覆盖需求 2 的三种 Android 形态。
func TestPlatformSuffixAndroid(t *testing.T) {
	cases := []struct {
		name string
		root bool
		want string
	}{
		{"root", true, "(android/" + runtime.GOARCH + ", root)"},
		{"非 root", false, "(android/" + runtime.GOARCH + ", non-root)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeAndroid(t, true, tc.root, nil)
			if got := platformSuffix(); got != tc.want {
				t.Fatalf("platformSuffix() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestPlatformSuffixProbeErrorFallsBack 覆盖探测失败：必须安全回落到编译期
// 平台，绝不 panic、绝不猜成 android。
func TestPlatformSuffixProbeErrorFallsBack(t *testing.T) {
	withFakeAndroid(t, false, false, errors.New("探测爆炸"))

	got := platformSuffix()
	want := "(" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got != want {
		t.Fatalf("探测失败应回落为 %q，实际 %q", want, got)
	}
}

// TestVersionString 覆盖需求 1/2 的组合输出形态。
func TestVersionString(t *testing.T) {
	t.Run("Android root", func(t *testing.T) {
		withFakeAndroid(t, true, true, nil)
		got := versionString("0.7.0")
		want := "0.7.0 (android/" + runtime.GOARCH + ", root)"
		if got != want {
			t.Fatalf("versionString = %q，期望 %q", got, want)
		}
	})
	t.Run("Android 非 root", func(t *testing.T) {
		withFakeAndroid(t, true, false, nil)
		got := versionString("0.7.0")
		want := "0.7.0 (android/" + runtime.GOARCH + ", non-root)"
		if got != want {
			t.Fatalf("versionString = %q，期望 %q", got, want)
		}
	})
	t.Run("服务器", func(t *testing.T) {
		withFakeAndroid(t, false, false, nil)
		got := versionString("0.7.0")
		want := "0.7.0 (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
		if got != want {
			t.Fatalf("versionString = %q，期望 %q", got, want)
		}
	})
	t.Run("空版本号占位", func(t *testing.T) {
		withFakeAndroid(t, false, false, nil)
		if got := versionString(""); !strings.HasPrefix(got, "dev ") {
			t.Fatalf("空版本号应以 dev 占位，实际 %q", got)
		}
	})
}

// TestRootCommandVersionIncludesPlatform 锁定端到端契约：cobra 的
// --version 输出必须带上平台标识（而不是裸版本号）。
func TestRootCommandVersionIncludesPlatform(t *testing.T) {
	withFakeAndroid(t, false, false, nil)

	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version 执行失败: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "("+runtime.GOOS+"/"+runtime.GOARCH+")") {
		t.Fatalf("--version 输出缺少平台标识：%q", got)
	}
	if strings.Contains(got, "android") {
		t.Fatalf("本机 --version 不该出现 android：%q", got)
	}
}

// TestRootCommandVersionOnAndroid 锁定 Android 上的端到端输出。
func TestRootCommandVersionOnAndroid(t *testing.T) {
	withFakeAndroid(t, true, true, nil)

	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version 执行失败: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "(android/"+runtime.GOARCH+", root)") {
		t.Fatalf("Android --version 输出不符：%q", got)
	}
}
