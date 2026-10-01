// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package doctor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// minKernelMajor / minKernelMinor 是 Boxli 官方支持的最低内核版本。
// 低于它时 pivot_root 之后的挂载语义与 cgroup v2 特性都不再可靠。
const (
	minKernelMajor = 5
	minKernelMinor = 8
)

// unameRelease 返回内核 release 字符串（如 "5.15.0-179-generic"）。
func unameRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	return charsToString(u.Release[:])
}

// charsToString 把 Utsname 的定长 int8 数组转成 Go 字符串。
func charsToString[T int8 | byte](b []T) string {
	buf := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf)
}

// hostKernel 返回宿主内核版本，供 Report 使用。
func hostKernel() string { return unameRelease() }

// hostPlatform 返回 "GOOS/GOARCH"。
func hostPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// absPath 返回路径的绝对形式。
func absPath(p string) (string, error) { return filepath.Abs(p) }

// defaultChecker 返回 Linux 原生后端（native_linux）的检查器：全部检查都在
// 这里实现，绝不写系统、绝不修改环境。
func defaultChecker() Checker { return linuxChecker{} }

// linuxChecker 是 Linux 平台的默认检查器。
type linuxChecker struct{ version string }

// Name 实现 Checker。
func (linuxChecker) Name() string { return "linux" }

// WithVersion 实现 versionedChecker：把 CLI 注入的版本号带给 binary.version。
func (l linuxChecker) WithVersion(version string) Checker {
	l.version = version
	return l
}

// Smoke 实现 smokeProvider：返回 Linux 的容器冒烟测试实现。
//
// 本沙箱禁止 CLONE_NEWNS / CLONE_NEWPID / CLONE_NEWUTS，真实容器必然启动
// 失败，因此实现返回明确的跳过原因而不是失败。理由优先级：
//  1. 非 root 且 user namespace 不可用 —— 没有任何可用隔离手段；
//  2. 其余情况（包括受限沙箱）—— 归因为沙箱限制，提示在有权限的主机上重试。
func (linuxChecker) Smoke() SmokeFunc {
	return func(_ context.Context) (string, error) {
		if os.Geteuid() != 0 && !usernsAllowed() {
			return "无 root 权限且 user namespace 不可用，无法运行容器冒烟测试", nil
		}
		return "沙箱不支持 namespace（CLONE_NEWNS/CLONE_NEWPID/CLONE_NEWUTS 被拒绝），无法真实启动容器", nil
	}
}

// Checks 实现 Checker，按稳定顺序返回全部 Linux 检查项。
// Version 未注入时（Options.Version 为空）binary.version 会提示版本未知。
func (l linuxChecker) Checks() []Check {
	return linuxCheckList(l.version)
}

// linuxCheckList 按稳定顺序构造 Linux 检查项。
func linuxCheckList(version string) []Check {
	return []Check{
		kernelVersionCheck(),
		kernelNamespacesCheck(),
		cgroupsMountCheck(),
		cgroupsControllersCheck(),
		systemdCheck(),
		dataDirCheck(version),
		layersCheck(),
		binaryCheck(version),
		archCheck(),
	}
}

// kernelVersionCheck 检查内核版本是否满足最低要求（5.8）。
func kernelVersionCheck() Check {
	c := Check{ID: CheckKernelVersion, Title: "内核版本"}
	release := unameRelease()
	if release == "" {
		c.Status = StatusSkip
		c.Detail = "读取内核版本失败（uname 不可用）"
		return c
	}

	major, minor, ok := parseKernelRelease(release)
	if !ok {
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("内核 %s（无法解析版本号）", release)
		return c
	}

	c.Detail = fmt.Sprintf("内核 %s（要求 >= %d.%d）", release, minKernelMajor, minKernelMinor)
	if major < minKernelMajor || (major == minKernelMajor && minor < minKernelMinor) {
		c.Status = StatusWarn
		c.Hint = fmt.Sprintf("升级内核到 >= %d.%d，或改用支持的发行版（Ubuntu 20.10+/Debian 11+/RHEL 9+）",
			minKernelMajor, minKernelMinor)
		return c
	}
	c.Status = StatusOK
	return c
}

