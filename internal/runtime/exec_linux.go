// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/LiStudioorg/boxli/internal/execns"
)

// pty ioctl 常量（syscall 包未导出）。编码：dir<<30 | size<<16 | type<<8 | nr。
//
//	TIOCSPTLCK：向内核**写入** 4 字节（dir=1），参数是 int 指针，指向 0 = 解锁；
//	TIOCGPTN ：从内核**读出** 4 字节（dir=2），参数是 uint32 指针，返回从端号。
const (
	tiocsptlck = 0x40045431
	tiocgptn   = 0x80045430
)

// ExecOptions 定义见 config.go（跨平台）。

// Exec 在目标容器的命名空间里执行命令并等待退出，返回退出码（信号死亡时
// 128+signum）。需要 root（CAP_SYS_ADMIN）以 setns 进入他人命名空间。
//
// setns(mount namespace) 在纯 Go 下会失败（Go issue #9091），因此进入容器
// 命名空间由 internal/execns 完成：cgo 构建时用 fork 出的 C 单线程子进程
// setns+exec；无 cgo 构建返回 ErrNoCgoExec。
func Exec(o *ExecOptions) (int, error) {
	if err := o.validate(); err != nil {
		return -1, err
	}
	if o.TargetPID <= 0 {
		return -1, fmt.Errorf("exec: target PID 非法: %w", ErrBadConfig)
	}
	if os.Geteuid() != 0 {
		return -1, ErrNotRoot
	}
	if !execns.Enabled() {
		return -1, fmt.Errorf("exec: %w", execns.ErrNoCgoExec)
	}

	// 决定三个 stdio fd。
	stdin := firstNonNil(o.Stdin, os.Stdin)
	stdout := firstNonNil(o.Stdout, os.Stdout)
	stderr := firstNonNil(o.Stderr, os.Stderr)
	inFd := int(stdin.Fd())
	outFd := int(stdout.Fd())
	errFd := int(stderr.Fd())

	// TTY：开伪终端，slave 作为子进程 stdio，master 由本进程转发给调用方。
	var master *os.File
	if o.TTY {
		m, s, err := openpty()
		if err != nil {
			return -1, fmt.Errorf("openpty: %w", err)
		}
		master = m
		defer func() { _ = s.Close(); _ = master.Close() }()
		inFd, outFd, errFd = int(s.Fd()), int(s.Fd()), int(s.Fd())
	}

	pid, err := execns.Enter(o.TargetPID, o.Workdir, o.User, o.Env, inFd, outFd, errFd, o.Cmd)
	if err != nil {
		return -1, err
	}
	if master != nil {
		relayLoop(master)
	}
	code := execns.Wait(pid)
	if code < 0 {
		return -1, fmt.Errorf("exec: 等待子进程失败")
	}
	return code, nil
}

// validate 检查 exec 选项。
func (o *ExecOptions) validate() error {
	if len(o.Cmd) == 0 {
		return fmt.Errorf("exec: 命令为空: %w", ErrBadConfig)
	}
	for _, seg := range o.Cmd {
		if strings.ContainsRune(seg, 0) {
			return fmt.Errorf("exec: 命令参数含 NUL: %w", ErrBadConfig)
		}
	}
	return nil
}

// applyExecUser 解析 "uid[:gid]" 并 setgroups/setgid/setuid。
func applyExecUser(user string) error {
	if user == "" {
		return nil
	}
	uidStr, gidStr, _ := strings.Cut(user, ":")
	uid, err := strconv.Atoi(uidStr)
	if err != nil || uid < 0 {
		return fmt.Errorf("exec: 非法用户 %q（应为数字 uid[:gid]）: %w", user, ErrBadConfig)
	}
	gid := -1
	if gidStr != "" {
		v, err := strconv.Atoi(gidStr)
		if err != nil || v < 0 {
			return fmt.Errorf("exec: 非法 gid %q: %w", gidStr, ErrBadConfig)
		}
		gid = v
	}
	if os.Geteuid() == 0 {
		if err := syscall.Setgroups([]int{}); err != nil {
			return fmt.Errorf("exec: setgroups: %w", err)
		}
	}
	if gid >= 0 {
		if err := syscall.Setgid(gid); err != nil {
			return fmt.Errorf("exec: setgid %d: %w", gid, err)
		}
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("exec: setuid %d: %w", uid, err)
	}
	return nil
}

// openpty 分配一个伪终端，返回 master/slave。从 /dev/ptmx 创建。
//
// 易错点：TIOCSPTLCK（解锁从端）的第三个参数是**指向 int 的指针**，不是
// 解锁值本身。内核会从该地址读 4 字节，因此传字面量 0 会让内核解引用地址 0
// 并返回 EFAULT（"bad address"）——这正是此前 `exec -it` 恒定失败的根因。
// 必须传 &unlock。两个 ioctl 的指针都要用 unsafe.Pointer 包裹，Go 的
// Syscall 不会阻止 GC 在调用期间移动/回收被指向的变量。
func openpty() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 /dev/ptmx: %w", err)
	}
	// TIOCSPTLCK=0x40045431：解锁从端；参数是指针，指向 0 表示"解锁"。
	unlock := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), tiocsptlck,
		uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		_ = m.Close()
		return nil, nil, fmt.Errorf("解锁从端: %w", errno)
	}
	// TIOCGPTN=0x80045430：取从端号。
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), tiocgptn,
		uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = m.Close()
		return nil, nil, fmt.Errorf("取从端号: %w", errno)
	}
	// 指向局部变量的指针跨 Syscall 使用后必须立即取用，避免被 GC 判定为死变量。
	runtime.KeepAlive(&n)
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = m.Close()
		return nil, nil, fmt.Errorf("打开从端: %w", err)
	}
	return m, s, nil
}

// relayLoop 在 exec 等待期间把主端与调用方 stdio 双向转发（TTY 交互）。
func relayLoop(master *os.File) {
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				_, _ = os.Stdout.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				_, _ = master.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
}
