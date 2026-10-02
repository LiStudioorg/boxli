// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrNoNamespaces 表示内核没有提供容器所需的任何命名空间隔离能力。
//
// 与裸 EPERM 的区别：EPERM 只说明"这次 fork 被拒"，可能是权限问题；本错误
// 说明"探测已确认所需 namespace 一个都不可用"，属于内核能力缺失，重试与提权
// 都不会解决。错误信息必须包含检测到什么、缺什么、怎么排查。
var ErrNoNamespaces = errors.New("内核未提供所需的 namespace 隔离能力")

// procSelfNS 是当前进程的 namespace 目录，做成变量以便测试注入。
var procSelfNS = "/proc/self/ns"

// maxUserNS 是 user namespace 数量上限文件，做成变量以便测试注入。
var maxUserNS = "/proc/sys/user/max_user_namespaces"

// requiredNamespaces 是启动容器**必需**的 namespace。
//
// 少了任何一个都无法构成容器：没有 pid 就没有进程隔离，没有 mnt 就无法
// pivot_root，没有 uts 无法设 hostname，没有 ipc 则共享宿主 IPC。
// net 不在其中——host 网络模式本就不需要独立 netns。
var requiredNamespaces = []string{"pid", "mnt", "uts", "ipc"}

// netNamespace 是需要按网络模式决定是否要的 namespace。
const netNamespace = "net"

// userNamespace 是可选的、用于 rootless 的 namespace。
const userNamespace = "user"

// nsProbe 是一次 namespace 可用性探测的结果。
type nsProbe struct {
	// Available 是 /proc/self/ns 下实际存在的 namespace 名集合。
	Available map[string]bool
	// UserNSAllowed 表示内核允许创建 user namespace。
	// 由 max_user_namespaces > 0 且 /proc/self/ns/user 存在共同判定。
	UserNSAllowed bool
	// ProcNSErr 是读取 /proc/self/ns 失败时的错误（非 nil 表示连探测都无法完成）。
	ProcNSErr error
}

// probeNamespaces 探测当前内核提供的 namespace 能力。
//
// **不做任何 fork/unshare**：探测必须是只读的、无副作用的。用
// `unshare -U` 之类的探测方式会产生真实副作用，且在被拒绝时无法区分
// "内核不支持"与"策略拒绝"。这里只读 /proc/self/ns 与
// /proc/sys/user/max_user_namespaces。
func probeNamespaces() nsProbe {
	return probeNamespacesAt(procSelfNS, maxUserNS)
}

// probeNamespacesAt 是 probeNamespaces 的可注入实现，路径由参数给出。
func probeNamespacesAt(nsDir, maxUserNSPath string) nsProbe {
	p := nsProbe{Available: map[string]bool{}}

	entries, err := os.ReadDir(nsDir)
	if err != nil {
		p.ProcNSErr = fmt.Errorf("读取 %s: %w", nsDir, err)
		return p
	}
	for _, e := range entries {
		// /proc/self/ns 下的条目即该 namespace 可用。
		// pid_for_children / time_for_children 是别名目录，不是独立能力，
		// 但仍然无害——真正关心的名字由 requiredNamespaces 决定。
		p.Available[e.Name()] = true
	}

	// user namespace：先看数量上限，再看 ns 目录项。
	// 上限为 0 或负数表示内核禁止创建 user namespace（部分 Android 内核、
	// 或 sysctl 被设成 0），此时即使 /proc/self/ns/user 存在也不能用。
	allowed := true
	if data, err := os.ReadFile(maxUserNSPath); err == nil {
		if n, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && n <= 0 {
			allowed = false
		}
	}
	p.UserNSAllowed = allowed && p.Available["user"]
	return p
}

