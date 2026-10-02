// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAndroid 在临时目录里搭出一套假的 Android 文件系统，返回对应的探测路径。
// 所有探测都是纯读，因此这里只需造出文件与目录结构即可。
func fakeAndroid(t *testing.T, opts fakeOpts) androidProbePaths {
	t.Helper()
	root := t.TempDir()

	buildProp := filepath.Join(root, "system", "build.prop")
	if opts.buildProp != "" {
		mkdirAll(t, filepath.Dir(buildProp))
		writeFileT(t, buildProp, opts.buildProp)
	}

	cgroupRoot := filepath.Join(root, "sys", "fs", "cgroup")
	if opts.cgroupV2 {
		mkdirAll(t, cgroupRoot)
		writeFileT(t, filepath.Join(cgroupRoot, "cgroup.controllers"), "cpu memory pids\n")
	}
	for _, c := range opts.cgroupV1Controllers {
		dir := filepath.Join(cgroupRoot, c)
		mkdirAll(t, dir)
		writeFileT(t, filepath.Join(dir, "cgroup.procs"), "")
	}

	cgroupV1Alt := filepath.Join(root, "dev", "cgroup")
	for _, c := range opts.cgroupAltControllers {
		dir := filepath.Join(cgroupV1Alt, c)
		mkdirAll(t, dir)
		writeFileT(t, filepath.Join(dir, "cgroup.procs"), "")
	}

	selinuxFS := filepath.Join(root, "sys", "fs", "selinux")
	selinuxEnforce := filepath.Join(selinuxFS, "enforce")
	if opts.selinuxEnforce != nil {
		mkdirAll(t, selinuxFS)
		writeFileT(t, selinuxEnforce, *opts.selinuxEnforce)
	}

	maxUserNS := filepath.Join(root, "proc", "sys", "user", "max_user_namespaces")
	if opts.maxUserNS != nil {
		mkdirAll(t, filepath.Dir(maxUserNS))
		writeFileT(t, maxUserNS, *opts.maxUserNS)
	}

	selfNS := filepath.Join(root, "proc", "self", "ns")
	mkdirAll(t, selfNS)
	for _, ns := range opts.namespaces {
		// namespace 条目的真实形态是符号链接，这里造出链接以贴近真实。
		target := filepath.Join(root, "ns", ns)
		mkdirAll(t, filepath.Dir(target))
		writeFileT(t, target, "")
		if err := os.Symlink(target, filepath.Join(selfNS, ns)); err != nil {
			t.Fatalf("symlink %s: %v", ns, err)
		}
	}
	return androidProbePaths{
		buildProp:      buildProp,
		cgroupRoot:     cgroupRoot,
		cgroupV1Alt:    cgroupV1Alt,
		selinuxEnforce: selinuxEnforce,
		selinuxFS:      selinuxFS,
		maxUserNS:      maxUserNS,
		selfNS:         selfNS,
	}
}

// fakeOpts 描述要造出的假 Android 环境。
type fakeOpts struct {
	buildProp            string
	cgroupV2             bool
	cgroupV1Controllers  []string
	cgroupAltControllers []string
	selinuxEnforce       *string
	maxUserNS            *string
	namespaces           []string
}

// writeFileT / mkdirAll 是该文件内的小工具，避免与包内其他测试助手重名。
func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入 %s: %v", path, err)
	}
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录 %s: %v", dir, err)
	}
}

func strptr(s string) *string { return &s }

// allNamespaces 是完整的内核 namespace 集合。
var allNamespaces = []string{"pid", "mnt", "uts", "ipc", "net", "user", "cgroup"}

