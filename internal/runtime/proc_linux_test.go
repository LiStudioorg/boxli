// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mountinfoLine 按真实 /proc/self/mountinfo 的字段布局拼一行。
// 第 5 个字段是挂载点，第 6 个是 per-mount 选项（hidepid 就在这里）。
func mountinfoLine(mountPoint, opts string) string {
	return "26 31 0:24 / " + mountPoint + " " + opts + " shared:12 - proc proc rw"
}

// TestParseHidePID 覆盖 hidepid 解析的全部输入形态。
func TestParseHidePID(t *testing.T) {
	cases := []struct {
		name       string
		mountinfo  string
		wantVal    int
		wantFound  bool
		wantParsed bool
	}{
		{
			name:       "unset",
			mountinfo:  mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime"),
			wantFound:  false,
			wantParsed: true,
		},
		{
			name:       "zero",
			mountinfo:  mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=0"),
			wantVal:    0,
			wantFound:  true,
			wantParsed: true,
		},
		{
			name:       "one",
			mountinfo:  mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=1"),
			wantVal:    1,
			wantFound:  true,
			wantParsed: true,
		},
		{
			name:       "two",
			mountinfo:  mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=2"),
			wantVal:    2,
			wantFound:  true,
			wantParsed: true,
		},
		{
			name:       "hidepid-first-option",
			mountinfo:  mountinfoLine("/proc", "hidepid=2,rw,nosuid"),
			wantVal:    2,
			wantFound:  true,
			wantParsed: true,
		},
		{
			name:       "garbage-value",
			mountinfo:  mountinfoLine("/proc", "rw,hidepid=abc"),
			wantFound:  true,
			wantParsed: false,
		},
		{
			name:       "empty-value",
			mountinfo:  mountinfoLine("/proc", "rw,hidepid="),
			wantFound:  true,
			wantParsed: false,
		},
		{
			name:       "no-proc-line",
			mountinfo:  mountinfoLine("/sys", "rw,nosuid,hidepid=2"),
			wantFound:  false,
			wantParsed: true,
		},
		{
			name:       "empty-input",
			mountinfo:  "",
			wantFound:  false,
			wantParsed: true,
		},
		{
			name:       "short-line",
			mountinfo:  "26 31 0:24 / /proc",
			wantFound:  false,
			wantParsed: true,
		},
		{
			name: "proc-line-among-others",
			mountinfo: strings.Join([]string{
				mountinfoLine("/", "rw,relatime"),
				mountinfoLine("/sys", "rw,nosuid,nodev,noexec,relatime"),
				mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=2"),
				mountinfoLine("/dev", "rw,nosuid,relatime"),
			}, "\n"),
			wantVal:    2,
			wantFound:  true,
			wantParsed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val, found, parsed := parseHidePID(tc.mountinfo)
			if found != tc.wantFound || parsed != tc.wantParsed {
				t.Fatalf("found=%v parsed=%v, want found=%v parsed=%v",
					found, parsed, tc.wantFound, tc.wantParsed)
			}
			if parsed && found && val != tc.wantVal {
				t.Fatalf("val=%d, want %d", val, tc.wantVal)
			}
		})
	}
}

