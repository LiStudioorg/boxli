// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
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

// 挂载常量补充（syscall 包未导出 MS_NOSUID / MS_NOEXEC / MS_NODEV）。
const (
	msNoSuid  = 0x2   // MS_NOSUID
	msNoDev   = 0x4   // MS_NODEV
	msNoExec  = 0x8   // MS_NOEXEC
	msNoAtime = 0x400 // MS_NOATIME
)

// minDevNodes 是最小可用 rootfs 需要的 /dev 设备节点。
//
// 采用"从宿主 bind 单个节点"而非整体 bind /dev：Android 的 /dev 下有
// binder / ashmem / kgsl 等平台专有节点，整体 bind 会把它们暴露给容器，
// 既无意义也可能带来越权风险。
//
// 清单比你直觉需要的更长，漏掉下面的项会在真实负载上出问题：
//   - full：/dev/full 写满返回 ENOSPC，部分测试与工具依赖；
//   - ptmx：没有它容器内无法开伪终端（exec -it 会失败）；
//   - 其余为常规字符设备。
var minDevNodes = []string{
	"null", "zero", "full", "random", "urandom", "tty", "ptmx",
}

// devSymlinks 是 /dev 下应存在的符号链接，指向 /proc/self/fd 对应项。
// 大量 shell 脚本与工具依赖 /dev/stdin、/dev/fd/N 这类路径；缺失会让
// 形如 `cmd < /dev/stdin`、`bash -c 'echo x > /dev/stderr'` 的用法失败。
var devSymlinks = map[string]string{
	"fd":     "/proc/self/fd",
	"stdin":  "/proc/self/fd/0",
	"stdout": "/proc/self/fd/1",
	"stderr": "/proc/self/fd/2",
}

// defaultShmSize 是 /dev/shm tmpfs 的默认大小（字节）。
// 共享内存是 PostgreSQL、Chromium 等负载的硬性依赖；内核默认的 tmpfs
// 大小是内存的一半，对容器来说过大且不可控，因此显式给一个保守默认值。
const defaultShmSize = 64 << 20

// envShmSize 允许调用方覆盖 /dev/shm 大小（字节，十进制字符串）。
const envShmSize = "BOXLI_SHM_SIZE"

// devShmSize 返回本次容器 /dev/shm 的大小：环境变量优先，其次默认值。
// 非法或非正值一律回落默认值，不让坏输入阻断容器启动。
func devShmSize() int64 {
	if v := os.Getenv(envShmSize); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultShmSize
}

// syscallUnmount 卸载一个挂载点（MNT_DETACH：即使仍被占用也延迟卸载）。
// 供 /dev 装配的清理路径与测试使用。
func syscallUnmount(path string) error {
	return syscall.Unmount(path, syscall.MNT_DETACH)
}

// fileInfoSys 取 os.FileInfo 底层的 syscall.Stat_t，用于读取设备号。
// 判断"是否真的是独立挂载点"必须比对设备号，仅看目录存在会被普通目录骗过。
func fileInfoSys(fi os.FileInfo) (*syscall.Stat_t, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return st, ok
}

// hostProcMountInfo 是宿主 /proc 自检所需的路径，做成变量以便测试注入。
var hostProcMountInfo = "/proc/self/mountinfo"

// parseHidePID 从 mountinfo 内容里解析宿主 /proc 的 hidepid 值。
//
// 返回 (值, 是否找到, 是否解析成功)。三条信息必须分开，因为决策不同：
//   - 找不到 hidepid 选项 → 未设置，调用方不传参数；
//   - 找到但值非法 → 视为解析失败，调用方不传参数（保守）；
//   - 找到且合法 → 用该值判断是否需要显式覆盖。
//
// mountinfo 的字段布局（见 proc(5)）：
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
//	①  ②  ③     ④     ⑤     ⑥                    ⑦  ⑧      ⑨        ⑩
//
// 第 5 个字段（索引 4）是挂载点，第 6 个（索引 5）是 per-mount 选项，
// hidepid 属于 per-mount 选项，因此在索引 5 里查找。
//
// 为什么解析 /proc/self/mountinfo 而不是 /proc/mounts：mounts 只给出
// 文件系统级的挂载选项（superblock options），而 hidepid 是 per-mount
// 选项，只有 mountinfo 才完整呈现。
func parseHidePID(mountinfo string) (int, bool, bool) {
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		// 至少需要到 " - " 分隔符前的 6 个字段。
		if len(fields) < 6 {
			continue
		}
		if fields[4] != "/proc" {
			continue
		}
		for _, opt := range strings.Split(fields[5], ",") {
			val, ok := strings.CutPrefix(opt, "hidepid=")
			if !ok {
				continue
			}
			n, err := strconv.Atoi(val)
			if err != nil {
				return 0, true, false // 找到了但值非法
			}
			return n, true, true
		}
		// 找到 /proc 行但其中没有 hidepid → 未设置。
		return 0, false, true
	}
	// 没有 /proc 行（异常环境）→ 视为未设置，不阻断。
	return 0, false, true
}

