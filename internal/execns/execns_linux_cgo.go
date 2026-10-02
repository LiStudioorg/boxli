// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux && cgo && !nocgo_exec

// Package execns 提供进入目标进程命名空间并执行的 cgo 辅助。
//
// 背景：纯 Go 进程调用 setns(CLONE_NEWNS) 进入挂载命名空间会返回 EINVAL
// （Go runtime 无法在纯 Go 下可靠 setns 到 mount namespace，见 Go issue
// #9091）。runc/docker exec 的标准做法是用 cgo 在 fork 出的单线程子进程里
// setns，再 exec 目标命令。boxli 用同样的方式实现：fork 出的子进程在
// fork-to-execve 之间是 C 单线程上下文，setns 因此成功。
//
// 仅此文件依赖 cgo；用 `-tags nocgo_exec` 或 CGO_ENABLED=0 回退到 stub。
package execns

/*
#define _GNU_SOURCE
#include <stdlib.h>
#include <unistd.h>
#include <grp.h>
#include <fcntl.h>
#include <sched.h>
#include <signal.h>
#include <sys/wait.h>
#include <sys/types.h>
#include <errno.h>

// execnsFork 进入 namespace 后 exec 目标命令。子进程内 C 单线程 setns。
// 返回：>=0 = 子进程 pid；<0 = -(errno)。
//
// 采用**两段 fork**，而不是"setns 完直接 execve"：
//
// setns(CLONE_NEWPID) 有个关键语义——它**不会把调用者本身移入新的 PID
// namespace**，只有之后 fork 出的子进程才是该 namespace 的成员。若在
// setns(pid) 之后直接 execve，得到的进程本身不属于新 PID namespace，却
// 被当作其成员使用；此时 Go runtime 启动阶段调用
// clone(CLONE_THREAD) 创建线程会被内核以 EINVAL 拒绝，进程直接
// fatal error: "failed to create new OS thread (have 2 already; errno=22)"。
//
// 纯 C/静态二进制（如 busybox）不会立刻建线程，所以看不出问题；但任何 Go
// 程序在容器内都会立刻崩溃，而 boxli 的镜像与用户程序大量是 Go 写的。
// 因此这里在 setns 全部完成后**再 fork 一次**：孙进程才是新 PID namespace
// 的真正成员，其 clone(CLONE_THREAD) 合法。runc 用同样的两段式做法。
//
// 中间的 fork 子进程负责 wait 孙进程并把退出码原样回传，保证调用方仍只需
// wait 一次。
static long execnsFork(
	const int* nsFds, int nsCount,
	const char* workdir,
	unsigned uid, int useUid,
	unsigned gid, int useGid,
	int inFd, int outFd, int errFd,
	char* const* argv, char* const* envp)
{
	pid_t pid = fork();
	if (pid < 0) return -errno;
	if (pid > 0) return pid;   // 父：直接返回中间进程 pid

	// —— 中间进程（C 单线程上下文）——
	for (int i = 0; i < nsCount; i++) {
		if (setns(nsFds[i], 0) != 0) { _exit(128); }   // nstype=0 自适应
	}

	// setns(pid) 之后再 fork：孙进程才真正位于目标 PID namespace。
	pid_t leaf = fork();
	if (leaf < 0) { _exit(135); }
	if (leaf > 0) {
		// 中间进程：等待孙进程并原样回传退出状态。
		int st = 0;
		while (waitpid(leaf, &st, 0) < 0) {
			if (errno == EINTR) continue;
			_exit(136);
		}
		if (WIFEXITED(st))   _exit(WEXITSTATUS(st));
		if (WIFSIGNALED(st)) { signal(WTERMSIG(st), SIG_DFL); raise(WTERMSIG(st)); _exit(137); }
		_exit(138);
	}

	// —— 孙进程：目标 PID namespace 的成员，可安全创建线程 ——
	if (workdir && workdir[0] && chdir(workdir) != 0) { _exit(129); }
	if (useUid || useGid) { if (setgroups(0, NULL) != 0) { _exit(130); } }
	if (useGid && setgid(gid) != 0) { _exit(131); }
	if (useUid && setuid(uid) != 0) { _exit(132); }
	if (dup2(inFd, 0) < 0 || dup2(outFd, 1) < 0 || dup2(errFd, 2) < 0) { _exit(133); }
	execve(argv[0], argv, envp);
	_exit(134);
}

// execnsWait 等待子进程，返回退出码（信号死亡 128+signum）；失败返回 -1。
static long execnsWait(pid_t pid) {
	int st = 0;
	while (waitpid(pid, &st, 0) < 0) {
		if (errno == EINTR) continue;
		return -1;
	}
	if (WIFEXITED(st)) return (long)((WEXITSTATUS(st)) & 0xFFFF);
	if (WIFSIGNALED(st)) return (long)((128 + WTERMSIG(st)) & 0xFFFF);
	return 1;
}
*/
import "C"

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

