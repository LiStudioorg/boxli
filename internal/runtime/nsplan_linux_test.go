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

// fakeNSDir 造一个含指定 namespace 条目的假 /proc/self/ns 目录。
func fakeNSDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ns")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		// /proc/self/ns 下都是符号链接；测试里用普通文件即可（只判存在性）。
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeMaxUserNS 造一个内容为 n 的 max_user_namespaces 文件。
func fakeMaxUserNS(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "max_user_namespaces")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// allNS 是"全部可用"的 namespace 集合。
var allNS = []string{"pid", "mnt", "uts", "ipc", "net", "user", "cgroup"}

// TestProbeNamespacesAtAllAvailable 全覆盖探测：全部 namespace 就位。
func TestProbeNamespacesAtAllAvailable(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "15177\n"))
	if p.ProcNSErr != nil {
		t.Fatalf("不应报错: %v", p.ProcNSErr)
	}
	for _, n := range allNS {
		if !p.Available[n] {
			t.Errorf("%s 应可用", n)
		}
	}
	if !p.UserNSAllowed {
		t.Error("max_user_namespaces>0 且 user ns 存在时应允许")
	}
	if m := p.missingRequired(); len(m) != 0 {
		t.Errorf("不应有缺失, got %v", m)
	}
}

// TestProbeNamespacesUserNSDisabledBySysctl 覆盖 sysctl 上限为 0：
// 即使 /proc/self/ns/user 存在也必须判定为不可用。
func TestProbeNamespacesUserNSDisabledBySysctl(t *testing.T) {
	for _, v := range []string{"0", "0\n", "-1"} {
		p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, v))
		if p.UserNSAllowed {
			t.Errorf("max_user_namespaces=%q 时应判定 userns 不可用", v)
		}
	}
}

// TestProbeNamespacesUserNSMissingEntry 覆盖 ns 目录里没有 user 条目。
func TestProbeNamespacesUserNSMissingEntry(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, "pid", "mnt", "uts", "ipc", "net"), fakeMaxUserNS(t, "15177"))
	if p.UserNSAllowed {
		t.Error("/proc/self/ns/user 缺失时应判定 userns 不可用")
	}
}

// TestProbeNamespacesUnreadableDir 覆盖连 ns 目录都读不到。
func TestProbeNamespacesUnreadableDir(t *testing.T) {
	p := probeNamespacesAt(filepath.Join(t.TempDir(), "nonexistent"), fakeMaxUserNS(t, "1"))
	if p.ProcNSErr == nil {
		t.Fatal("目录不存在时应记录错误")
	}
}

// TestProbeNamespacesUnreadableSysctlIsPermissive 覆盖 sysctl 读不到：
// 读不到不应武断判定为"不可用"，而是回退到只看 ns 目录项（保守放行）。
func TestProbeNamespacesUnreadableSysctlIsPermissive(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), filepath.Join(t.TempDir(), "nonexistent"))
	if !p.UserNSAllowed {
		t.Error("sysctl 不可读但 ns 条目存在时，应保守判定为可用")
	}
}

// TestPlanNamespacesRootWithUserNS 覆盖 root + userns 可用：
// root 直接建 namespace 即可，**不**加 CLONE_NEWUSER。
func TestPlanNamespacesRootWithUserNS(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "15177"))
	plan, err := planNamespaces(p, 0, true)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if plan.UseUserNamespace {
		t.Error("root 下不应使用 CLONE_NEWUSER（会把容器内 root 映射掉）")
	}
	want := uintptr(syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWIPC | syscall.CLONE_NEWNET)
	if plan.Flags != want {
		t.Fatalf("flags = %#x, want %#x", plan.Flags, want)
	}
}