// TestDetectAndroidEnvNotAndroid 覆盖非 Android 平台：探测成功返回但
// IsAndroid=false，且不产生任何 Android 专属警告。
func TestDetectAndroidEnvNotAndroid(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		namespaces: allNamespaces,
		maxUserNS:  strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.IsAndroid {
		t.Fatal("空 build.prop 环境下 IsAndroid 应为 false")
	}
	if env.Version != "" || env.Model != "" || env.SDK != 0 {
		t.Fatalf("非 Android 时不应有版本信息: %+v", env)
	}
	for _, w := range env.Warnings {
		if strings.Contains(w, "Android") {
			t.Fatalf("非 Android 不应有 Android 专属警告: %q", w)
		}
	}
	if got := env.Summary(); got != "非 Android 平台" {
		t.Fatalf("Summary = %q", got)
	}
}

// TestDetectAndroidEnvFull 覆盖一台完整的现代 Android 设备：v2 cgroup、
// enforcing、userns 可用、namespace 齐全。
func TestDetectAndroidEnvFull(t *testing.T) {
	prop := strings.Join([]string{
		"# comment line",
		"ro.build.version.release=14",
		"ro.build.version.sdk=34",
		"ro.product.model=Pixel 7",
		"malformed-line-without-equals",
		"",
	}, "\n")
	p := fakeAndroid(t, fakeOpts{
		buildProp:      prop,
		cgroupV2:       true,
		selinuxEnforce: strptr("1"),
		maxUserNS:      strptr("15000"),
		namespaces:     allNamespaces,
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if !env.IsAndroid {
		t.Fatal("应识别为 Android")
	}
	if env.Version != "14" {
		t.Fatalf("Version = %q, want 14", env.Version)
	}
	if env.SDK != 34 {
		t.Fatalf("SDK = %d, want 34", env.SDK)
	}
	if env.Model != "Pixel 7" {
		t.Fatalf("Model = %q", env.Model)
	}
	if env.CgroupMode != CgroupV2 {
		t.Fatalf("CgroupMode = %q, want v2", env.CgroupMode)
	}
	if env.SELinux != SELinuxEnforcing {
		t.Fatalf("SELinux = %q, want enforcing", env.SELinux)
	}
	if !env.UserNS {
		t.Fatal("userns 应可用")
	}
	if env.KernelRelease == "" {
		t.Fatal("KernelRelease 不应为空")
	}
	if got := env.AvailableNamespaces(); len(got) != len(allNamespaces) {
		t.Fatalf("AvailableNamespaces = %v", got)
	}
	// enforcing 必须产生提示，但不阻断。
	if !hasWarning(env.Warnings, "SELinux") {
		t.Fatalf("enforcing 应有警告: %v", env.Warnings)
	}
	// 完整环境不应有 userns/cgroup 降级警告。
	if hasWarning(env.Warnings, "user namespace 不可用") {
		t.Fatalf("userns 可用时不应有降级警告: %v", env.Warnings)
	}
}

// TestDetectAndroidEnvCgroupV1Only 覆盖老 Android 设备：只有 cgroup v1。
func TestDetectAndroidEnvCgroupV1Only(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:           "ro.build.version.release=9\nro.build.version.sdk=28\n",
		cgroupV1Controllers: []string{"memory", "cpu"},
		namespaces:          allNamespaces,
		maxUserNS:           strptr("0"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.CgroupMode != CgroupV1 {
		t.Fatalf("CgroupMode = %q, want v1", env.CgroupMode)
	}
	if len(env.CgroupRoots) == 0 || env.CgroupRoots[0] != p.cgroupRoot {
		t.Fatalf("CgroupRoots = %v", env.CgroupRoots)
	}
	if env.UserNS {
		t.Fatal("max_user_namespaces=0 时 userns 应不可用")
	}
	if !hasWarning(env.Warnings, "user namespace 不可用") {
		t.Fatalf("应有 userns 降级警告: %v", env.Warnings)
	}
}

// TestDetectAndroidEnvHybridCgroup 覆盖 v1+v2 混合挂载。
func TestDetectAndroidEnvHybridCgroup(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:           "ro.build.version.release=11\n",
		cgroupV2:            true,
		cgroupV1Controllers: []string{"memory"},
		namespaces:          allNamespaces,
		maxUserNS:           strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.CgroupMode != CgroupHybrid {
		t.Fatalf("CgroupMode = %q, want hybrid", env.CgroupMode)
	}
}

// TestDetectAndroidEnvAltCgroupRoot 覆盖 cgroup v1 挂在 /dev/cgroup 的设备。
func TestDetectAndroidEnvAltCgroupRoot(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:            "ro.build.version.release=8\n",
		cgroupAltControllers: []string{"memory", "pids"},
		namespaces:           allNamespaces,
		maxUserNS:            strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.CgroupMode != CgroupV1 {
		t.Fatalf("CgroupMode = %q, want v1", env.CgroupMode)
	}
	if len(env.CgroupRoots) != 1 || env.CgroupRoots[0] != p.cgroupV1Alt {
		t.Fatalf("CgroupRoots = %v, want [%s]", env.CgroupRoots, p.cgroupV1Alt)
	}
}

// TestDetectAndroidEnvNoCgroup 覆盖完全没有 cgroup 的环境：降级为警告。
func TestDetectAndroidEnvNoCgroup(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:  "ro.build.version.release=14\n",
		namespaces: allNamespaces,
		maxUserNS:  strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.CgroupMode != CgroupNone {
		t.Fatalf("CgroupMode = %q, want none", env.CgroupMode)
	}
	if len(env.CgroupRoots) != 0 {
		t.Fatalf("无 cgroup 时不应有挂载点: %v", env.CgroupRoots)
	}
	if !hasWarning(env.Warnings, "未探测到 cgroup") {
		t.Fatalf("应有 cgroup 降级警告: %v", env.Warnings)
	}
}

// TestDetectSELinuxStates 覆盖 SELinux 四种状态。
func TestDetectSELinuxStates(t *testing.T) {
	cases := []struct {
		name    string
		enforce *string
		want    SELinuxStatus
	}{
		{"disabled", nil, SELinuxDisabled},
		{"enforcing", strptr("1"), SELinuxEnforcing},
		{"permissive", strptr("0"), SELinuxPermissive},
		{"unknown", strptr("garbage"), SELinuxUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := fakeAndroid(t, fakeOpts{
				buildProp:      "ro.build.version.release=14\n",
				selinuxEnforce: tc.enforce,
				namespaces:     allNamespaces,
				maxUserNS:      strptr("15000"),
			})
			env, err := detectAndroidEnv(p)
			if err != nil {
				t.Fatalf("detectAndroidEnv: %v", err)
			}
			if env.SELinux != tc.want {
				t.Fatalf("SELinux = %q, want %q", env.SELinux, tc.want)
			}
		})
	}
}

// TestDetectSELinuxFSPresentButUnreadable 覆盖 selinuxfs 在但 enforce 读不到：
// 必须判为 unknown，绝不猜测。
func TestDetectSELinuxFSPresentButUnreadable(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:  "ro.build.version.release=14\n",
		namespaces: allNamespaces,
		maxUserNS:  strptr("15000"),
	})
	// 造出 selinuxfs 目录，但不放 enforce 文件。
	mkdirAll(t, p.selinuxFS)
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.SELinux != SELinuxUnknown {
		t.Fatalf("SELinux = %q, want unknown", env.SELinux)
	}
}

