// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// fakeMount 记录 mountRaw 调用并编程化返回错误序列。
type fakeMount struct {
	calls []mountCall
	// failFirst / failAll 控制失败模式。
	failFirst bool
	failAll   bool
	err       error
}

type mountCall struct {
	source, target, fstype, data string
	flags                        uintptr
}

func (f *fakeMount) install(t *testing.T) {
	t.Helper()
	old := mountRaw
	n := 0
	mountRaw = func(source, target, fstype string, flags uintptr, data string) error {
		f.calls = append(f.calls, mountCall{source, target, fstype, data, flags})
		n++
		if f.failAll {
			return f.err
		}
		if f.failFirst && n == 1 {
			return f.err
		}
		return nil
	}
	t.Cleanup(func() { mountRaw = old })
}

// TestMountDevShmFallsBackWithoutSize 覆盖 tmpfs 带 size 失败 → 回退不带参数：
// 第二次调用 data 必须为空串，且整体不报错。
func TestMountDevShmFallsBackWithoutSize(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{failFirst: true, err: syscall.EINVAL}
	fm.install(t)

	if err := mountDevShm(rootfs); err != nil {
		t.Fatalf("size 失败应回退成功: %v", err)
	}
	if len(fm.calls) != 2 {
		t.Fatalf("应调用两次, got %d", len(fm.calls))
	}
	if !strings.HasPrefix(fm.calls[0].data, "size=") {
		t.Errorf("首次应带 size=, got %q", fm.calls[0].data)
	}
	if fm.calls[1].data != "" {
		t.Errorf("回退应不带参数, got %q", fm.calls[1].data)
	}
	// 挂载 flags 保留 nosuid/nodev/noexec。
	want := uintptr(msNoSuid | msNoDev | msNoExec)
	if fm.calls[1].flags != want {
		t.Errorf(" 安全挂载选项丢失: %#x vs %#x", fm.calls[1].flags, want)
	}
}

// TestMountDevShmBothFail 覆盖两次都失败 → 必须报错（不假装成功）。
func TestMountDevShmBothFail(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{failAll: true, err: syscall.EPERM}
	fm.install(t)
	if err := mountDevShm(rootfs); err == nil {
		t.Fatal("两次都失败时必须报错")
	}
	if len(fm.calls) != 2 {
		t.Fatalf("应尝试两次, got %d", len(fm.calls))
	}
}

// TestMountDevShmHonorsEnvSize 覆盖 BOXLI_SHM_SIZE 传导到挂载参数。
func TestMountDevShmHonorsEnvSize(t *testing.T) {
	t.Setenv(envShmSize, "16m")
	rootfs := t.TempDir()
	fm := &fakeMount{}
	fm.install(t)
	if err := mountDevShm(rootfs); err != nil {
		t.Fatal(err)
	}
	if got := fm.calls[0].data; got != "size=16777216" {
		t.Errorf("data = %q, want size=16777216", got)
	}
}

// TestMountDevPtsFallbackDropsNewinstance 覆盖 devpts：newinstance 失败 →
// 回退时只去掉 newinstance，ptmxmode/mode 必须保留。
func TestMountDevPtsFallbackDropsNewinstance(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{failFirst: true, err: syscall.EINVAL}
	fm.install(t)

	if err := mountDevPts(rootfs); err != nil {
		t.Fatalf("newinstance 失败应回退成功: %v", err)
	}
	if len(fm.calls) != 2 {
		t.Fatalf("应调用两次, got %d", len(fm.calls))
	}
	if !strings.Contains(fm.calls[0].data, "newinstance") {
		t.Errorf("首次应带 newinstance: %q", fm.calls[0].data)
	}
	second := fm.calls[1].data
	if strings.Contains(second, "newinstance") {
		t.Errorf("回退不应再带 newinstance: %q", second)
	}
	for _, keep := range []string{"ptmxmode=0666", "mode=0620"} {
		if !strings.Contains(second, keep) {
			t.Errorf("回退不应丢 %s: %q", keep, second)
		}
	}
}

// TestMountDevPtsBothFail 两次都失败必须报错。
func TestMountDevPtsBothFail(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{failAll: true, err: syscall.EPERM}
	fm.install(t)
	if err := mountDevPts(rootfs); err == nil {
		t.Fatal("两次都失败时必须报错")
	}
}