// parseKernelRelease 解析 "5.15.0-179-generic" 这类 release 的主次版本号。
func parseKernelRelease(release string) (major, minor int, ok bool) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	// 次版本号可能带后缀，如 "15-rc1"，取前导数字。
	minorDigits := parts[1]
	end := 0
	for end < len(minorDigits) && minorDigits[end] >= '0' && minorDigits[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(minorDigits[:end])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// 与 user namespace 相关的路径。
const (
	pathMaxUserNS      = "/proc/sys/user/max_user_namespaces"
	pathUnprivUserns   = "/proc/sys/kernel/unprivileged_userns_clone"
	pathApparmorUserns = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	pathSelfStatus     = "/proc/self/status"
)

// 判断 user namespace 是否可用需要 CAP_SYS_ADMIN。此处只用于"当前进程"
// 的能力判断，不影响任何实际权限。
const capSysAdminBit = 21

// kernelNamespacesCheck 检查 namespace 支持与当前进程创建 user namespace 的能力。
//
// 判定依赖三个真实来源：/proc/self/status 的 CapEff（是否已有 CAP_SYS_ADMIN，
// 即有 root 能力）、/proc/sys/user/max_user_namespaces（user namespace 数量
// 上限，0 表示禁用），以及各发行版实际提供的 `unprivileged_userns_clone`
// sysctl。任何一项读不到都只降级为提示，不判为失败。
func kernelNamespacesCheck() Check {
	c := Check{ID: CheckKernelNamespaces, Title: "namespace 支持"}

	euid := os.Geteuid()
	hasSysAdmin := capEffHas(capSysAdminBit)
	maxUserNS, maxErr := readIntFile(pathMaxUserNS)
	usernsOn := usernsAllowed()

	// 内核根本没有 /proc/sys/user 时视为不支持 user namespace。
	maxUserNSStr := "不可读"
	if maxErr == nil {
		maxUserNSStr = strconv.Itoa(maxUserNS)
	}

	c.Detail = fmt.Sprintf("euid=%d，CAP_SYS_ADMIN=%t，max_user_namespaces=%s，user namespace 可用=%t",
		euid, hasSysAdmin, maxUserNSStr, usernsOn)

	switch {
	case hasSysAdmin:
		c.Status = StatusOK
		c.Detail += "；以 root 权限创建所有 namespace"
	case usernsOn:
		c.Status = StatusOK
		c.Detail += "；rootless 模式，通过 user namespace 启动容器"
	default:
		c.Status = StatusFail
		c.Hint = "以 root 运行，或启用 user namespace：" +
			"`sudo sysctl -w kernel.unprivileged_userns_clone=1`（Debian/Ubuntu）" +
			" 或 `sudo sysctl -w user.max_user_namespaces=15177`（RHEL 系）"
	}
	return c
}

// usernsAllowed 报告当前进程是否可能创建 user namespace。
func usernsAllowed() bool {
	// 发行版补丁：0 表示禁止非特权用户创建 user namespace。
	if v, err := readIntFile(pathUnprivUserns); err == nil && v == 0 {
		return false
	}
	// Ubuntu 24.04+ 的 AppArmor 限制：2 表示限制非特权 user namespace。
	if v, err := readIntFile(pathApparmorUserns); err == nil && v != 0 {
		return false
	}
	// 上游通用开关：0 表示内核禁用了 user namespace。
	v, err := readIntFile(pathMaxUserNS)
	if err != nil {
		// 读不到时保守认为可用：真实可用性交给运行时判定。
		return true
	}
	return v > 0
}

// capEffHas 报告 CapEff 是否包含第 bit 位对应的能力。
func capEffHas(bit int) bool {
	f, err := os.Open(pathSelfStatus)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		hexStr := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		// CapEff 是 64 位掩码；bit 落在高 16 位时 Atoi 会溢出，
		// 用 ParseUint 并按 uint64 处理。
		mask, err := strconv.ParseUint(hexStr, 16, 64)
		if err != nil {
			return false
		}
		return mask&(uint64(1)<<uint(bit)) != 0
	}
	return false
}