// procMountOptions 决定容器 /proc 应使用的挂载参数。
//
// 决策表（与设计一致）：
//
//	宿主 hidepid 未设置   → ""（用内核默认）
//	宿主 hidepid = 0      → ""（已经是最宽松，无需覆盖）
//	宿主 hidepid = 1 或 2 → "hidepid=0"（否则容器内看不到自己的进程）
//	解析失败              → ""（保守，且记日志不阻断）
//
// 返回的字符串直接作为 mount(2) 的 data 参数（空串表示不传）。
func procMountOptions(mountinfo string) string {
	hidepid, found, ok := parseHidePID(mountinfo)
	if !ok {
		slog.Debug("解析宿主 /proc hidepid 失败，容器将使用默认挂载参数")
		return ""
	}
	if !found {
		return ""
	}
	if hidepid > 0 {
		return "hidepid=0"
	}
	return ""
}

// mountContainerProc 在 rootfs/proc 挂载绑定当前 PID namespace 的 procfs。
//
// 先按宿主 hidepid 情况决定参数；若带参数挂载失败（内核不支持 hidepid，
// 或受限环境拒绝），回退为不带参数再试一次。两次都失败才报错——这样
// 既能在需要时修正 hidepid，又不会因参数不被支持而阻断容器启动。
func mountContainerProc(rootfs string) error {
	target := filepath.Join(rootfs, "proc")
	opts := procMountOptions(readHostMountInfo())

	if opts != "" {
		if err := mountProcRaw(target, opts); err != nil {
			slog.Debug("带 hidepid 参数挂载 /proc 失败，回退为默认参数",
				slog.String("opts", opts), slog.Any("err", err))
		} else {
			return nil
		}
	}
	if err := mountProcRaw(target, ""); err != nil {
		return fmt.Errorf("挂载 /proc: %w", err)
	}
	return nil
}

// mountProcRaw 是 mountContainerProc 使用的挂载原语，做成变量以便测试注入
// 一个假的挂载函数来验证回退逻辑，而不必真的改宿主挂载表。
var mountProcRaw = func(target, opts string) error {
	return syscall.Mount("proc", target, "proc", 0, opts)
}

// readHostMountInfo 读取宿主 mountinfo；失败返回空串（调用方按"未设置"处理）。
func readHostMountInfo() string {
	data, err := os.ReadFile(hostProcMountInfo)
	if err != nil {
		slog.Debug("读取宿主 mountinfo 失败，容器 /proc 使用默认参数",
			slog.String("path", hostProcMountInfo), slog.Any("err", err))
		return ""
	}
	return string(data)
}

// setupContainerDev 在 rootfs/dev 下装配容器所需的设备环境。
//
// 顺序有讲究：
//  1. 先 bind 字符设备（需要 rootfs/dev 已存在）；
//  2. 再建 /dev/shm 并挂 tmpfs（独立挂载，避免容器写满宿主 /dev）；
//  3. 再建 /dev/pts 并挂 devpts（exec -it 依赖）；
//  4. 最后建符号链接（纯文件操作，无失败风险但放最后更清晰）。
//
// 任一步骤失败都返回错误而非静默跳过：设备环境不完整会让容器内的
// 表现为"命令莫名失败"，比启动时报错难排查得多。唯一例外是宿主缺少
// 某设备节点——那是宿主本身的问题，跳过即可。
func setupContainerDev(rootfs string) error {
	devDir := filepath.Join(rootfs, "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		return fmt.Errorf("创建 /dev: %w", err)
	}
	if err := bindHostDevices(rootfs); err != nil {
		return err
	}
	if err := mountDevShm(rootfs); err != nil {
		return err
	}
	if err := mountDevPts(rootfs); err != nil {
		return err
	}
	return createDevSymlinks(rootfs)
}

// mountDevShm 在 rootfs/dev/shm 挂 tmpfs。
//
// 先尝试带 size 挂载；内核不支持 size= 时（极老内核或受限环境）退化为
// 不带参数的默认 tmpfs，而不是让容器启动失败——共享内存"有但大小不可控"
// 远好于"完全没有"。
func mountDevShm(rootfs string) error {
	shm := filepath.Join(rootfs, "dev", "shm")
	if err := os.MkdirAll(shm, 0o1777); err != nil {
		return fmt.Errorf("创建 /dev/shm: %w", err)
	}
	size := devShmSize()
	flags := uintptr(msNoSuid | msNoDev | msNoExec)
	opts := fmt.Sprintf("size=%d", size)
	if err := syscall.Mount("tmpfs", shm, "tmpfs", flags, opts); err != nil {
		// 回退：不带 size 参数再试一次。
		if err2 := syscall.Mount("tmpfs", shm, "tmpfs", flags, ""); err2 != nil {
			return fmt.Errorf("挂载 /dev/shm: %w", err)
		}
		slog.Debug("挂载 /dev/shm 时 size 参数不被支持，已回退默认大小",
			slog.Int64("size", size))
	}
	return nil
}

