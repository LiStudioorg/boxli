// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package resource

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestV1ApplyBlkioOOMAndQuota 补齐 applyV1 剩余分支：blkio、oom_control、
// cpu quota，全部经 **Apply**（而非 Setup）派发，覆盖 Apply 的 v1 分支。
func TestV1ApplyBlkioOOMAndQuota(t *testing.T) {
	root := fakeV1Root(t, "memory", "cpu", "blkio", "pids", "cpuset")
	withV1Roots(t, root)

	c, err := Setup("b1", &Limits{Memory: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	l := &Limits{
		Memory:            32 << 20,
		MemorySwap:        48 << 20,
		MemoryReservation: 16 << 20,
		OOMKillDisable:    true,
		CPUs:              1.5,
		BlkioWeight:       500,
		PidsLimit:         64,
	}
	if err := Apply(c, l); err != nil {
		t.Fatalf("Apply(v1): %v", err)
	}
	memDir := cgroupV1Path(root, "memory", "b1")
	cpuDir := cgroupV1Path(root, "cpu", "b1")
	checks := map[string]string{
		filepath.Join(memDir, "memory.limit_in_bytes"):                   strconv.Itoa(32 << 20),
		filepath.Join(memDir, "memory.memsw.limit_in_bytes"):             strconv.Itoa(48 << 20), // 32+16
		filepath.Join(memDir, "memory.soft_limit_in_bytes"):              strconv.Itoa(16 << 20),
		filepath.Join(memDir, "memory.oom_control"):                      "1",
		filepath.Join(cpuDir, "cpu.cfs_period_us"):                       "100000",
		filepath.Join(cpuDir, "cpu.cfs_quota_us"):                        "150000",
		filepath.Join(cgroupV1Path(root, "blkio", "b1"), "blkio.weight"): "500",
		filepath.Join(cgroupV1Path(root, "pids", "b1"), "pids.max"):      "64",
	}
	for f, want := range checks {
		if got := readTestFile(t, f); got != want {
			t.Errorf("%s = %q, want %q", filepath.Base(f), got, want)
		}
	}
}

// TestV1MissingControllersAreNotErrors 是 **部分控制器缺失时跳过** 的核心断言：
// 只挂 memory 的设备上，写 cpu/pids/blkio/cpuset 限制必须静默跳过而不是报错，
// 否则 Android 设备上常见的"控制器子集不全"会让容器根本起不来。
func TestV1MissingControllersAreNotErrors(t *testing.T) {
	root := fakeV1Root(t, "memory") // 只有 memory
	withV1Roots(t, root)
	c, err := Setup("partial", &Limits{Memory: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	// 请求了 4 个不存在的控制器 + memory。
	if err := Apply(c, &Limits{
		Memory: 16 << 20, CPUs: 2, CPUShares: 512, CPUSet: "0",
		BlkioWeight: 200, PidsLimit: 10,
	}); err != nil {
		t.Fatalf("缺失控制器不应报错: %v", err)
	}
	// memory 仍写入了。
	if got := readTestFile(t, filepath.Join(cgroupV1Path(root, "memory", "partial"),
		"memory.limit_in_bytes")); got != strconv.Itoa(16<<20) {
		t.Fatalf("memory.limit_in_bytes = %q", got)
	}
	// 其它控制器目录**没有**被创建（跳过而非建半套）。
	for _, ctrl := range []string{"cpu", "pids", "blkio", "cpuset"} {
		if _, err := os.Stat(cgroupV1Path(root, ctrl, "partial")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("缺失的 %s 不应被创建: %v", ctrl, err)
		}
	}
}

// TestV1StatsAndCollectErrorPaths 覆盖 v1 读取侧的失败分支。
func TestV1StatsAndCollectErrorPaths(t *testing.T) {
	root := fakeV1Root(t, "memory", "cpu", "cpuacct", "pids")
	withV1Roots(t, root)

	// 不存在的容器：StatsFor 应报 Running=false（不是错误）。
	st, err := StatsFor("ghost")
	if err != nil || st.Running {
		t.Fatalf("ghost 应 Running=false: %+v %v", st, err)
	}
	// Update 不存在的容器：报 ErrUnsupported。
	if err := Update("ghost", &Limits{Memory: 1 << 20}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Update(ghost) 应 ErrUnsupported, got %v", err)
	}
	// AddPID 不存在的容器：报错。
	if err := AddPID("ghost", 1); err == nil {
		t.Fatal("AddPID(ghost) 应报错")
	}

	// 建组后喂假数据，验证 collectV1 读的正是这些文件。
	if _, err := Setup("live", &Limits{Memory: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	write := func(dir, file, v string) {
		if err := os.WriteFile(filepath.Join(cgroupV1Path(root, dir, "live"), file),
			[]byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("memory", "memory.usage_in_bytes", "4096")
	write("memory", "memory.limit_in_bytes", "8388608")
	// v1 CPU 用量在 **cpuacct** 控制器下（cpuacct.usage 直接就是纳秒），
	// 不是 cpu/cpu.stat —— 这是 v1 与 v2 的另一处形态差异。
	write("cpuacct", "cpuacct.usage", "777")
	write("cpu", "cpu.shares", "1024")
	write("pids", "pids.current", "3")
	write("pids", "pids.max", "max")

	st, err = StatsFor("live")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running {
		t.Error("live 应 Running=true")
	}
	// cpuacct.usage 直接就是纳秒数（v2 的 cpu.usage_usec 才需要 ×1000）。
	if st.MemoryUsage != 4096 || st.CPUUsageNanos != 777 || st.PidsCurrent != 3 {
		t.Errorf("v1 采集异常: %+v", st)
	}
	// pids.max = "max" → 0（readV1Int 的约定）。
	if st.PidsLimit != 0 {
		t.Errorf("pids.max=max 应读成 0, got %d", st.PidsLimit)
	}
	// 非法整数值 → readV1Int 报错但不 panic。
	write("pids", "pids.current", "not-a-number")
	if _, err := readV1Int(filepath.Join(cgroupV1Path(root, "pids", "live"), "pids.current")); err == nil {
		t.Error("非法整数应报错")
	}
	if _, err := readV1Int(filepath.Join(root, "nope")); err == nil {
		t.Error("文件不存在应报错")
	}
}

// TestV1RemovePartialAndIdempotent 覆盖 removeV1：控制器目录不全时也全部清理，
// 且不存在的容器静默返回。
func TestV1RemovePartialAndIdempotent(t *testing.T) {
	root := fakeV1Root(t, "memory", "cpu")
	withV1Roots(t, root)
	if _, err := Setup("rm1", &Limits{}); err != nil {
		t.Fatal(err)
	}
	// 模拟设备只挂了 memory：cpu 组目录不存在。
	if err := os.RemoveAll(cgroupV1Path(root, "cpu", "rm1")); err != nil {
		t.Fatal(err)
	}
	if err := Remove("rm1"); err != nil {
		t.Fatalf("Remove 应容忍缺失控制器: %v", err)
	}
	if _, err := os.Stat(cgroupV1Path(root, "memory", "rm1")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("memory 组未被删除: %v", err)
	}
	if err := Remove("rm1"); err != nil {
		t.Fatalf("重复 Remove 应静默: %v", err)
	}
}

// TestDetectCgroupMountsDedups 覆盖 DetectCgroupMounts 的去重与"多个 v1 根
// 只取第一个可用"两个分支。
func TestDetectCgroupMountsDedups(t *testing.T) {
	v2root := t.TempDir()
	if err := os.WriteFile(filepath.Join(v2root, "cgroup.controllers"),
		[]byte("cpu memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldV2 := cgroupV2GroupRoot
	cgroupV2GroupRoot = v2root
	oldMode := v1ModeOverride
	v1ModeOverride = ""
	oldRoots := cgroupV1Roots
	// 第一个根可用、第二个也可用：结果只应有一个 v1 根。
	a := fakeV1ControllerRoot(t, "memory")
	b := fakeV1ControllerRoot(t, "memory")
	cgroupV1Roots = []string{a, b}
	t.Cleanup(func() {
		cgroupV2GroupRoot, v1ModeOverride, cgroupV1Roots = oldV2, oldMode, oldRoots
	})

	got := DetectCgroupMounts()
	// v2 根 + 第一个 v1 根；若 v1 根恰好等于 v2 根则去重成一个。
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Fatalf("DetectCgroupMounts 有重复项: %v", got)
		}
		seen[p] = true
	}
	if !seen[v2root] {
		t.Errorf("结果应含 v2 根 %s: %v", v2root, got)
	}
	if !seen[a] {
		t.Errorf("结果应含第一个 v1 根 %s: %v", a, got)
	}
	if seen[b] {
		t.Errorf("第二个 v1 根不应出现（多个根无意义）: %v", got)
	}
}

// fakeV1ControllerRoot 造一个带指定控制器的假 v1 根（独立临时目录）。
func fakeV1ControllerRoot(t *testing.T, controllers ...string) string {
	t.Helper()
	return fakeV1Root(t, controllers...)
}