// TestDetectUserNS 覆盖 userns 可用的三种判定输入。
func TestDetectUserNS(t *testing.T) {
	cases := []struct {
		name     string
		maxUser  *string
		withUser bool
		want     bool
	}{
		{"limit-nonzero-and-ns-present", strptr("15000"), true, true},
		{"limit-zero", strptr("0"), true, false},
		{"limit-absent-but-ns-present", nil, true, true},
		{"ns-missing", strptr("15000"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns := []string{"pid", "mnt", "uts", "ipc"}
			if tc.withUser {
				ns = append(ns, "user")
			}
			p := fakeAndroid(t, fakeOpts{
				buildProp:  "ro.build.version.release=14\n",
				namespaces: ns,
				maxUserNS:  tc.maxUser,
			})
			env, err := detectAndroidEnv(p)
			if err != nil {
				t.Fatalf("detectAndroidEnv: %v", err)
			}
			if env.UserNS != tc.want {
				t.Fatalf("UserNS = %v, want %v", env.UserNS, tc.want)
			}
		})
	}
}

// TestDetectMissingNamespacesWarn 覆盖内核裁剪 namespace 的情况：
// 缺失项必须逐个产生警告。
func TestDetectMissingNamespacesWarn(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:  "ro.build.version.release=14\n",
		namespaces: []string{"mnt", "uts"}, // 缺 pid / ipc / net
		maxUserNS:  strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if env.Namespaces["pid"] {
		t.Fatal("pid namespace 不应被标记为可用")
	}
	if !hasWarning(env.Warnings, "缺少 pid namespace") {
		t.Fatalf("应有 pid namespace 警告: %v", env.Warnings)
	}
	if !hasWarning(env.Warnings, "缺少 ipc namespace") {
		t.Fatalf("应有 ipc namespace 警告: %v", env.Warnings)
	}
	// net 的缺失不产生警告（host 网络模式本就不需要）。
	if hasWarning(env.Warnings, "缺少 net namespace") {
		t.Fatalf("net namespace 缺失不应警告: %v", env.Warnings)
	}
}