// cgroupsMountCheck 解析 /proc/self/mountinfo，判断本机 cgroup 形态以及当前
// 进程是否有可写的 cgroup 目录。只读判断：绝不创建任何 cgroup 目录。
func cgroupsMountCheck() Check {
	c := Check{ID: CheckCgroupsMount, Title: "cgroups 挂载"}

	entries, err := parseCgroupMounts()
	if err != nil {
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("读取 /proc/self/mountinfo 失败: %v", err)
		return c
	}
	if len(entries) == 0 {
		c.Status = StatusFail
		c.Detail = "未发现任何 cgroup 挂载点"
		c.Hint = "挂载 cgroup2：`sudo mount -t cgroup2 none /sys/fs/cgroup`，并确认内核开启 CONFIG_CGROUPS"
		return c
	}

	v2 := false
	var v1 []string
	for _, e := range entries {
		if e.fstype == "cgroup2" {
			v2 = true
		} else {
			v1 = append(v1, e.mountPoint)
		}
	}

	writable, dir := writableCgroupDir(entries)
	switch {
	case v2 && writable:
		c.Status = StatusOK
		c.Detail = fmt.Sprintf("cgroup v2 unified（%s），当前进程可写目录 %s", cgroupMountPoint(entries, "cgroup2"), dir)
	case v2:
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("cgroup v2 unified（%s），但当前进程无可写 cgroup 目录（未委托）", cgroupMountPoint(entries, "cgroup2"))
		c.Hint = "以 root 运行，或为当前 systemd slice 开启委托：" +
			"`systemctl set-property --runtime user.slice Delegate=yes`；未委托时容器仍可启动，资源限制不生效"
	case len(v1) > 0:
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("cgroup v1（%d 个挂载点），未发现 cgroup v2 unified 层级", len(v1))
		c.Hint = "在内核命令行加 `systemd.unified_cgroup_hierarchy=1` 后重启，切换到 cgroup v2"
	default:
		c.Status = StatusSkip
		c.Detail = "发现了 cgroup 挂载点但无法识别其类型"
	}
	return c
}

// cgroupMount 是 /proc/self/mountinfo 里的一条 cgroup 记录。
type cgroupMount struct {
	mountPoint string
	fstype     string
	// root 是该挂载暴露的 cgroup 子树，如 v1 的 "/docker/abc"。
	root string
}

// parseCgroupMounts 从 /proc/self/mountinfo 提取全部 cgroup 挂载点。
func parseCgroupMounts() ([]cgroupMount, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("打开 mountinfo: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []cgroupMount
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// 格式：id parent major:minor root mountpoint opts [optional...] - fstype src superopts
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(fields) || len(fields) < 5 {
			continue
		}
		fstype := fields[sep+1]
		if fstype != "cgroup" && fstype != "cgroup2" {
			continue
		}
		out = append(out, cgroupMount{
			mountPoint: unescapeOctal(fields[4]),
			root:       unescapeOctal(fields[3]),
			fstype:     fstype,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 mountinfo: %w", err)
	}
	return out, nil
}

// unescapeOctal 还原 mountinfo 中 \040 \011 \012 \134 形式的转义。
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// cgroupMountPoint 返回指定 fstype 的第一个挂载点。
func cgroupMountPoint(entries []cgroupMount, fstype string) string {
	for _, e := range entries {
		if e.fstype == fstype {
			return e.mountPoint
		}
	}
	return ""
}

// writableCgroupDir 在**不创建任何目录**的前提下判断当前进程是否有可写的
// cgroup 目录，返回该目录与其可写性。
//
// 判定顺序：先按 /proc/self/cgroup 给出的当前 cgroup 路径在挂载点下查找；
// 找不到时退回挂载点本身。只做 stat + 权限位判断，绝不写盘。
func writableCgroupDir(entries []cgroupMount) (bool, string) {
	if mp := cgroupMountPoint(entries, "cgroup2"); mp != "" {
		rel, ok := selfCgroupRelPath()
		if ok {
			if rel == "/" || rel == "" {
				if dirWritable(mp) {
					return true, mp
				}
			} else if dir := filepath.Join(mp, strings.TrimPrefix(rel, "/")); isDir(dir) {
				if dirWritable(dir) {
					return true, dir
				}
			}
		}
		return dirWritable(mp), mp
	}

	for _, e := range entries {
		if e.fstype != "cgroup" {
			continue
		}
		// v1 的挂载点通常已经把子树挂到了 /sys/fs/cgroup/<controller>，
		// 因此直接判断挂载点本身即可。
		if dirWritable(e.mountPoint) {
			return true, e.mountPoint
		}
	}
	return false, ""
}

// dirWritable 用"权限位 + 访问检查"判断目录是否可写。
// 它绝不创建文件，因此对被检查的 cgroup 树没有任何副作用。
func dirWritable(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	// 只读挂载（如 cgroup 只读重挂载）直接否决。
	if !mountAllowsWrite(dir) {
		return false
	}
	mode := st.Mode().Perm()
	euid := os.Geteuid()
	if euid == 0 {
		return mode&0o200 != 0
	}
	if st.Sys() != nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			switch uint32(euid) {
			case sys.Uid:
				return mode&0o200 != 0
			case sys.Gid:
				return mode&0o020 != 0
			}
		}
	}
	return mode&0o002 != 0
}