// mountDevPts 在 rootfs/dev/pts 挂 devpts，供容器内分配伪终端。
// 同样带一次回退：部分内核/环境对 newinstance 支持不完整。
func mountDevPts(rootfs string) error {
	pts := filepath.Join(rootfs, "dev", "pts")
	if err := os.MkdirAll(pts, 0o755); err != nil {
		return fmt.Errorf("创建 /dev/pts: %w", err)
	}
	flags := uintptr(msNoSuid | msNoDev | msNoExec)
	if err := syscall.Mount("devpts", pts, "devpts", flags, "newinstance,ptmxmode=0666,mode=0620"); err != nil {
		if err2 := syscall.Mount("devpts", pts, "devpts", flags, "ptmxmode=0666,mode=0620"); err2 != nil {
			return fmt.Errorf("挂载 /dev/pts: %w", err)
		}
		slog.Debug("挂载 /dev/pts 时 newinstance 不被支持，已回退")
	}
	return nil
}

// createDevSymlinks 在 rootfs/dev 下建立 fd/stdin/stdout/stderr 符号链接。
// 已存在则覆盖（rootfs 由镜像层决定，可能预置了指向别处的同名链接）。
func createDevSymlinks(rootfs string) error {
	for name, target := range devSymlinks {
		link := filepath.Join(rootfs, "dev", name)
		if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// 目录等无法删除的形态也不必失败，交给 Symlink 报错更准确。
			slog.Debug("移除既有 /dev 链接失败", slog.String("path", link), slog.Any("err", err))
		}
		if err := os.Symlink(target, link); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("创建 /dev/%s → %s: %w", name, target, err)
		}
	}
	return nil
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
	// 2.5 装配容器 /dev：字符设备 + shm(tmpfs) + pts(devpts) + 标准符号链接。
	if err := setupContainerDev(rootfs); err != nil {
		return err
	}
	// 3. rootfs 必须自身是挂载点；递归 bind 自身一次（连带把 dev bind 复制进新子树）。
	if err := syscall.Mount(rootfs, rootfs, "", uintptr(msRec|msBind), ""); err != nil {
		return fmt.Errorf("bind rootfs %s: %w", rootfs, err)
	}
	// 3.5 卷挂载：把宿主任一命名卷/匿名卷/bind 源 bind 或 tmpfs 进 rootfs
	//    目标路径。必须在 pivot_root 之前，保证挂载随新根一同进入容器。
	if err := mountRootfsVolumes(rootfs); err != nil {
		return err
	}
	// 4. 先挂 procfs（早于 pivot_root）。部分环境（如嵌套容器）在 pivot 并卸载旧根后
	//    拒绝再建 proc 超块；pivot 前挂载得到的是绑定本 PID namespace 的全新 procfs，
	//    随 rootfs 一起进入新根，语义完全等价。
	//    参数按宿主 hidepid 情况决定：宿主为 hidepid=1/2 时显式覆盖为 0，
	//    否则容器内 ps 看不到自己的进程（Android 有设备默认 hidepid=2）。
	if err := mountContainerProc(rootfs); err != nil {
		return err
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

// mountRootfsVolumes 把 -v 解析出的挂载列表 bind 进 rootfs 目标路径。
// 在 pivot_root 之前调用：rootfs 已 bind 为挂载点，挂载随新根进入容器。
// 目标路径必须是容器内绝对路径，且经 Clean 后不得逃逸出 rootfs。
func mountRootfsVolumes(rootfs string) error {
	mounts, ok := parseMountEnv(os.Environ())
	if !ok {
		return nil
	}
	for _, m := range mounts {
		if err := safeContainerTarget(m.Target); err != nil {
			return err
		}
		dst := filepath.Join(rootfs, filepath.Clean(m.Target)[1:])
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return fmt.Errorf("创建挂载点 %s: %w", m.Target, err)
		}
		flags := uintptr(msBind)
		if m.ReadOnly {
			flags |= syscall.MS_RDONLY
		}
		if err := syscall.Mount(m.Source, dst, "", flags, ""); err != nil {
			return fmt.Errorf("挂载卷 %s → %s: %w", m.Source, m.Target, err)
		}
		// bind 之后必须先 bind 再 remount 才能应用只读：单个
		// mount(MS_BIND|MS_RDONLY) 的 MS_RDONLY 会被内核忽略，bind 仍是可写。
		if m.ReadOnly {
			if err := syscall.Mount(dst, dst, "", uintptr(msBind|syscall.MS_REMOUNT|syscall.MS_RDONLY), ""); err != nil {
				return fmt.Errorf("卷 %s 设为只读失败: %w", m.Target, err)
			}
		}
	}
	return nil
}

// safeContainerTarget 校验容器内挂载目标是绝对路径且不含目录穿越。
func safeContainerTarget(target string) error {
	if !filepath.IsAbs(target) {
		return fmt.Errorf("卷目标 %q 不是绝对路径: %w", target, ErrBadConfig)
	}
	clean := filepath.Clean(target)
	if clean != target || strings.Contains(target, "..") {
		return fmt.Errorf("卷目标 %q 非法（含 .. 或非规范路径）: %w", target, ErrBadConfig)
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
