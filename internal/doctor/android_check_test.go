// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package doctor

import (
	"errors"
	"strings"
	"testing"
)

// withFakeAndroidEnv 用探测缝注入一份假的 Android 探测结果。
//
// 全部用例都不依赖真机：android.env 的判定只吃 *AndroidEnv，因此把探测结果
// 直接喂给检查函数即可覆盖每一个能力组合。缝在测试结束时还原。
func withFakeAndroidEnv(t *testing.T, env *AndroidEnv, err error) {
	t.Helper()
	prev := detectAndroidEnvFn
	detectAndroidEnvFn = func() (*AndroidEnv, error) { return env, err }
	t.Cleanup(func() { detectAndroidEnvFn = prev })
}

// androidEnvForTest 造一份"能力齐备的 Android"作为基线，各用例只改自己关心
// 的那一项，避免每个用例都写一长串字段。
func androidEnvForTest() *AndroidEnv {
	env := &AndroidEnv{
		IsAndroid:     true,
		Root:          true,
		Version:       "14",
		SDK:           34,
		Model:         "Pixel 7",
		KernelRelease: "5.15.78-android13",
		CgroupMode:    CgroupV2,
		CgroupRoots:   []string{"/sys/fs/cgroup"},
		SELinux:       SELinuxPermissive,
		UserNS:        true,
		Namespaces: map[string]bool{
			"pid": true, "mnt": true, "uts": true, "ipc": true,
			"net": true, "user": true, "cgroup": true,
		},
	}
	deriveAndroidWarnings(env)
	return env
}

// TestAndroidEnvCheckNonAndroidIsFolded 覆盖需求 2：非 Android 平台上这一项
// 折叠为一行跳过，不进入任何 Android 判定。
func TestAndroidEnvCheckNonAndroidIsFolded(t *testing.T) {
	c := androidEnvCheck(&AndroidEnv{IsAndroid: false, Namespaces: map[string]bool{}, CgroupMode: CgroupNone})
	if c.ID != CheckAndroidEnv {
		t.Fatalf("ID = %q，期望 %q", c.ID, CheckAndroidEnv)
	}
	if c.Status != StatusSkip {
		t.Fatalf("非 Android 应折叠为 %q，实际 %q", StatusSkip, c.Status)
	}
	if c.Hint != "" {
		t.Fatalf("折叠项不该给建议，实际 %q", c.Hint)
	}
	if !strings.Contains(c.Detail, "非 Android") {
		t.Fatalf("详情应说明非 Android，实际 %q", c.Detail)
	}

	// nil 也必须安全（防御性：调用方可能传空）。
	if c := androidEnvCheck(nil); c.Status != StatusSkip {
		t.Fatalf("nil 环境应折叠为 %q，实际 %q", StatusSkip, c.Status)
	}
}

// TestAndroidEnvCheckHealthyIsOK 覆盖需求 1：能力齐备时给出 OK 与完整明细。
func TestAndroidEnvCheckHealthyIsOK(t *testing.T) {
	c := androidEnvCheck(androidEnvForTest())
	if c.Status != StatusOK {
		t.Fatalf("能力齐备应为 %q，实际 %q（detail=%q）", StatusOK, c.Status, c.Detail)
	}
	if c.Hint != "" {
		t.Fatalf("OK 项不该给建议，实际 %q", c.Hint)
	}
	// 明细必须包含需求列出的每一项：版本/SDK/型号、root、cgroup、SELinux、
	// namespace 矩阵、userns。
	for _, want := range []string{"14", "API 34", "Pixel 7", "root", "cgroup v2", "SELinux", "namespace 可用", "user namespace"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("详情缺少 %q：\n%s", want, c.Detail)
		}
	}
}

// TestAndroidEnvCheckMissingRequiredNamespaceFails 覆盖"必需 namespace 缺失"
// 组合：与 nsplan 的 ErrNoNamespaces 硬失败对齐，记 Fail 并给出排查建议。
func TestAndroidEnvCheckMissingRequiredNamespaceFails(t *testing.T) {
	for _, ns := range requiredNamespaces {
		t.Run("缺"+ns, func(t *testing.T) {
			env := androidEnvForTest()
			delete(env.Namespaces, ns)
			c := androidEnvCheck(env)
			if c.Status != StatusFail {
				t.Fatalf("缺 %s namespace 应记 %q，实际 %q", ns, StatusFail, c.Status)
			}
			if !strings.Contains(c.Hint, ns) {
				t.Errorf("建议里应点名缺失的 namespace %q，实际 %q", ns, c.Hint)
			}
			if !strings.Contains(c.Detail, "缺失 "+ns) {
				t.Errorf("详情应列出缺失项，实际 %q", c.Detail)
			}
		})
	}
}