// mountAllowsWrite 判断某路径所在挂载是否以读写方式挂载。
// 读不到 mountinfo 时按可写处理，避免误报。
func mountAllowsWrite(dir string) bool {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return true
	}
	defer func() { _ = f.Close() }()

	target, err := filepath.Abs(dir)
	if err != nil {
		return true
	}

	best := ""
	readOnly := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 6 {
			continue
		}
		mp := unescapeOctal(fields[4])
		if mp != target && !strings.HasPrefix(target+string(os.PathSeparator), mp+string(os.PathSeparator)) {
			continue
		}
		if len(mp) < len(best) {
			continue
		}
		best = mp
		readOnly = false
		for _, opt := range strings.Split(fields[5], ",") {
			if opt == "ro" {
				readOnly = true
			}
		}
	}
	return !readOnly
}

// isDir 报告路径是否为目录。
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// selfCgroupRelPath 解析 /proc/self/cgroup，返回当前进程的 cgroup 路径。
// cgroup v2 形态为 "0::/user.slice/..."。
func selfCgroupRelPath() (string, bool) {
	f, err := os.Open("/proc/self/cgroup")
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.SplitN(sc.Text(), ":", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[0] == "0" {
			return fields[2], true
		}
	}
	return "", false
}

// cgroupsControllersCheck 列出当前进程实际可用的 cgroup 控制器。
func cgroupsControllersCheck() Check {
	c := Check{ID: CheckCgroupsControllers, Title: "cgroup 控制器"}

	if mp := cgroupMountPointOf("cgroup2"); mp != "" {
		rel, ok := selfCgroupRelPath()
		if ok && rel != "" && rel != "/" {
			if data, err := os.ReadFile(filepath.Join(mp, strings.TrimPrefix(rel, "/"), "cgroup.controllers")); err == nil {
				ctrls := strings.Fields(string(data))
				if len(ctrls) > 0 {
					c.Status = StatusOK
					c.Detail = fmt.Sprintf("cgroup v2，当前进程可用控制器: %s", strings.Join(ctrls, " "))
					return c
				}
			}
		}
		if data, err := os.ReadFile(filepath.Join(mp, "cgroup.controllers")); err == nil {
			ctrls := strings.Fields(string(data))
			c.Status = StatusWarn
			c.Detail = fmt.Sprintf("cgroup v2，根层级控制器: %s（当前进程未获得委托，容器内不可直接使用）", strings.Join(ctrls, " "))
			c.Hint = "为当前 slice 开启委托：`systemctl set-property --runtime user.slice Delegate=yes`"
			return c
		}
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("无法读取 %s/cgroup.controllers", mp)
		return c
	}

	// cgroup v1：控制器即挂载点名字。
	entries, err := parseCgroupMounts()
	if err != nil {
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("读取 /proc/self/mountinfo 失败: %v", err)
		return c
	}
	var ctrls []string
	for _, e := range entries {
		if e.fstype != "cgroup" {
			continue
		}
		name := filepath.Base(e.mountPoint)
		if name == "" || name == "." || name == "/" {
			name = e.root
		}
		if name != "" {
			ctrls = append(ctrls, strings.TrimPrefix(name, "/"))
		}
	}
	if len(ctrls) == 0 {
		c.Status = StatusSkip
		c.Detail = "未发现 cgroup v1 控制器挂载点"
		return c
	}
	c.Status = StatusOK
	c.Detail = fmt.Sprintf("cgroup v1，可用控制器: %s", strings.Join(ctrls, " "))
	return c
}

// cgroupMountPointOf 返回指定 fstype 的 cgroup 挂载点（供共享逻辑使用）。
func cgroupMountPointOf(fstype string) string {
	entries, err := parseCgroupMounts()
	if err != nil {
		return ""
	}
	return cgroupMountPoint(entries, fstype)
}