// ErrNoCgoExec 备用（本文件因 cgoEnabled=true 正常不会被 runtime 读取）。
var ErrNoCgoExec = fmt.Errorf("exec 需要 cgo 构建")

// cgoEnabled 供纯 Go 决策层判断。
const cgoEnabled = true

// Enabled 报告当前构建是否启用 cgo exec。
func Enabled() bool { return true }

// nsOrder 进入顺序：runc 为 mnt,uts,ipc,net,pid。
var nsOrder = []string{"mnt", "uts", "ipc", "net", "pid"}

// Enter 进入 targetPID 的命名空间并 exec cmd，返回子进程 pid（须随后 Wait）。
// 子进程会立刻 exec；stdio 用 in/out/err fd。
func Enter(targetPID int, workdir, user string, env []string, inFd, outFd, errFd int, cmd []string) (int, error) {
	if len(cmd) == 0 {
		return -1, fmt.Errorf("execns: 命令为空")
	}
	// 先在宿主 /proc 打开全部命名空间 fd（不能在 setns 之后再 open：进入容器
	// mount ns 后 /proc 变成容器的，scratch 里没有 procfs，open 会 ENOENT）。
	nss, err := openNamespaces(targetPID)
	if err != nil {
		return -1, err
	}
	defer closeFds(nss)
	uidStr, gidStr, _ := strings.Cut(user, ":")
	var uid, gid int
	var useUid, useGid bool
	if uidStr != "" {
		v, e := strconv.Atoi(uidStr)
		if e != nil || v < 0 {
			return -1, fmt.Errorf("execns: 非法 uid")
		}
		uid, useUid = v, true
	}
	if gidStr != "" {
		v, e := strconv.Atoi(gidStr)
		if e != nil || v < 0 {
			return -1, fmt.Errorf("execns: 非法 gid")
		}
		gid, useGid = v, true
	}

	c := newCPtrs()
	defer c.free()
	argv := make([]*C.char, 0, len(cmd)+1)
	for _, a := range cmd {
		argv = append(argv, c.cstr(a))
	}
	argv = append(argv, nil)
	envp := make([]*C.char, 0, len(env)+1)
	for _, e := range env {
		envp = append(envp, c.cstr(e))
	}
	envp = append(envp, nil)

	fds := make([]C.int, len(nss))
	for i, f := range nss {
		fds[i] = C.int(f)
	}
	pid := C.execnsFork(
		&fds[0], C.int(len(fds)),
		c.cstr(workdir),
		C.uint(uid), b2i(useUid),
		C.uint(gid), b2i(useGid),
		C.int(inFd), C.int(outFd), C.int(errFd),
		(**C.char)(&argv[0]), (**C.char)(&envp[0]),
	)
	if pid < 0 {
		return -1, fmt.Errorf("execns: 启动失败: %v", syscall.Errno(-int(pid)))
	}
	return int(pid), nil
}

// Wait 等待 Enter 返回的子进程并返回退出码（信号死亡 128+signum）。
func Wait(pid int) int {
	code := C.execnsWait(C.pid_t(pid))
	if code < 0 {
		return -1
	}
	return int(code)
}

type cPtrs struct{ s []unsafe.Pointer }

func newCPtrs() *cPtrs { return &cPtrs{} }

func (c *cPtrs) cstr(s string) *C.char {
	if s == "" {
		return nil
	}
	p := C.CString(s)
	c.s = append(c.s, unsafe.Pointer(p))
	return p
}

func (c *cPtrs) free() {
	for _, p := range c.s {
		C.free(p)
	}
	c.s = nil
}

func b2i(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// openNamespaces 在宿主 /proc 打开 targetPID 的全部 ns fd。
func openNamespaces(targetPID int) ([]int, error) {
	fds := make([]int, 0, len(nsOrder))
	for _, n := range nsOrder {
		fd, err := syscall.Open(filepath.Join("/proc", strconv.Itoa(targetPID), "ns", n),
			syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			closeFds(fds)
			return nil, fmt.Errorf("execns: 打开 %s ns: %w", n, err)
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

func closeFds(fds []int) {
	for _, f := range fds {
		_ = syscall.Close(f)
	}
}