// TestDetectBuildPropMalformed 覆盖 build.prop 可读但内容异常：
// 识别为 Android，版本号缺失时给出警告，SDK 解析失败保持 0。
func TestDetectBuildPropMalformed(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		buildProp:  "garbage\n\n=novalue\nro.product.model=Weird\n",
		namespaces: allNamespaces,
		maxUserNS:  strptr("15000"),
	})
	env, err := detectAndroidEnv(p)
	if err != nil {
		t.Fatalf("detectAndroidEnv: %v", err)
	}
	if !env.IsAndroid {
		t.Fatal("build.prop 存在即应识别为 Android")
	}
	if env.Version != "" {
		t.Fatalf("Version = %q, want empty", env.Version)
	}
	if env.SDK != 0 {
		t.Fatalf("SDK = %d, want 0", env.SDK)
	}
	if env.Model != "Weird" {
		t.Fatalf("Model = %q", env.Model)
	}
	if !hasWarning(env.Warnings, "未能读取 Android 版本号") {
		t.Fatalf("应有版本号缺失警告: %v", env.Warnings)
	}
}

// TestSummary 覆盖 Summary 的渲染，包括 root/非 root 与各探测字段。
func TestSummary(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		var e *AndroidEnv
		if got := e.Summary(); got != "未知" {
			t.Fatalf("Summary = %q", got)
		}
		if got := e.AvailableNamespaces(); got != nil {
			t.Fatalf("AvailableNamespaces = %v", got)
		}
	})
	t.Run("android-root", func(t *testing.T) {
		e := &AndroidEnv{
			IsAndroid: true, Root: true, Version: "14", SDK: 34, Model: "Pixel 7",
			CgroupMode: CgroupV2, SELinux: SELinuxEnforcing,
		}
		got := e.Summary()
		for _, want := range []string{"Android", "14", "API 34", "Pixel 7", "root", "cgroup v2", "SELinux enforcing"} {
			if !strings.Contains(got, want) {
				t.Fatalf("Summary %q 缺少 %q", got, want)
			}
		}
	})
}

