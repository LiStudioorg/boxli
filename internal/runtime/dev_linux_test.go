// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestMinDevNodesCoversRequired 断言必需设备清单完整。
//
// 这个清单是"漏一个就在真实负载上炸"的类型，因此把预期集合显式写死：
// 谁删了其中任何一项，测试立刻失败并说明缺什么。
func TestMinDevNodesCoversRequired(t *testing.T) {
	required := []string{
		"null", "zero", "full", "random", "urandom", "tty", "ptmx",
	}
	have := map[string]bool{}
	for _, n := range minDevNodes {
		have[n] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("/dev 清单缺少必需设备 %q（当前: %v）", want, minDevNodes)
		}
	}
	// 反向检查：清单里不应有重复项。
	seen := map[string]bool{}
	for _, n := range minDevNodes {
		if seen[n] {
			t.Errorf("/dev 清单存在重复项 %q", n)
		}
		seen[n] = true
	}
}

// TestDevSymlinksTargets 断言标准符号链接的目标符合 /proc/self/fd 约定。
func TestDevSymlinksTargets(t *testing.T) {
	want := map[string]string{
		"fd":     "/proc/self/fd",
		"stdin":  "/proc/self/fd/0",
		"stdout": "/proc/self/fd/1",
		"stderr": "/proc/self/fd/2",
	}
	if len(devSymlinks) != len(want) {
		t.Fatalf("符号链接数量 %d，期望 %d: %v", len(devSymlinks), len(want), devSymlinks)
	}
	for name, target := range want {
		if got := devSymlinks[name]; got != target {
			t.Errorf("/dev/%s → %q，期望 %q", name, got, target)
		}
	}
}

// TestDevShmSize 覆盖 /dev/shm 大小的解析优先级与非法输入回落。
func TestDevShmSize(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int64
	}{
		{"default", "", defaultShmSize},
		{"explicit", "134217728", 128 << 20},
		{"small", "1048576", 1 << 20},
		{"zero-falls-back", "0", defaultShmSize},
		{"negative-falls-back", "-1", defaultShmSize},
		{"garbage-falls-back", "abc", defaultShmSize},
		{"empty-falls-back", "", defaultShmSize},
		{"overflow-falls-back", "99999999999999999999999", defaultShmSize},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envShmSize, tc.env)
			if got := devShmSize(); got != tc.want {
				t.Fatalf("devShmSize() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCreateDevSymlinks 覆盖符号链接创建：
// 目标不存在时创建，已存在（含指向别处的旧链接）时覆盖。
func TestCreateDevSymlinks(t *testing.T) {
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 预置一个指向错误目标的旧链接，验证会被覆盖。
	stale := filepath.Join(rootfs, "dev", "fd")
	if err := os.Symlink("/nonexistent", stale); err != nil {
		t.Fatal(err)
	}

	if err := createDevSymlinks(rootfs); err != nil {
		t.Fatalf("createDevSymlinks: %v", err)
	}
	for name, want := range devSymlinks {
		link := filepath.Join(rootfs, "dev", name)
		got, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("读取 /dev/%s: %v", name, err)
		}
		if got != want {
			t.Errorf("/dev/%s → %q, want %q", name, got, want)
		}
	}
}

// TestCreateDevSymlinksIdempotent 覆盖重复调用：不应因已存在而报错。
func TestCreateDevSymlinksIdempotent(t *testing.T) {
	rootfs := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootfs, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := createDevSymlinks(rootfs); err != nil {
			t.Fatalf("第 %d 次 createDevSymlinks: %v", i+1, err)
		}
	}
}

// cleanupTempRootfs 注册 TempDir 的完整拆卸：先卸载 /dev 下的所有挂载
// （字符设备 bind、shm、pts），否则 testing 的 RemoveAll 会因
// "device or resource busy" 失败。
func cleanupTempRootfs(t *testing.T, rootfs string) {
	t.Helper()
	t.Cleanup(func() {
		dev := filepath.Join(rootfs, "dev")
		_ = syscallUnmount(filepath.Join(dev, "pts"))
		_ = syscallUnmount(filepath.Join(dev, "shm"))
		for _, name := range minDevNodes {
			_ = syscallUnmount(filepath.Join(dev, name))
		}
	})
}

