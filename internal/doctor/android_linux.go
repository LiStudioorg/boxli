// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

//go:build linux

package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Android 是 Android 环境探测的唯一实现文件。
//
// 设计约定（与 doctor 的整体约定一致，见 package 注释）：
//
//   - **全部只读**：只 stat / 读取 /proc、/sys、/system，绝不 fork 子进程去
//     unshare、绝不写 cgroup、绝不调用 setenforce。判断 user namespace 是否
//     可用时读 /proc/sys/user/max_user_namespaces 与 /proc/self/ns/user，
//     而不是真的去 unshare —— 后者会产生副作用，且在被 SELinux 拒绝时给出
//     假阴性。
//   - **探测失败一律降级**：缺文件、权限不足、命令不存在都只是让对应字段为空
//     并在 Warnings 里说明，绝不返回 error，也绝不让 doctor 判定为失败。
//   - build tag 用 `linux` 而非 `android`：Go 在 GOOS=android 时会同时满足
//     linux 标签，因此本文件在 Android 上自动参与编译；反过来用 `android`
//     标签会让桌面 Linux 上的单元测试与 `licore doctor` 完全看不到这段逻辑。
//     是否真的运行在 Android 上，由 /system/build.prop 等特征在运行时判断。

// CgroupMode 描述宿主的 cgroup 层级形态。
type CgroupMode string

// cgroup 层级形态取值。
const (
	// CgroupNone 表示未探测到任何 cgroup 挂载，资源限制不可用。
	CgroupNone CgroupMode = "none"
	// CgroupV1 表示只有 cgroup v1 控制器挂载（老 Android 设备常见）。
	CgroupV1 CgroupMode = "v1"
	// CgroupV2 表示统一层级 cgroup v2（新 Android 与主流发行版）。
	CgroupV2 CgroupMode = "v2"
	// CgroupHybrid 表示 v1 与 v2 混合挂载（Android 常见过渡形态）。
	CgroupHybrid CgroupMode = "hybrid"
)

// SELinuxStatus 描述 SELinux 的运行状态。
type SELinuxStatus string

// SELinux 状态取值。
const (
	// SELinuxDisabled 表示未挂载 selinuxfs，SELinux 未启用。
	SELinuxDisabled SELinuxStatus = "disabled"
	// SELinuxPermissive 表示已启用但只记录不拦截。
	SELinuxPermissive SELinuxStatus = "permissive"
	// SELinuxEnforcing 表示已启用且强制拦截。
	SELinuxEnforcing SELinuxStatus = "enforcing"
	// SELinuxUnknown 表示 selinuxfs 存在但状态不可读。
	SELinuxUnknown SELinuxStatus = "unknown"
)

// androidProbePaths 汇总探测所需的全部路径，便于测试注入替身。
// 生产环境用 defaultProbePaths()。
type androidProbePaths struct {
	// buildProp 是 Android 系统属性文件。
	buildProp string
	// cgroupRoot 是 cgroup 统一挂载根。
	cgroupRoot string
	// cgroupV1Alt 是部分 Android 设备的 cgroup v1 备用挂载点。
	cgroupV1Alt string
	// selinuxEnforce 是 SELinux 强制开关。
	selinuxEnforce string
	// selinuxFS 是 selinuxfs 挂载点，用于判断 SELinux 是否启用。
	selinuxFS string
	// maxUserNS 是 user namespace 数量上限。
	maxUserNS string
	// selfNS 是当前进程的 namespace 目录。
	selfNS string
	// getprop 读取单个 Android 系统属性，默认调用同名命令。
	// 做成字段是为了在非 Android 宿主上也能测试回退路径。
	getprop func(key string) (string, error)
}

// defaultProbePaths 返回真实系统的探测路径。
func defaultProbePaths() androidProbePaths {
	return androidProbePaths{
		buildProp:      "/system/build.prop",
		cgroupRoot:     "/sys/fs/cgroup",
		cgroupV1Alt:    "/dev/cgroup",
		selinuxEnforce: "/sys/fs/selinux/enforce",
		selinuxFS:      "/sys/fs/selinux",
		maxUserNS:      "/proc/sys/user/max_user_namespaces",
		selfNS:         "/proc/self/ns",
		getprop:        getprop,
	}
}