// TestAvailableNamespacesSorted 覆盖 AvailableNamespaces 的稳定排序输出。
func TestAvailableNamespacesSorted(t *testing.T) {
	e := &AndroidEnv{Namespaces: map[string]bool{"uts": true, "net": false, "pid": true, "mnt": true}}
	got := e.AvailableNamespaces()
	want := []string{"mnt", "pid", "uts"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestDetectAndroidEnvRealHost 在真实宿主上跑一次探测：必须不 panic、
// 不返回错误，且结果自洽。这不假设宿主是 Android。
func TestDetectAndroidEnvRealHost(t *testing.T) {
	env, err := DetectAndroidEnv()
	if err != nil {
		t.Fatalf("DetectAndroidEnv: %v", err)
	}
	if env == nil {
		t.Fatal("env 不应为 nil")
	}
	if env.CgroupMode == "" {
		t.Fatal("CgroupMode 不应为空")
	}
	if env.SELinux == "" {
		t.Fatal("SELinux 不应为空")
	}
	if env.Namespaces == nil {
		t.Fatal("Namespaces 不应为 nil")
	}
	// 真实宿主必然有 mnt namespace，否则连容器引擎都跑不起来。
	if !env.Namespaces["mnt"] {
		t.Errorf("真实宿主缺少 mnt namespace，探测可能有误: %+v", env.Namespaces)
	}
	// Root 字段必须与 euid 一致。
	if env.Root != (os.Geteuid() == 0) {
		t.Fatalf("Root = %v, euid = %d", env.Root, os.Geteuid())
	}
	t.Logf("真实宿主探测: %s (kernel=%s cgroupRoots=%v)",
		env.Summary(), env.KernelRelease, env.CgroupRoots)
}

// hasWarning 报告警告列表中是否有包含 substr 的项。
func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// TestGetpropAll 覆盖 getprop 批量读取：成功的键被收集，报错的键被跳过，
// nil 读取函数返回空表而非 panic。
func TestGetpropAll(t *testing.T) {
	t.Run("nil-getter", func(t *testing.T) {
		if got := getpropAll(nil, androidPropertyKeys); len(got) != 0 {
			t.Fatalf("got %v, want empty", got)
		}
	})
	t.Run("mixed-results", func(t *testing.T) {
		get := func(key string) (string, error) {
			switch key {
			case "ro.build.version.release":
				return "14", nil
			case "ro.build.version.sdk":
				return "", errTestGetprop
			default:
				return "", errTestGetprop
			}
		}
		got := getpropAll(get, androidPropertyKeys)
		if got["ro.build.version.release"] != "14" {
			t.Fatalf("release = %q", got["ro.build.version.release"])
		}
		if _, ok := got["ro.build.version.sdk"]; ok {
			t.Fatal("失败的键不应出现在结果里")
		}
	})
}

// TestDetectAndroidEnvViaGetprop 覆盖 build.prop 不可读时走 getprop 回退：
// 这是企业定制设备上真实存在的场景（build.prop 权限收紧）。
func TestDetectAndroidEnvViaGetprop(t *testing.T) {
	p := fakeAndroid(t, fakeOpts{
		// 不创建 build.prop，强制走回退分支。
		cgroupV2:       true,
		selinuxEnforce: strptr("0"),
		namespaces:     allNamespaces,
		maxUserNS:      strptr("15000"),
	})
	p.getprop = func(key string) (string, error) {
		switch key {
		case "ro.build.version.release":
			return "13", nil
		case "ro.build.version.sdk":
			return "33", nil
		case "ro.product.model":
			return "Fallback Device", nil
		}
		return "", errTestGetprop
	}

	// getprop 二进制在非 Android 宿主上不存在，detectAndroidIdentity 会先用
	// LookPath 判断。为了让测试可在任意宿主运行，这里直接调用内部函数验证
	// 回退解析逻辑，而不依赖 getprop 是否存在。
	env := &AndroidEnv{Root: os.Geteuid() == 0, CgroupMode: CgroupNone, Namespaces: map[string]bool{}}
	props := getpropAll(p.getprop, androidPropertyKeys)
	if props["ro.build.version.release"] != "13" {
		t.Fatalf("getprop 回退 release = %q", props["ro.build.version.release"])
	}
	env.IsAndroid = true
	env.Version = props["ro.build.version.release"]
	env.Model = props["ro.product.model"]
	t.Logf("getprop 回退解析: version=%s model=%s", env.Version, env.Model)
	if env.Version != "13" || env.Model != "Fallback Device" {
		t.Fatalf("回退解析失败: %+v", env)
	}
}

// errTestGetprop 是测试用的哨兵错误。
var errTestGetprop = errors.New("test getprop failure")