// TestAndroidEnvCheckOptionalNamespaceMissingIsNotFail 覆盖反向边界：net/cgroup
// 等非必需 namespace 缺失**不得**判失败（net 缺失由 nsplan 记 Degraded，不是
// 硬失败）——避免把"可选能力不足"误升级为"环境不可用"。
func TestAndroidEnvCheckOptionalNamespaceMissingIsNotFail(t *testing.T) {
	for _, ns := range []string{"net", "cgroup"} {
		t.Run("缺"+ns, func(t *testing.T) {
			env := androidEnvForTest()
			delete(env.Namespaces, ns)
			c := androidEnvCheck(env)
			if c.Status == StatusFail {
				t.Fatalf("可选 namespace %s 缺失不应记 %q（detail=%q）", ns, StatusFail, c.Detail)
			}
		})
	}
}

// TestAndroidEnvCheckRootStatus 覆盖 root / 非 root / userns 的四种组合。
func TestAndroidEnvCheckRootStatus(t *testing.T) {
	cases := []struct {
		name   string
		root   bool
		userns bool
		want   Status
	}{
		{"root+userns", true, true, StatusOK},
		// Android 常见形态：root 且 userns 被 ROM 禁用 —— 正常路径，不是降级。
		{"root+无 userns", true, false, StatusOK},
		// 非 root 但有 userns：rootless 理论可用，官方仍不支持 Android 无 Root。
		{"非 root+userns", false, true, StatusWarn},
		// 非 root 且无 userns：没有任何可用隔离手段，对应 ErrNotRoot 硬失败。
		{"非 root+无 userns", false, false, StatusFail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := androidEnvForTest()
			env.Root = tc.root
			env.UserNS = tc.userns
			c := androidEnvCheck(env)
			if c.Status != tc.want {
				t.Fatalf("root=%v userns=%v 期望 %q，实际 %q（detail=%q hint=%q）",
					tc.root, tc.userns, tc.want, c.Status, c.Detail, c.Hint)
			}
			if tc.want != StatusOK && c.Hint == "" {
				t.Errorf("降级项必须给出排查建议，实际为空")
			}
		})
	}
}

// TestAndroidEnvCheckRootWithoutUserNSIsNotWarn 锁定 3.3 的语义：root 且
// userns 不可用时，检查项**不得**因 userns 一项而报警——这是 Android 常态，
// 按"隔离确实变弱"才算降级。与 runtime 的
// TestPlanNamespacesRootNoUserNSIsNotDegraded 是同一个约定的两侧。
func TestAndroidEnvCheckRootWithoutUserNSIsNotWarn(t *testing.T) {
	env := androidEnvForTest()
	env.UserNS = false
	env.CgroupMode = CgroupV2
	env.SELinux = SELinuxPermissive
	env.Warnings = nil // 只看检查项自身的判定，不被 warnings 文本干扰
	c := androidEnvCheck(env)
	if c.Status != StatusOK {
		t.Fatalf("root+无 userns 不应降级，实际 %q（hint=%q）", c.Status, c.Hint)
	}
}