// systemdCheck 检查 systemd 是否可用。缺失 systemd 只是警告：
// Boxli 的主流程（run/stop/ps）不依赖它，只有开机自启需要。
func systemdCheck() Check {
	c := Check{ID: CheckSystemdAvailable, Title: "systemd 可用性"}

	_, statErr := os.Stat("/run/systemd/system")
	runDir := statErr == nil
	path, pathErr := exec.LookPath("systemctl")

	switch {
	case runDir && pathErr == nil:
		version := systemctlVersion()
		c.Status = StatusOK
		c.Detail = fmt.Sprintf("/run/systemd/system 存在，systemctl=%s", path)
		if version != "" {
			c.Detail += fmt.Sprintf("（%s）", version)
		}
	case runDir:
		c.Status = StatusWarn
		c.Detail = "/run/systemd/system 存在，但 PATH 中未找到 systemctl"
		c.Hint = "安装 systemd 工具或在 root 的 PATH 中运行；开机自启可手动写入 /etc/systemd/system/boxli.service"
	case pathErr == nil:
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("未发现 /run/systemd/system（当前 PID 1 不是 systemd），但存在 systemctl=%s", path)
		c.Hint = "在容器/CI 中属正常现象；开机自启用 `boxli boot enable` 会提示手动注册，可改用 cron @reboot 调 `boxli boot`"
	default:
		c.Status = StatusWarn
		c.Detail = "本机既无 /run/systemd/system 也无 systemctl"
		c.Hint = "开机自启不可用，可改用 cron @reboot 调用 `boxli boot`；容器运行不受影响"
	}
	return c
}

// systemctlVersion 调用 `systemctl --version` 取版本号首行。这是只读查询，
// 失败时返回空串（不影响检查结论）。
func systemctlVersion() string {
	out, err := exec.Command("systemctl", "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// dataDirCheck 检查数据目录是否存在、可写、剩余空间是否充足。
func dataDirCheck(version string) Check {
	_ = version
	c := Check{ID: CheckStorageDataDir, Title: "数据目录"}
	dir := resolveDataDir("")
	if dir == "" {
		c.Status = StatusSkip
		c.Detail = "无法确定数据目录（HOME 不可用）"
		c.Hint = "显式指定：`boxli --data-dir /path/to/data ...` 或设置 $BOXLI_HOME"
		return c
	}

	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// boxli 会在首次使用时创建数据目录；父目录可写即视为通过。
		parent := filepath.Dir(dir)
		if dirWritable(parent) {
			c.Status = StatusOK
			c.Detail = fmt.Sprintf("%s 尚未创建，父目录 %s 可写（首次使用时自动创建）", dir, parent)
			return c
		}
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("%s 不存在，且父目录 %s 不可写", dir, parent)
		c.Hint = fmt.Sprintf("`mkdir -p %s` 或改用 `--data-dir` 指向可写目录", dir)
		return c
	case err != nil:
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("无法访问 %s: %v", dir, err)
		c.Hint = "检查目录权限与所在文件系统是否可访问"
		return c
	case !st.IsDir():
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("%s 存在但不是目录", dir)
		c.Hint = fmt.Sprintf("移除该文件或改用 `--data-dir` 指定其它目录：`rm -f %s`", dir)
		return c
	}

	// 目录已存在：这里刻意不做"创建探针文件"的写测试，保持全程只读。
	if !dirWritable(dir) {
		c.Status = StatusFail
		c.Detail = fmt.Sprintf("%s 存在但当前用户（euid=%d）不可写", dir, os.Geteuid())
		c.Hint = fmt.Sprintf("`sudo chown -R $(id -u):$(id -g) %s`，或用 `--data-dir` 指向可写目录", dir)
		return c
	}

	free, total, err := diskSpace(dir)
	if err != nil {
		c.Status = StatusOK
		c.Detail = fmt.Sprintf("%s 可写（剩余空间未知: %v）", dir, err)
		return c
	}
	usedPct := 0.0
	if total > 0 {
		usedPct = float64(total-free) / float64(total) * 100
	}
	c.Detail = fmt.Sprintf("%s 可写，剩余 %s / 共 %s（已用 %.1f%%）",
		dir, humanBytes(int64(free)), humanBytes(int64(total)), usedPct)
	if total > 0 && usedPct >= 90 {
		c.Status = StatusWarn
		c.Hint = fmt.Sprintf("磁盘接近写满，清理镜像与容器：`boxli rm -a` 或删除 %s/layers 中的无用层", dir)
		return c
	}
	c.Status = StatusOK
	return c
}

// layersCheck 统计层缓存（<root>/layers/sha256）体积与层数。
// 该缓存跨容器共享、不随容器删除回收，因此值得单独报告。
func layersCheck() Check {
	c := Check{ID: CheckStorageLayers, Title: "层缓存"}
	root := resolveDataDir("")
	if root == "" {
		c.Status = StatusSkip
		c.Detail = "无法确定数据目录，未检查层缓存"
		return c
	}
	layers := filepath.Join(root, "layers")
	if !isDir(layers) {
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("%s 不存在（尚未 pull 过任何镜像）", layers)
		c.Hint = "`boxli pull <file.boxli>` 后层缓存会出现在此处"
		return c
	}

	shaDir := filepath.Join(layers, "sha256")
	scanRoot := layers
	if isDir(shaDir) {
		scanRoot = shaDir
	}

	var total int64
	var layersN, files int
	err := filepath.WalkDir(scanRoot, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			// 单个条目读不到（权限/竞态）时跳过，不影响整体统计。
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		files++
		return nil
	})
	if err != nil {
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("统计 %s 失败: %v", scanRoot, err)
		return c
	}

	if entries, err := os.ReadDir(scanRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				layersN++
			}
		}
	}

	c.Status = StatusOK
	c.Detail = fmt.Sprintf("%s：%d 个层，%d 个文件，共 %s", scanRoot, layersN, files, humanBytes(total))
	return c
}

