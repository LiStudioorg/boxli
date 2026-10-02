// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// ExecOptions 定义见 config.go（跨平台）。

// Exec 在目标容器的命名空间里执行命令并等待退出，返回退出码（信号死亡时
// 128+signum）。需要 root（CAP_SYS_ADMIN）以 setns 进入他人命名空间。
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
	// setns 是线程级操作：锁定避免 Go 调度迁移线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// 打开全部命名空间 fd。
	nsFds := map[string]int{}
	openNS := func(name string) (int, error) {
		fp := filepath.Join("/proc", strconv.Itoa(o.TargetPID), "ns", name)
		fd, err := syscall.Open(fp, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return -1, fmt.Errorf("打开命名空间 %s: %w", fp, err)
		}
		nsFds[name] = fd
		return fd, nil
	}
	for _, name := range []string{"mnt", "uts", "ipc", "net", "pid"} {
		if _, err := openNS(name); err != nil {
			closeNSFds(nsFds)
			return -1, err
		}
	}
	defer closeNSFds(nsFds)

	// 先进入 mnt/uts/ipc/net，再 pid：真实依赖（rootless 的 user ns
	// 逻辑复杂，此处按 root 容器处理；非 root 已在上方栏）。
	for _, name := range []string{"mnt", "uts", "ipc", "net"} {
		if err := setns(nsFds[name], nsFlags(name)); err != nil {
			return -1, nsErr(name, err)
		}
	}
	// 进入 PID 命名空间：此后 fork 出的子进程才会落入容器 PID namespace。
	if err := setns(nsFds["pid"], syscall.CLONE_NEWPID); err != nil {
		return -1, nsErr("pid", err)
	}

	// 工作目录（此刻已进入容器 mount namespace，按容器内路径解析）。
	if o.Workdir != "" {
		if err := os.Chdir(o.Workdir); err != nil {
			return -1, fmt.Errorf("chdir %s: %w", o.Workdir, err)
		}
	}
	// 运行身份：setgroups 必须先于 setgid/setuid。
	if err := applyExecUser(o.User); err != nil {
		return -1, err
	}

	// 决定三个 stdio fd。
	stdin := firstNonNil(o.Stdin, os.Stdin)
	stdout := firstNonNil(o.Stdout, os.Stdout)
	stderr := firstNonNil(o.Stderr, os.Stderr)
	files := []uintptr{stdin.Fd(), stdout.Fd(), stderr.Fd()}

	var master *os.File
	if o.TTY {
		m, s, err := openpty()
		if err != nil {
			return -1, fmt.Errorf("openpty: %w", err)
		}
		master = m
		files = []uintptr{s.Fd(), s.Fd(), s.Fd()}
		defer func() {
			_ = s.Close()
			if master != nil {
				_ = master.Close()
			}
		}()
	}

	// ForkExec：子进程直接 exec 目标命令，落入容器 PID 命名空间。
	pid, err := syscall.ForkExec(o.Cmd[0], o.Cmd, &syscall.ProcAttr{
		Env:   o.Env,
		Files: files,
	})
	if err != nil {
		if master != nil {
			_ = master.Close()
		}
		return -1, fmt.Errorf("exec %s: %w", o.Cmd[0], err)
	}

	if master != nil {
		relayLoop(master)
	}

	var ws syscall.WaitStatus
	for {
		wpid, err := syscall.Wait4(pid, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return -1, fmt.Errorf("wait exec: %w", err)
		}
		if wpid == pid {
			break
		}
	}
	return execExitCode(ws), nil
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

// execExitCode 统一退出码：正常退出原样；信号死亡 128+signum。
func execExitCode(ws syscall.WaitStatus) int {
	if ws.Exited() {
		return ws.ExitStatus()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}

// nsErr 包装一次 setns 命名空间错误；权限不足时给出 Android-有 Root 的指引。
func nsErr(name string, err error) error {
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return fmt.Errorf("setns %s：namespace 创建失败，权限不足。Android 需 Root，无 Root 环境官方不支持: %w", name, err)
	}
	return fmt.Errorf("setns %s: %w", name, err)
}

// nsFlags 命名空间标记位（syscall 包未导出 CLONE_NEWNET）。
func nsFlags(name string) int {
	switch name {
	case "mnt":
		return syscall.CLONE_NEWNS
	case "uts":
		return syscall.CLONE_NEWUTS
	case "ipc":
		return syscall.CLONE_NEWIPC
	case "net":
		return cloneNewNet
	}
	return 0
}

// setns 封装内核 setns 系统调用。
func setns(fd int, nstype int) error {
	num := setnsSyscallNr()
	if num == 0 {
		return fmt.Errorf("setns 在当前架构 %s 不支持", runtime.GOARCH)
	}
	_, _, errno := syscall.Syscall(num, uintptr(fd), uintptr(nstype), 0)
	if errno == syscall.EINVAL && nstype == cloneNewNet {
		// 部分内核要求 nstype=0 进入 netns。
		_, _, errno = syscall.Syscall(num, uintptr(fd), 0, 0)
	}
	if errno != 0 {
		return errno
	}
	return nil
}

// setnsSyscallNr 返回当前架构的 setns 系统调用号。
func setnsSyscallNr() uintptr {
	switch runtime.GOARCH {
	case "amd64":
		return 308
	case "arm64", "riscv64":
		return 268
	case "386":
		return 346
	case "arm":
		return 375
	case "ppc64", "ppc64le":
		return 350
	case "s390x":
		return 339
	case "loong64":
		return 437
	}
	return 0
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

// closeNSFds 关闭全部打开的命名空间 fd。
func closeNSFds(m map[string]int) {
	for _, fd := range m {
		_ = syscall.Close(fd)
	}
}

// 命名空间常量（syscall 未导出）。
const cloneNewNet = 0x40000000