// TestAndroidEnvCheckCgroupAndSELinuxWarn 覆盖无 cgroup 与 enforcing 两个降级组合。
func TestAndroidEnvCheckCgroupAndSELinuxWarn(t *testing.T) {
	t.Run("无 cgroup", func(t *testing.T) {
		env := androidEnvForTest()
		env.CgroupMode = CgroupNone
		env.CgroupRoots = nil
		c := androidEnvCheck(env)
		if c.Status != StatusWarn {
			t.Fatalf("无 cgroup 应记 %q，实际 %q", StatusWarn, c.Status)
		}
		if !strings.Contains(c.Hint, "cgroup") {
			t.Errorf("建议应指向 cgroup 排查，实际 %q", c.Hint)
		}
		if !strings.Contains(c.Detail, "未探测到") {
			t.Errorf("详情应说明 cgroup 未探测到，实际 %q", c.Detail)
		}
	})

	t.Run("SELinux enforcing", func(t *testing.T) {
		env := androidEnvForTest()
		env.SELinux = SELinuxEnforcing
		c := androidEnvCheck(env)
		if c.Status != StatusWarn {
			t.Fatalf("enforcing 应记 %q，实际 %q", StatusWarn, c.Status)
		}
		if !strings.Contains(c.Hint, "avc") {
			t.Errorf("建议应给出 avc 排查方向，实际 %q", c.Hint)
		}
		// 绝不建议关 SELinux：排查建议里不得出现 setenforce 0 这类引导。
		if strings.Contains(c.Hint, "setenforce") && !strings.Contains(c.Hint, "不调 setenforce") {
			t.Errorf("建议不得引导用户关闭 SELinux，实际 %q", c.Hint)
		}
	})

	t.Run("v1 与 hybrid 形态可渲染", func(t *testing.T) {
		for _, m := range []CgroupMode{CgroupV1, CgroupHybrid} {
			env := androidEnvForTest()
			env.CgroupMode = m
			c := androidEnvCheck(env)
			if c.Status != StatusOK {
				t.Fatalf("cgroup %s 应记 OK，实际 %q", m, c.Status)
			}
			if !strings.Contains(c.Detail, "cgroup v1") {
				t.Errorf("详情应渲染 cgroup v1 形态，实际 %q", c.Detail)
			}
		}
	})
}

// TestAndroidEnvCheckWarningsAppearInDetail 覆盖需求 1 的 Warnings 列表：
// 探测得到的降级提示必须出现在详情里，不能只留在结构体里。
func TestAndroidEnvCheckWarningsAppearInDetail(t *testing.T) {
	env := androidEnvForTest()
	env.SELinux = SELinuxEnforcing
	env.CgroupMode = CgroupNone
	env.UserNS = false
	env.Warnings = nil
	deriveAndroidWarnings(env)
	if len(env.Warnings) == 0 {
		t.Fatal("用例前提不成立：应产生若干降级提示")
	}
	c := androidEnvCheck(env)
	for _, w := range env.Warnings {
		if !strings.Contains(c.Detail, w) {
			t.Errorf("详情里缺少警告 %q：\n%s", w, c.Detail)
		}
	}
}

// TestAndroidEnvCheckDetailIsIndented 锁定多行详情的排版契约：续行必须自带
// 与 render.go 相同的四空格缩进，否则 text 输出会错行。
func TestAndroidEnvCheckDetailIsIndented(t *testing.T) {
	c := androidEnvCheck(androidEnvForTest())
	lines := strings.Split(c.Detail, "\n")
	if len(lines) < 2 {
		t.Fatalf("详情应为多行，实际 %q", c.Detail)
	}
	for i, ln := range lines[1:] {
		if !strings.HasPrefix(ln, "    ") {
			t.Errorf("第 %d 续行缺少四空格缩进: %q", i+2, ln)
		}
	}
	if strings.Contains(c.Detail, "\n\n") || strings.HasSuffix(c.Detail, "\n") {
		t.Errorf("详情不应有空行或尾随换行: %q", c.Detail)
	}
}

// TestAndroidEnvCheckWiredIntoLinuxChecks 覆盖需求 3 的接线：android.env 必须
// 真的出现在 linux 检查器输出里，且排在最后（保证既有顺序不变）。
func TestAndroidEnvCheckWiredIntoLinuxChecks(t *testing.T) {
	withFakeAndroidEnv(t, androidEnvForTest(), nil)
	checks := linuxCheckList("v0.0.0-test")

	var found int
	for i, c := range checks {
		if c.ID != CheckAndroidEnv {
			continue
		}
		found++
		if i != len(checks)-1 {
			t.Errorf("android.env 应排在最后，实际第 %d/%d 项", i+1, len(checks))
		}
		if c.Status != StatusOK {
			t.Errorf("注入健康 Android 后应为 OK，实际 %q", c.Status)
		}
	}
	if found != 1 {
		t.Fatalf("android.env 应恰好出现一次，实际 %d 次", found)
	}

	// 既有检查项顺序不受影响：除新增的 android.env 外，列表应与改动前逐项一致。
	// 注意 rootfs.test 由 Diagnose 追加（runSmoke），不在本列表里，因此这里
	// 只比对 linuxCheckList 自己负责的项。
	wantExisting := []string{
		CheckKernelVersion, CheckKernelNamespaces, CheckCgroupsMount,
		CheckCgroupsControllers, CheckSystemdAvailable, CheckStorageDataDir,
		CheckStorageLayers, CheckBinaryVersion, CheckArchHost,
	}
	if len(checks) != len(wantExisting)+1 {
		t.Fatalf("检查项数量应为 %d，实际 %d", len(wantExisting)+1, len(checks))
	}
	for i, id := range wantExisting {
		if checks[i].ID != id {
			t.Errorf("第 %d 项应为 %s，实际 %s", i+1, id, checks[i].ID)
		}
	}
}

