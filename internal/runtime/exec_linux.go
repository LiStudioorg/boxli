// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"fmt"
	"github.com/LiStudioorg/boxli/internal/execns"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
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
func openpty() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("打开 /dev/ptmx: %w", err)
	}
	// TIOCSPTLCK=0x40045431：解锁。
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), 0x40045431, 0); errno != 0 {
		_ = m.Close()
		return nil, nil, errno
	}
	// TIOCGPTN=0x80045430：取从端号。
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), 0x80045430, uintptr(unsafe.Pointer(&n))); errno != 0 {
		_ = m.Close()
		return nil, nil, errno
	}
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