// missingRequired 返回缺失的必需 namespace 名（按固定顺序）。
func (p nsProbe) missingRequired() []string {
	var missing []string
	for _, n := range requiredNamespaces {
		if !p.Available[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

// nsPlan 描述本次启动实际要用的 namespace 组合与降级理由。
type nsPlan struct {
	// Flags 是要传给 clone 的 CLONE_NEW* 位。
	Flags uintptr
	// UseUserNamespace 表示是否附加 CLONE_NEWUSER（rootless）。
	UseUserNamespace bool
	// Degraded 表示走了降级路径（可用但非最优），需要 Warn 级日志。
	Degraded bool
	// Reason 是降级原因，用于日志。
	Reason string
}

// planNamespaces 决定本次容器启动使用的 namespace 组合。
//
// 决策表：
//
//	euid=0 且 userns 不可用     → 必需 ns + 可选 net，**不加** CLONE_NEWUSER
//	                              （Android 有 Root 的常见形态；root 本就有
//	                              权建其它 namespace，无需 userns）
//	euid=0 且 userns 可用       → 同上，root 下也无需 userns
//	                              （用 userns 反而会把容器内 root 映射掉）
//	euid≠0 且 userns 可用       → 追加 CLONE_NEWUSER（rootless 路径）
//	euid≠0 且 userns 不可用     → 返回 ErrNotRoot（无权建 ns 且无 userns 兜底）
//	必需 ns 缺失                → 返回 ErrNoNamespaces（内核能力缺失）
//
// wantNet 由调用方按网络模式给出：bridge/none 需要独立 netns，host 不需要。
//
// 注意：本函数只做决策，不做任何系统调用或 fork，因此可完整单元测试。
func planNamespaces(p nsProbe, euid int, wantNet bool) (nsPlan, error) {
	// 1. 内核能力缺失是最硬的失败：先报这个，避免用户去查权限。
	if p.ProcNSErr != nil {
		return nsPlan{}, fmt.Errorf("%w：无法读取 %s（%v）。"+
			"请确认 /proc 已挂载且可读；在受限容器/沙箱中运行时可能被屏蔽",
			ErrNoNamespaces, procSelfNS, p.ProcNSErr)
	}
	if missing := p.missingRequired(); len(missing) > 0 {
		return nsPlan{}, fmt.Errorf("%w：缺少必需 namespace %v（已探测到 %v）。"+
			"排查：确认内核配置启用 CONFIG_NAMESPACES 及对应子项，"+
			"并检查 /proc/self/ns 下是否可见；Android 需 Root",
			ErrNoNamespaces, missing, availableNames(p))
	}

	// 2. 组装必需位。
	flags := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS | syscall.CLONE_NEWIPC)
	plan := nsPlan{Flags: flags}

	// 2.1 网络 namespace 是**可选**的：缺失时可降级为共享宿主网络栈，
	//     但这是真实的能力下降（容器不再有独立网络），必须 Warn 而非静默。
	if wantNet {
		if p.Available[netNamespace] {
			flags |= syscall.CLONE_NEWNET
			plan.Flags = flags
		} else {
			plan.Degraded = true
			plan.Reason = "内核未提供 net namespace，容器将共享宿主网络栈（无独立网络隔离）；" +
				"若需独立网络请确认内核启用 CONFIG_NET_NS"
		}
	}

	// 3. rootless 与降级判定。
	if euid == 0 {
		// root：有权限直接建 namespace，**不需要** userns。
		//
		// 这里**不是降级**：root 直接创建 PID/MNT/UTS/IPC/NET 已获得完整隔离；
		// 反而若强行加 CLONE_NEWUSER，容器内 root 会被映射成普通用户，失去挂载
		// 与建 namespace 的能力——那才是能力下降。
		// 因此 userns 不可用在 Android 有 Root 形态下属**正常路径**，记 Debug。
		if !p.UserNSAllowed {
			slog.Debug("user namespace 不可用，但当前为 root：直接创建其它 namespace，"+
				"隔离完整，不使用 CLONE_NEWUSER（Android 常见形态）",
				slog.Bool("userNSAllowed", p.UserNSAllowed))
		}
		return plan, nil
	}

	// 非 root：必须靠 user namespace 才能获得建 namespace 的权限。
	if !p.UserNSAllowed {
		return nsPlan{}, fmt.Errorf("%w：当前 euid=%d 非 root，且 user namespace 不可用"+
			"（%s 为 0 或 /proc/self/ns/user 缺失）。"+
			"排查：以 root 运行可绕过该限制；或检查 sysctl kernel.unprivileged_userns_clone"+
			" 与 /proc/sys/user/max_user_namespaces",
			ErrNotRoot, euid, maxUserNS)
	}
	plan.UseUserNamespace = true
	return plan, nil
}

// availableNames 返回已探测到的 namespace 名（排序后），用于错误信息。
func availableNames(p nsProbe) []string {
	names := make([]string, 0, len(p.Available))
	for n, ok := range p.Available {
		if ok {
			names = append(names, n)
		}
	}
	sortStrings(names)
	return names
}

// sortStrings 是插入排序：namespace 名数量极少（<10），避免为此引入 sort 依赖路径。
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// resolveNSPlan 是 StartWith 使用的入口：探测 + 决策 + 日志。
//
// 日志级别约定：
//   - Debug：正常跳过（如 root 下不用 userns）
//   - Warn ：走了降级但仍可运行
//   - Error：致命，由返回的错误体现（调用方负责打印）
func resolveNSPlan(wantNet bool) (nsPlan, error) {
	probe := probeNamespaces()
	plan, err := planNamespaces(probe, os.Geteuid(), wantNet)
	if err != nil {
		slog.Error("namespace 规划失败，容器无法启动", slog.Any("err", err))
		return nsPlan{}, err
	}
	if plan.Degraded {
		slog.Warn("容器以降级 namespace 组合启动", slog.String("reason", plan.Reason))
	}
	return plan, nil
}