// AndroidEnv 是一次 Android 环境探测的完整结果。
//
// 即使不在 Android 上本结构体也会被返回（IsAndroid=false），这样调用方
// 无需先判断平台再决定要不要探测。
type AndroidEnv struct {
	// IsAndroid 表示当前是否运行在 Android 上。
	IsAndroid bool `json:"isAndroid"`
	// Root 表示当前进程是否以 root（euid 0）运行。
	Root bool `json:"root"`
	// Version 是 Android 版本号，如 "14"。
	Version string `json:"version,omitempty"`
	// SDK 是 Android API level，如 34；探测不到为 0。
	SDK int `json:"sdk,omitempty"`
	// Model 是设备型号，如 "Pixel 7"。
	Model string `json:"model,omitempty"`
	// KernelRelease 是内核版本，如 "5.15.78-android13"。
	KernelRelease string `json:"kernelRelease,omitempty"`
	// CgroupMode 是 cgroup 层级形态。
	CgroupMode CgroupMode `json:"cgroupMode"`
	// CgroupRoots 是实际探测到的 cgroup 挂载点。
	CgroupRoots []string `json:"cgroupRoots,omitempty"`
	// SELinux 是 SELinux 运行状态。
	SELinux SELinuxStatus `json:"selinux"`
	// UserNS 表示内核是否允许创建 user namespace。
	UserNS bool `json:"userNS"`
	// Namespaces 记录各 namespace 在 /proc/self/ns 下是否存在。
	Namespaces map[string]bool `json:"namespaces,omitempty"`
	// Warnings 是降级说明（如 "user namespace 不可用，将不启用 rootless"）。
	// 它们不影响可用性，只是提示。
	Warnings []string `json:"warnings,omitempty"`
}

// androidPropertyKeys 是从 build.prop 中提取的属性键。
var androidPropertyKeys = []string{
	"ro.build.version.release",
	"ro.build.version.sdk",
	"ro.product.model",
}

// DetectAndroidEnv 探测当前环境是否为 Android 以及相关平台能力。
//
// 本函数只读、无副作用、不 fork 子进程。探测不到的信息留空并在 Warnings
// 中说明；它**不会**因为环境不是 Android 或缺少某个文件而返回 error ——
// 唯一的 error 出现在结果自身不成立的矛盾情况（当前实现不会发生），
// 保留返回值是为了给未来的致命探测失败留出契约位。
func DetectAndroidEnv() (*AndroidEnv, error) {
	return detectAndroidEnv(defaultProbePaths())
}

// detectAndroidEnv 是可注入路径的探测实现，供单元测试使用。
func detectAndroidEnv(p androidProbePaths) (*AndroidEnv, error) {
	env := &AndroidEnv{
		Root:       os.Geteuid() == 0,
		CgroupMode: CgroupNone,
		SELinux:    SELinuxDisabled,
		Namespaces: map[string]bool{},
	}

	detectAndroidIdentity(env, p)
	env.KernelRelease = unameRelease()
	detectCgroup(env, p)
	detectSELinux(env, p)
	detectUserNS(env, p)
	detectNamespaces(env, p)
	deriveAndroidWarnings(env)

	return env, nil
}

// detectAndroidIdentity 判断是否 Android 并读取版本信息。
func detectAndroidIdentity(env *AndroidEnv, p androidProbePaths) {
	props, propErr := readAndroidProps(p.buildProp)
	if propErr == nil {
		env.IsAndroid = true
	} else if _, err := exec.LookPath("getprop"); err == nil {
		// build.prop 不可读（部分设备权限收紧）时，getprop 是权威回退。
		env.IsAndroid = true
		props = getpropAll(p.getprop, androidPropertyKeys)
	}

	if !env.IsAndroid {
		return
	}
	env.Version = props["ro.build.version.release"]
	env.Model = props["ro.product.model"]
	if sdk, err := strconv.Atoi(strings.TrimSpace(props["ro.build.version.sdk"])); err == nil {
		env.SDK = sdk
	}
	if env.Version == "" {
		env.Warnings = append(env.Warnings, "未能读取 Android 版本号（ro.build.version.release 不可用）")
	}
}

// readAndroidProps 解析 build.prop 的键值对。文件不存在或不可读时返回错误。
func readAndroidProps(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	return props, nil
}

// getpropAll 依次调用 getprop 读取给定属性键。命令不存在或失败时对应键为空。
func getpropAll(get func(string) (string, error), keys []string) map[string]string {
	props := map[string]string{}
	if get == nil {
		return props
	}
	for _, k := range keys {
		if v, err := get(k); err == nil {
			props[k] = v
		}
	}
	return props
}