// TestBindHostDevicesSkipsMissing 覆盖宿主缺少某设备时的跳过行为：
// 不存在的设备不应让函数失败（宿主本身的问题，不该阻断容器）。
func TestBindHostDevicesSkipsMissing(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bind 挂载需要 root")
	}
	rootfs := t.TempDir()
	cleanupTempRootfs(t, rootfs)
	if err := os.MkdirAll(filepath.Join(rootfs, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 真实宿主一定有 /dev/null，这里只验证函数不因缺失项报错。
	if err := bindHostDevices(rootfs); err != nil {
		t.Fatalf("bindHostDevices: %v", err)
	}
	// /dev/null 必须被 bind 进来（宿主必然存在）。
	if _, err := os.Stat(filepath.Join(rootfs, "dev", "null")); err != nil {
		t.Fatalf("bind 后 /dev/null 不存在: %v", err)
	}
}

// TestSetupContainerDevFull 是 /dev 装配的集成测试（需 root）。
// 验证：字符设备就位、/dev/shm 是独立 tmpfs、/dev/pts 已挂载、
// 标准符号链接存在。
func TestSetupContainerDevFull(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("设备挂载需要 root")
	}
	rootfs := t.TempDir()
	cleanupTempRootfs(t, rootfs)
	if err := setupContainerDev(rootfs); err != nil {
		t.Fatalf("setupContainerDev: %v", err)
	}
	dev := filepath.Join(rootfs, "dev")

	// 1. 字符设备：至少 null/zero/random/urandom/tty 必须存在。
	for _, name := range []string{"null", "zero", "random", "urandom", "tty"} {
		fi, err := os.Stat(filepath.Join(dev, name))
		if err != nil {
			t.Errorf("/dev/%s 缺失: %v", name, err)
			continue
		}
		if fi.Mode()&os.ModeDevice == 0 {
			t.Errorf("/dev/%s 不是设备节点: %v", name, fi.Mode())
		}
	}

	// 2. /dev/shm 必须是挂载点（与根文件系统不同设备号）。
	assertSeparateMount(t, filepath.Join(dev, "shm"), "/dev/shm")

	// 3. /dev/pts 也必须是独立挂载点。
	assertSeparateMount(t, filepath.Join(dev, "pts"), "/dev/pts")

	// 4. 符号链接。
	for name := range devSymlinks {
		if _, err := os.Lstat(filepath.Join(dev, name)); err != nil {
			t.Errorf("/dev/%s 符号链接缺失: %v", name, err)
		}
	}

}

// assertSeparateMount 断言 path 是一个独立挂载点：其设备号与父目录不同。
// 这是判断"确实挂上了"最可靠的方式——仅看目录存在会被普通目录骗过。
func assertSeparateMount(t *testing.T, path, label string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s 不存在: %v", label, err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("%s 父目录不存在: %v", label, err)
	}
	self, ok1 := deviceNumberOf(fi)
	par, ok2 := deviceNumberOf(parent)
	if !ok1 || !ok2 {
		t.Fatalf("%s 无法读取设备号", label)
	}
	if self == par {
		t.Errorf("%s 不是独立挂载点（设备号与父目录相同 %d）", label, self)
	}
}

// TestSetupContainerDevShmSize 覆盖 /dev/shm 大小可通过环境变量配置。
func TestSetupContainerDevShmSize(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("tmpfs 挂载需要 root")
	}
	t.Setenv(envShmSize, "16777216") // 16 MiB
	rootfs := t.TempDir()
	cleanupTempRootfs(t, rootfs)
	if err := setupContainerDev(rootfs); err != nil {
		t.Fatalf("setupContainerDev: %v", err)
	}

	shm := filepath.Join(rootfs, "dev", "shm")
	// 写入超过 16MiB 应当失败（ENOSPC/EFBIG），说明 size 生效。
	big := filepath.Join(shm, "toobig")
	f, err := os.Create(big)
	if err != nil {
		t.Fatalf("在 /dev/shm 创建文件: %v", err)
	}
	defer f.Close()
	chunk := make([]byte, 1<<20)
	written := 0
	var werr error
	for range 64 { // 试图写 64 MiB
		n, err := f.Write(chunk)
		written += n
		if err != nil {
			werr = err
			break
		}
	}
	if werr == nil && written > 17<<20 {
		t.Errorf("tmpfs size 限制未生效：写入了 %d 字节（上限应约 16MiB）", written)
	}
	t.Logf("/dev/shm 写入 %d 字节后停止（err=%v）", written, werr)
	_ = os.Remove(big)
}

// TestMountRootfsVolumesUnaffected 是回归保护：确认新增 /dev 逻辑没有
// 改变卷挂载的解析与目标校验行为。
func TestMountRootfsVolumesUnaffected(t *testing.T) {
	// 没有 BOXLI_MOUNT_* 环境变量时应直接返回 nil（不挂任何东西）。
	os.Unsetenv("BOXLI_MOUNT_COUNT")
	if err := mountRootfsVolumes(t.TempDir()); err != nil {
		t.Fatalf("无卷环境时应为 no-op: %v", err)
	}
}

// deviceNumberOf 从 FileInfo 提取设备号（Unix 平台）。
func deviceNumberOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fileInfoSys(fi)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// sortedKeysOf 返回 map 的键并排序，便于稳定断言。
func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMinDevNodesStableOrder 断言清单顺序稳定：顺序影响挂载次序，
// 稳定的顺序让失败时的错误信息可复现。
func TestMinDevNodesStableOrder(t *testing.T) {
	want := []string{"null", "zero", "full", "random", "urandom", "tty", "ptmx"}
	if len(minDevNodes) != len(want) {
		t.Fatalf("清单长度 %d，期望 %d", len(minDevNodes), len(want))
	}
	for i := range want {
		if minDevNodes[i] != want[i] {
			t.Fatalf("minDevNodes[%d] = %q，期望 %q（顺序变更需同步本测试）",
				i, minDevNodes[i], want[i])
		}
	}
}