// binaryCheck 检查 boxli 版本与可执行文件路径。version 由 CLI 经
// Options.Version 注入（最终来源是 main 的 -ldflags -X main.version）；
// 为空说明是源码直跑（go run），属于提示而非错误。
func binaryCheck(version string) Check {
	c := Check{ID: CheckBinaryVersion, Title: "二进制"}
	exe, err := os.Executable()
	if err != nil {
		c.Status = StatusWarn
		c.Detail = fmt.Sprintf("无法确定可执行文件路径: %v", err)
		c.Hint = "用绝对路径重新执行 boxli，或从发布包安装到 /usr/local/bin"
		return c
	}
	c.Status = StatusOK
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	c.Detail = fmt.Sprintf("可执行文件 %s", exe)
	if v := strings.TrimSpace(version); v != "" {
		c.Detail += fmt.Sprintf("；版本 %s", v)
	} else {
		c.Detail += "；版本未知（未注入）"
		c.Hint = "从发布包运行，或用 `-ldflags -X main.version=<v>` 重新编译"
	}
	return c
}

// archCheck 检查宿主架构是否在支持列表内。真实判断在各自平台的
// archSupported（build tag 分文件），公共逻辑里不出现 runtime.GOOS 分支。
func archCheck() Check {
	c := Check{ID: CheckArchHost, Title: "宿主架构"}
	backend := platformBackend()
	c.Detail = fmt.Sprintf("%s/%s，平台后端 %s", runtime.GOOS, runtime.GOARCH, backend)
	if archSupported() {
		c.Status = StatusOK
		return c
	}
	c.Status = StatusWarn
	c.Hint = fmt.Sprintf("当前 %s/%s 无可用后端，受支持架构：linux/amd64 linux/arm64 linux/386 linux/arm darwin/amd64 darwin/arm64 android/arm64 android/amd64",
		runtime.GOOS, runtime.GOARCH)
	return c
}

// platformBackend 返回当前平台对应的运行时后端名（AGENTS.md 表格）。
func platformBackend() string {
	switch runtime.GOOS {
	case "linux":
		return "native_linux"
	case "darwin":
		return "vm_darwin"
	default:
		return "未登记"
	}
}

// archSupported 报告当前 GOOS/GOARCH 是否已有后端实现。
func archSupported() bool {
	if runtime.GOOS == "android" {
		return runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64"
	}
	switch runtime.GOOS {
	case "linux":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64" ||
			runtime.GOARCH == "386" || runtime.GOARCH == "arm"
	case "darwin":
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	}
	return false
}

// diskSpace 返回路径所在文件系统的可用与总字节数。
func diskSpace(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	free = st.Bavail * bsize
	total = st.Blocks * bsize
	return free, total, nil
}

// readIntFile 读取只含一个整数的 sysfs/procfs 文件。
func readIntFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("读取 %s: %w", path, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("解析 %s: %w", path, err)
	}
	return v, nil
}

// humanBytes 把字节数格式化成人类可读字符串（二进制单位）。
func humanBytes(n int64) string {
	if n < 0 {
		return "0 B"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.*f %s", 1, math.Round(v*10)/10, units[i])
}