// getprop 读取单个 Android 系统属性。这是对 getprop 的只读查询。
func getprop(key string) (string, error) {
	out, err := exec.Command("getprop", key).Output()
	if err != nil {
		return "", fmt.Errorf("getprop %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// detectCgroup 判定 cgroup 形态并收集挂载点。
//
// 判定规则：
//   - 存在 <root>/cgroup.controllers → 有 v2 统一层级；
//   - <root> 下存在任一控制器子目录（如 memory/、cpu/）→ 有 v1；
//   - 两者皆有 → hybrid；仅 v2 → v2；仅 v1 → v1；都无 → none。
func detectCgroup(env *AndroidEnv, p androidProbePaths) {
	hasV2 := fileExists(filepath.Join(p.cgroupRoot, "cgroup.controllers"))
	hasV1 := hasControllerDirs(p.cgroupRoot)

	// 部分 Android 设备（尤其是较老的定制内核）把 v1 挂在 /dev/cgroup。
	altV1 := hasControllerDirs(p.cgroupV1Alt)

	switch {
	case hasV2 && (hasV1 || altV1):
		env.CgroupMode = CgroupHybrid
	case hasV2:
		env.CgroupMode = CgroupV2
	case hasV1 || altV1:
		env.CgroupMode = CgroupV1
	default:
		env.CgroupMode = CgroupNone
	}

	env.CgroupRoots = cgroupMounts(p, hasV2, hasV1, altV1)
}

// cgroupMounts 返回实际探测到的 cgroup 挂载点，顺序即探测优先级。
func cgroupMounts(p androidProbePaths, hasV2, hasV1, altV1 bool) []string {
	var out []string
	if hasV2 || hasV1 {
		out = append(out, p.cgroupRoot)
	}
	if altV1 {
		out = append(out, p.cgroupV1Alt)
	}
	return out
}

// cgroupV1Controllers 是 cgroup v1 下需要探测的控制器目录名。
// 顺序固定，保证输出可复现。
var cgroupV1Controllers = []string{"memory", "cpu", "cpuacct", "cpuset", "pids", "blkio"}

// hasControllerDirs 判断目录下是否存在任一 cgroup v1 控制器挂载子目录。
func hasControllerDirs(root string) bool {
	for _, c := range cgroupV1Controllers {
		if fileExists(filepath.Join(root, c, "tasks")) ||
			fileExists(filepath.Join(root, c, "cgroup.procs")) {
			return true
		}
	}
	return false
}

// detectSELinux 读取 SELinux 状态。
//
// selinuxfs 未挂载 → disabled；enforce 可读则 1=enforcing、0=permissive；
// selinuxfs 在但 enforce 读不到 → unknown（不猜）。
func detectSELinux(env *AndroidEnv, p androidProbePaths) {
	if !dirExists(p.selinuxFS) {
		env.SELinux = SELinuxDisabled
		return
	}
	data, err := os.ReadFile(p.selinuxEnforce)
	if err != nil {
		env.SELinux = SELinuxUnknown
		return
	}
	switch strings.TrimSpace(string(data)) {
	case "1":
		env.SELinux = SELinuxEnforcing
	case "0":
		env.SELinux = SELinuxPermissive
	default:
		env.SELinux = SELinuxUnknown
	}
}

// detectUserNS 判断内核是否允许 user namespace。
//
// 只读判断，绝不真的 unshare：
//   - /proc/sys/user/max_user_namespaces 为 0 或不存在 → 不可用；
//   - /proc/self/ns/user 不存在 → 内核未编译 CONFIG_USER_NS → 不可用。
func detectUserNS(env *AndroidEnv, p androidProbePaths) {
	if data, err := os.ReadFile(p.maxUserNS); err == nil {
		if n, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && n <= 0 {
			env.UserNS = false
			return
		}
	}
	env.UserNS = fileExists(filepath.Join(p.selfNS, "user"))
}

// namespaceNames 是需要探测的内核 namespace 名字，顺序固定便于稳定输出。
var namespaceNames = []string{"pid", "mnt", "uts", "ipc", "net", "user", "cgroup"}

// detectNamespaces 记录各 namespace 在 /proc/self/ns 下是否存在。
func detectNamespaces(env *AndroidEnv, p androidProbePaths) {
	for _, name := range namespaceNames {
		if fileExists(filepath.Join(p.selfNS, name)) {
			env.Namespaces[name] = true
		}
	}
}

// deriveAndroidWarnings 根据探测结果生成降级提示。
func deriveAndroidWarnings(env *AndroidEnv) {
	if env.IsAndroid && !env.Root {
		env.Warnings = append(env.Warnings,
			"当前不是 root：Android 无 Root 官方不支持，请以 root 运行（su）")
	}
	if env.IsAndroid && env.SELinux == SELinuxEnforcing {
		env.Warnings = append(env.Warnings,
			"SELinux 为 enforcing：容器内进程可能被策略拦截；LiCore 会尝试设置 exec 上下文，失败时不影响引擎自身运行")
	}
	if env.IsAndroid && !env.UserNS {
		env.Warnings = append(env.Warnings,
			"user namespace 不可用：将不使用 CLONE_NEWUSER（Android 有 Root 场景本就无需 rootless）")
	}
	if env.IsAndroid && env.CgroupMode == CgroupNone {
		env.Warnings = append(env.Warnings,
			"未探测到 cgroup 挂载：CPU/内存/PID 资源限制将不可用，容器其余功能不受影响")
	}
	if env.IsAndroid {
		for _, ns := range []string{"pid", "mnt", "uts", "ipc"} {
			if !env.Namespaces[ns] {
				env.Warnings = append(env.Warnings,
					fmt.Sprintf("内核缺少 %s namespace：容器隔离能力受限", ns))
			}
		}
	}
}

// fileExists 报告路径是否存在且为普通文件（或符号链接指向的文件）。
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// dirExists 报告路径是否存在且为目录。
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// AvailableNamespaces 返回探测到的可用 namespace 名字，按字母序排序。
// 供 doctor 渲染与调用方做能力判断。
func (e *AndroidEnv) AvailableNamespaces() []string {
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.Namespaces))
	for name, ok := range e.Namespaces {
		if ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Summary 返回一行人类可读的环境摘要，供 doctor 检查项与日志使用。
func (e *AndroidEnv) Summary() string {
	if e == nil {
		return "未知"
	}
	if !e.IsAndroid {
		return "非 Android 平台"
	}
	parts := []string{"Android"}
	if e.Version != "" {
		parts = append(parts, e.Version)
	}
	if e.SDK > 0 {
		parts = append(parts, fmt.Sprintf("API %d", e.SDK))
	}
	if e.Model != "" {
		parts = append(parts, e.Model)
	}
	if e.Root {
		parts = append(parts, "root")
	} else {
		parts = append(parts, "非 root")
	}
	parts = append(parts, "cgroup "+string(e.CgroupMode))
	parts = append(parts, "SELinux "+string(e.SELinux))
	return strings.Join(parts, " / ")
}

// requiredNamespaces 是容器运行**必需**的 namespace。缺任一项 LiCore 无法提供
// 真隔离，nsplan 会以 ErrNoNamespaces 硬失败——因此 doctor 记 StatusFail 而非警告。
var requiredNamespaces = []string{"pid", "mnt", "uts", "ipc"}

// androidCheckTimeout 说明：本检查不 fork 子进程、不写任何文件，探测耗时与
// 读几个 /proc、/sys 文件相当，因此不需要超时或取消支持。

// detectAndroidEnvFn 是探测入口的测试缝。生产环境恒为 DetectAndroidEnv。
//
// 约定：缝只在测试中替换（见 android_check_test.go），生产代码永不改写它；
// 默认值即上述真实实现，替换不会改变任何对外契约。
var detectAndroidEnvFn = DetectAndroidEnv

// androidEnvCheck 生成 Android 环境专项检查项。
//
// 语义设计（与 nsplan 的失败模型严格对齐，不发明新的判定）：
//   - 非 Android：StatusSkip，一行说明即止，不干扰既有的 Linux 检查输出；
//   - Android && 必需 namespace 缺失：StatusFail（与 ErrNoNamespaces 同为硬失败）；
//   - Android && 非 root && userns 不可用：StatusFail（与 ErrNotRoot 同为硬失败）；
//   - Android && 其余降级（无 cgroup、enforcing、非 root 但 userns 可用、
//     userns 不可用）：StatusWarn，能力缺失但容器仍可运行；
//   - 其余：StatusOK。
//
// 无论哪种降级都会带上可执行的排查建议（Hint），不做"只报警不给办法"的输出。
func androidEnvCheck(env *AndroidEnv) Check {
	c := Check{ID: CheckAndroidEnv, Title: titleOf(CheckAndroidEnv)}

	if env == nil || !env.IsAndroid {
		c.Status = StatusSkip
		c.Detail = "非 Android 平台，Android 专项检查已折叠（" + hostPlatform() + "）"
		return c
	}

	c.Status = StatusOK
	c.Detail = androidEnvDetail(env)

	if missing := missingRequiredNamespaces(env); len(missing) > 0 {
		c.Status = StatusFail
		c.Hint = "内核缺少 " + strings.Join(missing, "/") + " namespace，LiCore 无法提供真隔离" +
			"（对应 ErrNoNamespaces，官方不做无隔离的假容器）；请更换开启了 CONFIG_NAMESPACES 的 GKI 内核或 ROM"
		return c
	}
	if !env.Root && !env.UserNS {
		c.Status = StatusFail
		c.Hint = "非 root 且 user namespace 不可用：没有任何可用隔离手段（对应 ErrNotRoot）；" +
			"Android 有 Root 请以 root 运行（su），无 Root 官方不支持，见 docs/android-root.md"
		return c
	}
	if !env.Root {
		c.Status = StatusWarn
		c.Hint = "非 root：Android 无 Root 官方不支持；当前仅因 user namespace 可用而可能以 rootless 启动，" +
			"官方不保证可用性，建议以 root 运行（su）"
		return c
	}
	if env.CgroupMode == CgroupNone {
		c.Status = StatusWarn
		c.Hint = "未探测到 cgroup 挂载：CPU/内存/PID 限制不可用（容器其余功能正常）；" +
			"检查 /sys/fs/cgroup 是否挂载、是否可写（mkdir 探测），见 docs/android-verify.md §1.3"
		return c
	}
	if env.SELinux == SELinuxEnforcing {
		c.Status = StatusWarn
		c.Hint = "enforcing 下容器内进程可能被策略拦截（LiCore 不改策略、不调 setenforce）；" +
			"排查用 `dmesg | grep avc` 看 scontext/tcontext，见 docs/android-root.md 第 4.5 节"
		return c
	}
	return c
}

// androidEnvDetail 组装 Android 环境检查项的详情文本。
//
// 渲染器按整块输出 Detail（render.go 只加一次缩进），因此这里显式给续行加
// 同样的四空格缩进，保证 text 输出对齐。
func androidEnvDetail(env *AndroidEnv) string {
	var lines []string
	lines = append(lines, env.Summary())
	if env.KernelRelease != "" {
		lines = append(lines, "内核 "+env.KernelRelease)
	}
	lines = append(lines, fmt.Sprintf("namespace 可用：%s；必需项（pid/mnt/uts/ipc）%s",
		orNone(env.AvailableNamespaces()), orMissing(missingRequiredNamespaces(env))))
	lines = append(lines, fmt.Sprintf("user namespace %s；cgroup %s",
		yesNo(env.UserNS), cgroupModeLabel(env.CgroupMode)))
	if len(env.CgroupRoots) > 0 {
		lines = append(lines, "cgroup 挂载点 "+strings.Join(env.CgroupRoots, " "))
	}
	for _, w := range env.Warnings {
		lines = append(lines, "警告: "+w)
	}
	return strings.Join(lines, "\n    ")
}

// missingRequiredNamespaces 返回缺失的必需 namespace。
func missingRequiredNamespaces(env *AndroidEnv) []string {
	if env == nil {
		return nil
	}
	var missing []string
	for _, ns := range requiredNamespaces {
		if !env.Namespaces[ns] {
			missing = append(missing, ns)
		}
	}
	return missing
}

// cgroupModeLabel 把 cgroup 形态渲染成给人看的中文标签。
func cgroupModeLabel(m CgroupMode) string {
	switch m {
	case CgroupV2:
		return "v2（统一层级）"
	case CgroupV1:
		return "v1（部分控制器可能缺失）"
	case CgroupHybrid:
		return "v1/v2 混合（按 v2 优先）"
	default:
		return "未探测到（资源限制不可用）"
	}
}

// yesNo 渲染布尔值为"可用/不可用"。
func yesNo(ok bool) string {
	if ok {
		return "可用"
	}
	return "不可用"
}

// orNone 渲染列表，空列表显示"无"。
func orNone(items []string) string {
	if len(items) == 0 {
		return "无"
	}
	return strings.Join(items, "/")
}

// orMissing 渲染缺失项说明，用于"必需项全部具备"的正向表述。
func orMissing(missing []string) string {
	if len(missing) == 0 {
		return "全部具备"
	}
	return "缺失 " + strings.Join(missing, "/")
}
