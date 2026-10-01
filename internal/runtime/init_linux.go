// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/LiStudioorg/boxli/internal/network"
)

// Linux 挂载常量（syscall 包未导出这些位）。
const (
	msRec     = 0x4000  // MS_REC
	msPrivate = 0x40000 // MS_PRIVATE
	msBind    = 0x1000  // MS_BIND

	oldRootPrefix = ".boxli_old_root."
)

// minDevNodes 是最小可用 rootfs 需要的 /dev 设备。rootless 下无法 mknod，
// 统一从宿主 bind 进来；宿主没有的自动跳过。
var minDevNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

// RunInit 是容器 1 号进程入口：pivot_root 进新根、挂载最小 /dev 与 /proc，
// 最后 exec 用户命令。仅应由 `boxli init` 重执行路径调用（见 IsInitProcess）。
func RunInit() error {
	if !IsInitProcess() {
		return ErrNotInit
	}
	// chdir/chroot/pivot_root 都是线程级 FS 语义，锁定线程避免 Go 调度干扰。
	runtime.LockOSThread()

	rootfs := os.Getenv(envRootfs)
	if rootfs == "" {
		return fmt.Errorf("环境变量 %s 缺失: %w", envRootfs, ErrBadConfig)
	}
	cmdline, err := childCmdline()
	if err != nil {
		return err
	}
	env := envWithoutBoxli()

	if hn := os.Getenv(envHostname); hn != "" {
		if err := syscall.Sethostname([]byte(hn)); err != nil {
			return fmt.Errorf("sethostname %q: %w", hn, err)
		}
	}

	// 每容器唯一旧根目录名：同一 rootfs 上并发多容器共享磁盘目录项，
	// 固定名会让先退出者 Rmdir 掉他人尚未完成 pivot 的旧根。
	oldRoot := oldRootPrefix + instanceID()

	// 1. 全树挂载事件设 private：切断宿主共享子树的传播，也是 bind/pivot 的前置条件。
	if err := syscall.Mount("", "/", "", uintptr(msRec|msPrivate), ""); err != nil {
		return fmt.Errorf("根挂载设 private: %w", err)
	}
	// 2. 预建目录与最小 /dev（此刻宿主路径仍可见）。
	for _, d := range []string{"proc", "dev", "tmp", oldRoot} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	if err := bindHostDevices(rootfs); err != nil {
		return err
	}
	// 3. rootfs 必须自身是挂载点；递归 bind 自身一次（连带把 dev bind 复制进新子树）。
	if err := syscall.Mount(rootfs, rootfs, "", uintptr(msRec|msBind), ""); err != nil {
		return fmt.Errorf("bind rootfs %s: %w", rootfs, err)
	}
	// 4. 先挂 procfs（早于 pivot_root）。部分环境（如嵌套容器）在 pivot 并卸载旧根后
	//    拒绝再建 proc 超块；pivot 前挂载得到的是绑定本 PID namespace 的全新 procfs，
	//    随 rootfs 一起进入新根，语义完全等价。
	if err := syscall.Mount("proc", filepath.Join(rootfs, "proc"), "proc", 0, ""); err != nil {
		return fmt.Errorf("挂载 /proc（pivot 前）: %w", err)
	}
	// 5. pivot_root(".", oldRoot)。注意：不做 chroot——pivot_root 的内核约束是
	//    new_root 必须位于当前 root 之下且不等于当前 root，chroot 反而使其失败。
	if err := os.Chdir(rootfs); err != nil {
		return fmt.Errorf("chdir %s: %w", rootfs, err)
	}
	if err := syscall.PivotRoot(".", oldRoot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	// 6. 摘除旧根，宿主文件系统从容器视野消失。
	if err := syscall.Unmount("/"+oldRoot, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("卸载旧根: %w", err)
	}
	_ = syscall.Rmdir("/" + oldRoot)

	// 7. 容器侧网络装配：赋值 IP/路由，把 DNS 指向网桥网关。此刻已在
	//    容器 netns 与 rootfs 内；非 bridge 模式（host/none）跳过。
	if mode, cid, name, ip, gw, hostname, prefix, ok := parseNetEnv(os.Environ()); ok && *mode == network.ModeBridge {
		if err := network.ConfigurePeer(name, cid, ip, gw, prefix, hostname); err != nil {
			return fmt.Errorf("配置容器网络失败: %w", err)
		}
	}

	if err := syscall.Exec(cmdline[0], cmdline, env); err != nil {
		return fmt.Errorf("exec %s: %w", cmdline[0], err)
	}
	return nil // 不可达
}

// instanceID 返回本次容器实例 ID（父进程注入）。缺失时退化为固定名，
// 单容器场景仍正确，仅并发共享同一 rootfs 时才有旧根重名风险。
func instanceID() string {
	if id := os.Getenv(envCID); id != "" {
		return id
	}
	return "0"
}

// bindHostDevices 把宿主 /dev 的最小设备集 bind 进 rootfs/dev。
func bindHostDevices(rootfs string) error {
	for _, name := range minDevNodes {
		src := filepath.Join("/dev", name)
		dst := filepath.Join(rootfs, "dev", name)
		if _, err := os.Stat(src); err != nil {
			continue // 宿主没有该设备则跳过
		}
		f, err := os.OpenFile(dst, os.O_CREATE, 0o666)
		if err != nil {
			return fmt.Errorf("创建设备占位 %s: %w", dst, err)
		}
		_ = f.Close()
		if err := syscall.Mount(src, dst, "", msBind, ""); err != nil {
			return fmt.Errorf("bind %s: %w", src, err)
		}
	}
	return nil
}

// childCmdline 从 BOXLI_ARGC / BOXLI_ARG0..N 读取用户命令。
func childCmdline() ([]string, error) {
	n, err := strconv.Atoi(os.Getenv(envChildCmdCountKey))
	if err != nil || n <= 0 {
		return nil, fmt.Errorf("%s=%q 非法: %w", envChildCmdCountKey, os.Getenv(envChildCmdCountKey), ErrBadConfig)
	}
	argv := make([]string, 0, n)
	for i := range n {
		v, ok := os.LookupEnv(envChildCmdPrefix + strconv.Itoa(i))
		if !ok {
			return nil, fmt.Errorf("缺少 %s%d: %w", envChildCmdPrefix, i, ErrBadConfig)
		}
		argv = append(argv, v)
	}
	return argv, nil
}

// envWithoutBoxli 返回剥离 BOXLI_* 内部变量后的容器环境。
func envWithoutBoxli() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "BOXLI_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}