// TestPlanNamespacesRootWithoutUserNS 覆盖 **root + userns 不可用** ——
// Android 有 Root 的常见形态，必须正常规划出无 userns 的组合。
func TestPlanNamespacesRootWithoutUserNS(t *testing.T) {
	// 只给必需 ns + net，不给 user。
	p := probeNamespacesAt(fakeNSDir(t, "pid", "mnt", "uts", "ipc", "net"), fakeMaxUserNS(t, "0"))
	plan, err := planNamespaces(p, 0, true)
	if err != nil {
		t.Fatalf("root + 无 userns 应可正常启动: %v", err)
	}
	if plan.UseUserNamespace {
		t.Error("userns 不可用时不应使用 CLONE_NEWUSER")
	}
	if plan.Flags&syscall.CLONE_NEWUSER != 0 {
		t.Error("flags 中不应含 CLONE_NEWUSER")
	}
	// 其它必需位必须齐全——降级不等于放弃隔离。
	for _, f := range []uintptr{syscall.CLONE_NEWPID, syscall.CLONE_NEWNS,
		syscall.CLONE_NEWUTS, syscall.CLONE_NEWIPC, syscall.CLONE_NEWNET} {
		if plan.Flags&f == 0 {
			t.Errorf("flags 缺少 %#x", f)
		}
	}
}

// TestPlanNamespacesRootless 覆盖非 root + userns 可用：必须走 CLONE_NEWUSER。
func TestPlanNamespacesRootless(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "15177"))
	plan, err := planNamespaces(p, 1000, true)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !plan.UseUserNamespace {
		t.Error("非 root 且 userns 可用时必须使用 CLONE_NEWUSER")
	}
}

// TestPlanNamespacesRootlessWithoutUserNS 覆盖非 root + userns 不可用：
// 必须报 ErrNotRoot，且错误信息说明排查方向。
func TestPlanNamespacesRootlessWithoutUserNS(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, "pid", "mnt", "uts", "ipc", "net"), fakeMaxUserNS(t, "0"))
	_, err := planNamespaces(p, 1000, true)
	if err == nil {
		t.Fatal("非 root 且无 userns 时应报错")
	}
	if !errors.Is(err, ErrNotRoot) {
		t.Fatalf("应包裹 ErrNotRoot, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"root", "user namespace", "max_user_namespaces"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应含 %q 以便排查: %v", want, msg)
		}
	}
}

// TestPlanNamespacesMissingRequired 覆盖**内核完全没有 namespace**：
// 必须报 ErrNoNamespaces 并列出缺什么，而不是裸 EPERM。
func TestPlanNamespacesMissingRequired(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t), fakeMaxUserNS(t, "15177")) // 空 ns 目录
	_, err := planNamespaces(p, 0, true)
	if err == nil {
		t.Fatal("无任何 namespace 时应报错")
	}
	if !errors.Is(err, ErrNoNamespaces) {
		t.Fatalf("应包裹 ErrNoNamespaces, got %v", err)
	}
	msg := err.Error()
	// 错误必须说清：缺什么、怎么排查。
	for _, want := range []string{"缺少必需 namespace", "pid", "mnt", "uts", "ipc", "CONFIG_NAMESPACES"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应含 %q: %v", want, msg)
		}
	}
}

// TestPlanNamespacesPartialMissing 覆盖只缺一部分（如缺 mnt）：
// 同样必须报 ErrNoNamespaces 并精确列出缺的那一个。
func TestPlanNamespacesPartialMissing(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, "pid", "uts", "ipc", "net"), fakeMaxUserNS(t, "15177"))
	_, err := planNamespaces(p, 0, true)
	if !errors.Is(err, ErrNoNamespaces) {
		t.Fatalf("应包裹 ErrNoNamespaces, got %v", err)
	}
	if !strings.Contains(err.Error(), "mnt") {
		t.Errorf("错误应指出缺 mnt: %v", err)
	}
	if strings.Contains(err.Error(), "缺少必需 namespace [pid") {
		t.Errorf("不应把已存在的 pid 列为缺失: %v", err)
	}
}

// TestPlanNamespacesUnreadableProc 覆盖 /proc/self/ns 读不到：
// 报 ErrNoNamespaces 且提示检查 /proc 挂载。
func TestPlanNamespacesUnreadableProc(t *testing.T) {
	p := probeNamespacesAt(filepath.Join(t.TempDir(), "nope"), fakeMaxUserNS(t, "1"))
	_, err := planNamespaces(p, 0, true)
	if !errors.Is(err, ErrNoNamespaces) {
		t.Fatalf("应包裹 ErrNoNamespaces, got %v", err)
	}
	if !strings.Contains(err.Error(), "/proc") {
		t.Errorf("错误应提示 /proc: %v", err)
	}
}