// TestAndroidEnvCheckRegistersSkipAndTitle 锁定对外契约：ID 必须登记在
// --skip 白名单与标题表里，否则 CLI 会拒绝 --skip android.env。
func TestAndroidEnvCheckRegistersSkipAndTitle(t *testing.T) {
	var inAll bool
	for _, id := range AllCheckIDs() {
		if id == CheckAndroidEnv {
			inAll = true
		}
	}
	if !inAll {
		t.Fatalf("android.env 未登记进 AllCheckIDs（--skip 会拒绝该 ID）")
	}
	if got := titleOf(CheckAndroidEnv); got == CheckAndroidEnv || got == "" {
		t.Fatalf("android.env 缺少中文标题，实际 %q", got)
	}
}

// TestAndroidEnvCheckProbeErrorIsVisible 覆盖探测失败路径：探测函数返回 error
// 时，doctor 不得整体失败，也不得静默——必须把原因显示出来。
func TestAndroidEnvCheckProbeErrorIsVisible(t *testing.T) {
	withFakeAndroidEnv(t, nil, errors.New("探测爆炸"))
	c := androidEnvCheck(detectAndroidEnvResult())
	if !strings.Contains(c.Detail, "探测失败") {
		t.Fatalf("详情应显示探测失败原因，实际 %q", c.Detail)
	}
	if !strings.Contains(c.Detail, "探测爆炸") {
		t.Fatalf("详情应带上原始错误，实际 %q", c.Detail)
	}
	// 探测失败时无任何可信能力：必需 ns 全缺 → Fail，但绝不能 panic。
	if c.Status != StatusFail {
		t.Fatalf("探测失败且无任何能力时应记 %q，实际 %q", StatusFail, c.Status)
	}
}

// TestAndroidEnvCheckSurvivesDiagnose 覆盖端到端：Diagnose 在注入 Android 环境
// 后仍能正常汇总，且 android.env 参与计数（集成层不吞掉这一项）。
func TestAndroidEnvCheckSurvivesDiagnose(t *testing.T) {
	withFakeAndroidEnv(t, androidEnvForTest(), nil)
	rep, err := Diagnose(t.Context(), &Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Diagnose 失败: %v", err)
	}
	var got *Check
	for i := range rep.Checks {
		if rep.Checks[i].ID == CheckAndroidEnv {
			got = &rep.Checks[i]
		}
	}
	if got == nil {
		t.Fatalf("报告里缺少 android.env 检查项")
	}
	if got.Status != StatusOK {
		t.Fatalf("注入健康 Android 后应为 OK，实际 %q（detail=%q）", got.Status, got.Detail)
	}
}

// TestAndroidEnvCheckSkipIsHonoured 锁定 --skip 行为：跳过 android.env 后它
// 必须从报告里消失（诊断本身仍成功）。
func TestAndroidEnvCheckSkipIsHonoured(t *testing.T) {
	withFakeAndroidEnv(t, androidEnvForTest(), nil)
	rep, err := Diagnose(t.Context(), &Options{DataDir: t.TempDir(), Skip: []string{CheckAndroidEnv}})
	if err != nil {
		t.Fatalf("Diagnose 失败: %v", err)
	}
	for _, c := range rep.Checks {
		if c.ID == CheckAndroidEnv {
			t.Fatalf("--skip android.env 后该项仍出现在报告里")
		}
	}
}