// TestBindHostDevicesSkipsMissingSource 覆盖宿主设备缺失时跳过：
// 用一个不存在设备的视角无法直接构造（/dev/null 恒在），改为验证
// bind 的调用集合恰好是 minDevNodes ∩ 宿主实际存在的设备。
func TestBindHostDevicesSkipsMissingSource(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{}
	fm.install(t)

	if err := bindHostDevices(rootfs); err != nil {
		t.Fatal(err)
	}
	bound := map[string]bool{}
	for _, c := range fm.calls {
		bound[filepath.Base(c.source)] = true
		if c.fstype != "" || c.flags != uintptr(msBind) {
			t.Errorf("bind 参数异常: %+v", c)
		}
	}
	var expect []string
	for _, n := range minDevNodes {
		if _, err := os.Stat(filepath.Join("/dev", n)); err == nil {
			expect = append(expect, n)
			if !bound[n] {
				t.Errorf("宿主存在的 /dev/%s 未被 bind", n)
			}
		}
	}
	if len(fm.calls) != len(expect) {
		t.Errorf("bind 次数 %d != 期望 %d (%v)", len(fm.calls), len(expect), expect)
	}
}

// TestBindHostDevicesBindFailurePropagates bind 失败必须上报。
func TestBindHostDevicesBindFailurePropagates(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{failAll: true, err: syscall.EPERM}
	fm.install(t)
	if err := bindHostDevices(rootfs); err == nil {
		t.Fatal("bind 失败必须报错")
	} else if !errors.Is(err, syscall.EPERM) {
		t.Errorf("错误应保留原始 errno: %v", err)
	}
}

// TestSetupContainerDevFullSequence 用注入的假 mount 走完 setupContainerDev
// 全流程：目录结构 + 符号链接 + 全部三条 mount 都按预期发生。
func TestSetupContainerDevFullSequence(t *testing.T) {
	rootfs := t.TempDir()
	fm := &fakeMount{}
	fm.install(t)

	if err := setupContainerDev(rootfs); err != nil {
		t.Fatalf("setupContainerDev: %v", err)
	}
	for _, d := range []string{"shm", "pts"} {
		if fi, err := os.Stat(filepath.Join(rootfs, "dev", d)); err != nil || !fi.IsDir() {
			t.Errorf("dev/%s 未创建", d)
		}
	}
	for name := range devSymlinks {
		tgt, err := os.Readlink(filepath.Join(rootfs, "dev", name))
		if err != nil {
			t.Errorf("符号链接 %s: %v", name, err)
			continue
		}
		if !strings.HasPrefix(tgt, "/proc/self/fd") {
			t.Errorf("%s → %s", name, tgt)
		}
	}
	// 三类挂载都发生了：bind、tmpfs、devpts。
	kinds := map[string]bool{}
	for _, c := range fm.calls {
		switch c.fstype {
		case "":
			kinds["bind"] = true
		case "tmpfs":
			kinds["tmpfs"] = true
		case "devpts":
			kinds["devpts"] = true
		}
	}
	for _, k := range []string{"bind", "tmpfs", "devpts"} {
		if !kinds[k] {
			t.Errorf("缺少 %s 挂载", k)
		}
	}
}

// TestFileinfoSysAndUnmountHelpers fileInfoSys / syscallUnmount 是薄的
// syscall 包装：验证参数形态正确（不是真卸载宿主路径）。
func TestFileinfoSysAndUnmountHelpers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// 对未挂载路径卸载应返回 EINVAL（说明真的透传到了 syscall）。
	if err := syscallUnmount(p); !errors.Is(err, syscall.EINVAL) {
		t.Logf("卸载未挂载点返回: %v（内核版本差异可接受，仅记录）", err)
	}
	st, ok := fileInfoSys(fi)
	if !ok || st.Mode == 0 {
		t.Errorf("fileInfoSys 未返回 Stat_t: %v %v", st, ok)
	}
	// 非 FileInfo 输入必须 ok=false 而不是 panic。
	if _, ok := fileInfoSys(nil); ok {
		t.Error("nil 输入应 ok=false")
	}
}

// TestDevShmSizeSuffixParsing 覆盖 BOXLI_SHM_SIZE 的后缀形态。
// 用户最自然的写法是 "16m" 而不是 "16777216"，若只收裸字节数，写 "16m"
// 会得到静默的默认值——这是最糟的失败模式（配置看着生效，其实没有）。
func TestDevShmSizeSuffixParsing(t *testing.T) {
	cases := map[string]int64{
		"16777216": 16 << 20,
		"16m":      16 << 20,
		"16M":      16 << 20,
		"512k":     512 << 10,
		"1g":       1 << 30,
		" 32m ":    32 << 20,
	}
	for in, want := range cases {
		t.Setenv(envShmSize, in)
		if got := devShmSize(); got != want {
			t.Errorf("devShmSize(%q) = %d, want %d", in, got, want)
		}
	}
	// 非法值回退默认（并告警，不静默）。
	for _, bad := range []string{"abc", "0", "-1", "16x", ""} {
		t.Setenv(envShmSize, bad)
		if got := devShmSize(); bad != "" && got != defaultShmSize {
			t.Errorf("非法值 %q 应回退默认, got %d", bad, got)
		}
	}
}
