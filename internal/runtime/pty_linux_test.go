// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// ptyAvailable 判断当前环境能否真的开 pty（需要 /dev/ptmx 且可访问）。
//
// 注意：开 /dev/ptmx **不需要 root**——pty 是普通用户可用的设施。
// 若在受限沙箱里 /dev/ptmx 不可用，则跳过而不是失败。
func ptyAvailable() bool {
	f, err := os.OpenFile(devPtmxPath, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// TestOpenptyAllocatesRealPTY 是 TIOCSPTLCK 指针语义的**回归测试**。
//
// 历史缺陷：TIOCSPTLCK 的第三个参数是**指向 int 的指针**，内核会从该地址
// 读 4 字节。旧代码传字面量 0，等于让内核解引用地址 0，返回 EFAULT，
// 表现为 `exec -it` 恒报 "openpty: bad address"。
//
// 本测试直接调用 openpty 并断言返回的两个 fd 都是终端，这样一旦有人把
// 参数改回字面量 0，测试立刻失败。
func TestOpenptyAllocatesRealPTY(t *testing.T) {
	if !ptyAvailable() {
		t.Skipf("%s 不可用，跳过 pty 分配测试", devPtmxPath)
	}
	master, slave, err := openpty()
	if err != nil {
		t.Fatalf("openpty 失败（若为 bad address，说明 TIOCSPTLCK 参数传错）: %v", err)
	}
	defer func() { _ = master.Close(); _ = slave.Close() }()

	if !isTerminal(master.Fd()) {
		t.Error("master 不是终端")
	}
	if !isTerminal(slave.Fd()) {
		t.Error("slave 不是终端（TIOCSPTLCK/打开从端有问题）")
	}
	// 从端必须在 devpts 目录下且名字为数字。
	name := slave.Name()
	if dir := filepath.Dir(name); dir != devPtsDir {
		t.Errorf("从端路径 %q 不在 %q 下", name, devPtsDir)
	}
	if base := filepath.Base(name); base == "" || base == "0" {
		t.Errorf("从端名异常: %q", base)
	}
}

// TestOpenptyTwoAllocationsDiffer 验证连续分配拿到不同从端号，
// 即 TIOCGPTN（取从端号）确实读到了内核写入的值。
func TestOpenptyTwoAllocationsDiffer(t *testing.T) {
	if !ptyAvailable() {
		t.Skipf("%s 不可用", devPtmxPath)
	}
	m1, s1, err := openpty()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m1.Close(); _ = s1.Close() }()
	m2, s2, err := openpty()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m2.Close(); _ = s2.Close() }()

	if s1.Name() == s2.Name() {
		t.Errorf("两次分配得到同一起端 %q；TIOCGPTN 可能未生效", s1.Name())
	}
}

// TestOpenptyBadPtmxReportsError 覆盖打开 /dev/ptmx 失败的路径：
// 必须返回带路径的清晰错误，而不是 panic 或裸 errno。
func TestOpenptyBadPtmxReportsError(t *testing.T) {
	oldM, oldD := devPtmxPath, devPtsDir
	devPtmxPath = filepath.Join(t.TempDir(), "nonexistent-ptmx")
	devPtsDir = t.TempDir()
	t.Cleanup(func() { devPtmxPath, devPtsDir = oldM, oldD })

	m, s, err := openpty()
	if err == nil {
		_ = m.Close()
		_ = s.Close()
		t.Fatal("ptmx 不存在时应报错")
	}
	if m != nil || s != nil {
		t.Error("失败时不应返回文件句柄")
	}
}

// TestOpenptySlaveOpenFailure 覆盖 ptmx 可用但从端打不开的情形：
// 把 devPtsDir 指向错误目录，验证错误被上报且 master 被关闭（不泄漏 fd）。
func TestOpenptySlaveOpenFailure(t *testing.T) {
	if !ptyAvailable() {
		t.Skipf("%s 不可用", devPtmxPath)
	}
	oldD := devPtsDir
	devPtsDir = filepath.Join(t.TempDir(), "no-such-pts")
	t.Cleanup(func() { devPtsDir = oldD })

	// 记录调用前的 fd 数，验证 master 被正确关闭。
	before := countOpenFDs(t)
	m, s, err := openpty()
	if err == nil {
		_ = m.Close()
		_ = s.Close()
		t.Fatal("从端目录不存在时应报错")
	}
	if m != nil || s != nil {
		t.Error("失败时不应返回句柄")
	}
	after := countOpenFDs(t)
	if after > before {
		t.Errorf("失败路径泄漏了 fd：before=%d after=%d", before, after)
	}
}

// countOpenFDs 统计当前进程打开的 fd 数量。
func countOpenFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("无法读取 /proc/self/fd: %v", err)
	}
	return len(ents)
}

// isTerminal 判断 fd 是否终端。
//
// 用 ioctl(TCGETS) 判定：内核把 termios 写入我们提供的缓冲区，成功即说明
// 是终端。必须传**有效指针**——传 nil 会得到 EFAULT，与"不是终端"的
// ENOTTY 无法区分，那正是本项目在 TIOCSPTLCK 上踩过的同一类坑。
//
// 不引用 x/term 等外部依赖（本项目禁止第三方依赖），直接用 syscall + unsafe。
func isTerminal(fd uintptr) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, tcgets,
		uintptr(unsafe.Pointer(&t)))
	runtime.KeepAlive(&t)
	return errno == 0
}

// tcgets 是 TCGETS 的 ioctl 号（取终端属性，成功即说明是终端）。
const tcgets = 0x5401