// TestPlanNamespacesHostNetwork 覆盖 host 网络模式：不应加 CLONE_NEWNET。
func TestPlanNamespacesHostNetwork(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "15177"))
	plan, err := planNamespaces(p, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Flags&syscall.CLONE_NEWNET != 0 {
		t.Error("host 网络模式不应含 CLONE_NEWNET")
	}
	// 但必需位仍要在。
	if plan.Flags&syscall.CLONE_NEWPID == 0 {
		t.Error("host 网络模式下仍应有 CLONE_NEWPID")
	}
}

// TestPlanNamespacesNeverSilentlyDowngradesRootless 是安全约束：
// 非 root 且 userns 不可用时**绝不能**降级成"以当前身份继续"。
func TestPlanNamespacesNeverSilentlyDowngradesRootless(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "0"))
	plan, err := planNamespaces(p, 1000, true)
	if err == nil {
		t.Fatalf("必须报错而不是静默降级；plan=%+v", plan)
	}
	if plan.Flags != 0 || plan.UseUserNamespace {
		t.Errorf("失败时不应返回可用的 plan: %+v", plan)
	}
}

// TestProbeNamespacesRealHost 在真实宿主上探测：必须成功且必需 ns 齐全。
func TestProbeNamespacesRealHost(t *testing.T) {
	p := probeNamespaces()
	if p.ProcNSErr != nil {
		t.Fatalf("真实宿主探测失败: %v", p.ProcNSErr)
	}
	if m := p.missingRequired(); len(m) > 0 {
		t.Fatalf("真实宿主缺少必需 namespace: %v", m)
	}
	t.Logf("宿主 namespace: %v, userns 允许=%v", availableNames(p), p.UserNSAllowed)

	// 真实宿主上必须能规划出可用的组合。
	plan, err := planNamespaces(p, os.Geteuid(), true)
	if err != nil {
		t.Fatalf("真实宿主上应能规划: %v", err)
	}
	t.Logf("规划结果: flags=%#x userns=%v", plan.Flags, plan.UseUserNamespace)
}

// TestSortStrings 覆盖内部排序助手（错误信息可复现依赖它）。
func TestSortStrings(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{[]string{"pid", "mnt", "ipc"}, []string{"ipc", "mnt", "pid"}},
		{[]string{}, []string{}},
		{[]string{"b"}, []string{"b"}},
		{[]string{"z", "a", "m", "a"}, []string{"a", "a", "m", "z"}},
	}
	for _, tc := range cases {
		got := append([]string(nil), tc.in...)
		sortStrings(got)
		if len(got) != len(tc.want) {
			t.Fatalf("len %v != %v", got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("sortStrings(%v) = %v, want %v", tc.in, got, tc.want)
			}
		}
	}
}

// TestPlanNamespacesMissingNetDegrades 覆盖**真正的降级路径**：
// 内核没有 net namespace 但容器需要独立网络 —— 必须标记 Degraded 并给出理由，
// 让用户知道容器网络与预期不同，而不是静默共享宿主网络。
func TestPlanNamespacesMissingNetDegrades(t *testing.T) {
	// 必需 ns 齐全，但没有 net。
	p := probeNamespacesAt(fakeNSDir(t, "pid", "mnt", "uts", "ipc", "user"), fakeMaxUserNS(t, "15177"))
	plan, err := planNamespaces(p, 0, true)
	if err != nil {
		t.Fatalf("缺 net 应降级而非报错: %v", err)
	}
	if !plan.Degraded {
		t.Error("缺 net 且需要独立网络时必须标记 Degraded，否则用户无从得知网络未隔离")
	}
	if plan.Reason == "" {
		t.Error("降级必须带原因说明")
	}
	if !strings.Contains(plan.Reason, "net") {
		t.Errorf("原因应指明 net namespace: %q", plan.Reason)
	}
	if plan.Flags&syscall.CLONE_NEWNET != 0 {
		t.Error("无 net namespace 时不应设置 CLONE_NEWNET（会导致 fork 失败）")
	}
	// 其它必需位仍要齐全。
	if plan.Flags&syscall.CLONE_NEWPID == 0 {
		t.Error("降级后仍必须有 PID 隔离")
	}
}