// TestProcMountOptions 覆盖决策表：什么情况下传 hidepid=0。
func TestProcMountOptions(t *testing.T) {
	cases := []struct {
		name      string
		mountinfo string
		want      string
	}{
		{"unset-uses-default", mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime"), ""},
		{"zero-uses-default", mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=0"), ""},
		{"one-overridden", mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=1"), "hidepid=0"},
		{"two-overridden", mountinfoLine("/proc", "rw,nosuid,nodev,noexec,relatime,hidepid=2"), "hidepid=0"},
		{"garbage-uses-default", mountinfoLine("/proc", "rw,hidepid=abc"), ""},
		{"no-proc-uses-default", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := procMountOptions(tc.mountinfo); got != tc.want {
				t.Fatalf("procMountOptions() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReadHostMountInfoRealHost 在真实宿主上读取：必须能读到 /proc 行。
func TestReadHostMountInfoRealHost(t *testing.T) {
	info := readHostMountInfo()
	if info == "" {
		t.Fatal("真实宿主应能读到 mountinfo")
	}
	if !strings.Contains(info, " /proc ") {
		t.Fatalf("mountinfo 中应含 /proc 挂载行")
	}
	_, _, ok := parseHidePID(info)
	if !ok {
		t.Fatalf("真实宿主 mountinfo 应能被解析")
	}
	t.Logf("真实宿主 /proc 挂载参数: %q", procMountOptions(info))
}

// TestReadHostMountInfoMissing 覆盖 mountinfo 不存在：
// 返回空串，决策退化为"不传参数"，不阻断。
func TestReadHostMountInfoMissing(t *testing.T) {
	old := hostProcMountInfo
	hostProcMountInfo = filepath.Join(t.TempDir(), "nope")
	t.Cleanup(func() { hostProcMountInfo = old })

	if got := readHostMountInfo(); got != "" {
		t.Fatalf("缺失文件应返回空串, got %q", got)
	}
	if got := procMountOptions(readHostMountInfo()); got != "" {
		t.Fatalf("读不到 mountinfo 时应不传参数, got %q", got)
	}
}

// TestMountContainerProcOptionSelection 覆盖挂载参数选择：
// 用注入的假挂载函数记录实际传入的 opts。
func TestMountContainerProcOptionSelection(t *testing.T) {
	cases := []struct {
		name       string
		mountinfo  string
		wantFirst  string
		wantMounts int
	}{
		{"hidepid2-sends-hidepid0", mountinfoLine("/proc", "rw,hidepid=2"), "hidepid=0", 1},
		{"hidepid1-sends-hidepid0", mountinfoLine("/proc", "rw,hidepid=1"), "hidepid=0", 1},
		{"unset-sends-empty", mountinfoLine("/proc", "rw,relatime"), "", 1},
		{"hidepid0-sends-empty", mountinfoLine("/proc", "rw,hidepid=0"), "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootfs := t.TempDir()
			if err := os.MkdirAll(filepath.Join(rootfs, "proc"), 0o755); err != nil {
				t.Fatal(err)
			}
			oldInfo, oldMount := hostProcMountInfo, mountProcRaw
			hostProcMountInfo = writeTempMountInfo(t, tc.mountinfo)
			var got []string
			mountProcRaw = func(target, opts string) error {
				got = append(got, opts)
				return nil
			}
			t.Cleanup(func() {
				hostProcMountInfo = oldInfo
				mountProcRaw = oldMount
			})

			if err := mountContainerProc(rootfs); err != nil {
				t.Fatalf("mountContainerProc: %v", err)
			}
			if len(got) != tc.wantMounts {
				t.Fatalf("挂载调用次数 %d, want %d (opts=%v)", len(got), tc.wantMounts, got)
			}
			if got[0] != tc.wantFirst {
				t.Fatalf("首次挂载 opts = %q, want %q", got[0], tc.wantFirst)
			}
		})
	}
}

// TestMountContainerProcFallback 覆盖回退：带 hidepid 挂载失败时，
// 必须不带参数重试；两次都失败才报错。
func TestMountContainerProcFallback(t *testing.T) {
	t.Run("fallback-succeeds", func(t *testing.T) {
		rootfs := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootfs, "proc"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldInfo, oldMount := hostProcMountInfo, mountProcRaw
		hostProcMountInfo = writeTempMountInfo(t, mountinfoLine("/proc", "rw,hidepid=2"))
		var got []string
		mountProcRaw = func(target, opts string) error {
			got = append(got, opts)
			if opts != "" {
				return errors.New("hidepid not supported")
			}
			return nil
		}
		t.Cleanup(func() { hostProcMountInfo = oldInfo; mountProcRaw = oldMount })

		if err := mountContainerProc(rootfs); err != nil {
			t.Fatalf("回退后应成功: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("应有 2 次挂载尝试, got %d: %v", len(got), got)
		}
		if got[0] != "hidepid=0" || got[1] != "" {
			t.Fatalf("回退顺序错误: %v（应先 hidepid=0 再空）", got)
		}
	})

	t.Run("both-fail-reports-error", func(t *testing.T) {
		rootfs := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootfs, "proc"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldInfo, oldMount := hostProcMountInfo, mountProcRaw
		hostProcMountInfo = writeTempMountInfo(t, mountinfoLine("/proc", "rw,hidepid=2"))
		mountProcRaw = func(target, opts string) error { return errors.New("always fails") }
		t.Cleanup(func() { hostProcMountInfo = oldInfo; mountProcRaw = oldMount })

		err := mountContainerProc(rootfs)
		if err == nil {
			t.Fatal("两次都失败时应报错")
		}
		if !strings.Contains(err.Error(), "/proc") {
			t.Fatalf("错误信息应包含 /proc: %v", err)
		}
	})

	t.Run("no-opts-failure-errors-once", func(t *testing.T) {
		// 不需要 hidepid 时不应有多余的重试。
		rootfs := t.TempDir()
		if err := os.MkdirAll(filepath.Join(rootfs, "proc"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldInfo, oldMount := hostProcMountInfo, mountProcRaw
		hostProcMountInfo = writeTempMountInfo(t, mountinfoLine("/proc", "rw,relatime"))
		calls := 0
		mountProcRaw = func(target, opts string) error { calls++; return errors.New("fail") }
		t.Cleanup(func() { hostProcMountInfo = oldInfo; mountProcRaw = oldMount })

		if err := mountContainerProc(rootfs); err == nil {
			t.Fatal("应报错")
		}
		if calls != 1 {
			t.Fatalf("无需 hidepid 时只应尝试 1 次, got %d", calls)
		}
	})
}

// writeTempMountInfo 把给定内容写成临时 mountinfo 文件，返回其路径。
func writeTempMountInfo(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 mountinfo: %v", err)
	}
	return path
}

// TestMountContainerProcRealHost 在真实宿主上实际挂载一次 /proc（需 root）。
// 验证：挂载成功、是独立挂载点、能被卸载且不留残留。
func TestMountContainerProcRealHost(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("/proc 挂载需要 root")
	}
	rootfs := t.TempDir()
	procDir := filepath.Join(rootfs, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscallUnmount(procDir) })

	if err := mountContainerProc(rootfs); err != nil {
		t.Fatalf("mountContainerProc: %v", err)
	}
	// 必须是独立挂载点（设备号与父目录不同）。
	assertSeparateMount(t, procDir, "/proc")
	// 挂载后应能看到真实的 procfs 内容。
	if _, err := os.Stat(filepath.Join(procDir, "self", "status")); err != nil {
		t.Fatalf("容器 /proc/self/status 不可读: %v", err)
	}
	// 卸载必须成功（无残留）。
	if err := syscallUnmount(procDir); err != nil {
		t.Fatalf("卸载 /proc: %v", err)
	}
}