// TestPlanNamespacesMissingNetHostMode 覆盖 host 网络模式缺 net：
// 本就不需要 netns，因此**不应**标记降级。
func TestPlanNamespacesMissingNetHostMode(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, "pid", "mnt", "uts", "ipc"), fakeMaxUserNS(t, "15177"))
	plan, err := planNamespaces(p, 0, false)
	if err != nil {
		t.Fatalf("host 模式不缺 net 需求: %v", err)
	}
	if plan.Degraded {
		t.Errorf("host 网络模式不需要 netns，不应标记降级: %q", plan.Reason)
	}
}

// TestPlanNamespacesRootNoUserNSIsNotDegraded 是**关键语义测试**：
// root + userns 不可用是 Android 有 Root 的**正常路径**（隔离完整），
// 绝不能标记为降级 —— 否则每次启动都会打出误导性 Warn。
func TestPlanNamespacesRootNoUserNSIsNotDegraded(t *testing.T) {
	p := probeNamespacesAt(fakeNSDir(t, allNS...), fakeMaxUserNS(t, "0"))
	plan, err := planNamespaces(p, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Degraded {
		t.Errorf("root 下 userns 不可用不是降级（隔离完整），不应 Warn: %q", plan.Reason)
	}
	if plan.Flags&syscall.CLONE_NEWNET == 0 {
		t.Error("net ns 存在时应使用独立网络")
	}
}

// TestResolveNSPlanOnFakeProbe 覆盖 resolveNSPlan 的编排与日志分支：
// 探测路径本来就是 seam（procSelfNS / maxUserNS），指到假目录即可分别
// 走通 成功 / 降级 / 致命 三条分支。
func TestResolveNSPlanOnFakeProbe(t *testing.T) {
	oldNS, oldMax := procSelfNS, maxUserNS
	t.Cleanup(func() { procSelfNS, maxUserNS = oldNS, oldMax })

	t.Run("degraded-net", func(t *testing.T) {
		// 必需 ns 齐全但没有 net，且要求独立网络 → Degraded + Warn 分支。
		procSelfNS = fakeNSDir(t, "pid", "mnt", "uts", "ipc", "user")
		maxUserNS = fakeMaxUserNS(t, "1")
		plan, err := resolveNSPlan(true)
		if err != nil {
			// 非 root 时 rootless 判定可能先行报错，两种结果都可接受，
			// 关键是不得 panic；root 环境下断言降级。
			if !errors.Is(err, ErrNotRoot) {
				t.Fatalf("意外错误: %v", err)
			}
			return
		}
		if !plan.Degraded {
			t.Error("缺 net 且 wantNet 应 Degraded")
		}
	})

	t.Run("fatal-no-ns", func(t *testing.T) {
		// 空 ns 目录 → ErrNoNamespaces（Error 日志分支）。
		procSelfNS = fakeNSDir(t)
		_, err := resolveNSPlan(false)
		if err == nil || !errors.Is(err, ErrNoNamespaces) {
			t.Fatalf("应 ErrNoNamespaces, got %v", err)
		}
	})

	t.Run("normal", func(t *testing.T) {
		procSelfNS = fakeNSDir(t, allNS...)
		maxUserNS = fakeMaxUserNS(t, "100")
		plan, err := resolveNSPlan(true)
		switch {
		case err == nil:
			if plan.Flags&syscall.CLONE_NEWPID == 0 || plan.Flags&syscall.CLONE_NEWNET == 0 {
				t.Errorf("plan 缺位: %#x", plan.Flags)
			}
		case errors.Is(err, ErrNotRoot):
			// 非 root 且（该假目录里的）userns 判定未过 → 合法分支。
		default:
			t.Fatalf("意外错误: %v", err)
		}
	})
}
