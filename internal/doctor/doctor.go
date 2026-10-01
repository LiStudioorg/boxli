// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package doctor 实现 `boxli doctor` 的环境自检：内核版本、namespace 与
// user namespace 可用性、cgroups 挂载与控制器、systemd 可用性、数据目录与
// 层缓存、二进制版本、宿主架构，以及可选的容器真实冒烟测试。
//
// 设计约定（与 AGENTS.md 一致）：
//
//   - 全部检查都是**只读**且无副作用：只 stat/读取 /proc、/sys、数据目录，
//     最多执行 `systemctl --version` 这类纯查询命令，绝不创建、删除或修改
//     任何文件，也不写 cgroup。
//   - 缺少文件、权限不足、命令不存在等一律降级为 StatusSkip / StatusWarn，
//     并在 Detail 说明原因、在 Hint 给出可操作的修复建议；它们**不会**让
//     Diagnose 返回错误。
//   - 平台差异用 build tag 分文件（checks_linux.go / checks_other.go），
//     公共代码里不出现 runtime.GOOS 分支。
package doctor

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Status 是一个检查项的结果等级。
type Status string

// 结果等级取值。StatusFail 是唯一会让 `boxli doctor` 退出码非 0 的等级。
const (
	// StatusOK 表示该项检查通过。
	StatusOK Status = "ok"
	// StatusWarn 表示环境可用但存在隐患，不影响正常使用。
	StatusWarn Status = "warn"
	// StatusFail 表示该环境无法运行 Boxli。
	StatusFail Status = "fail"
	// StatusSkip 表示本环境无法检查或用户主动跳过，不代表失败。
	StatusSkip Status = "skip"
)

// 稳定的检查 ID。这些字符串是 doctor 的对外契约：CLI 的 --skip 参数、
// text/JSON 输出与 E2E 断言都依赖它们，不得随意改名。
const (
	// CheckKernelVersion 是内核版本检查。
	CheckKernelVersion = "kernel.version"
	// CheckKernelNamespaces 是 namespace 与 user namespace 可用性检查。
	CheckKernelNamespaces = "kernel.namespaces"
	// CheckCgroupsMount 是 cgroup 挂载形态检查。
	CheckCgroupsMount = "cgroups.mount"
	// CheckCgroupsControllers 是 cgroup 控制器可用性检查。
	CheckCgroupsControllers = "cgroups.controllers"
	// CheckSystemdAvailable 是 systemd 可用性检查。
	CheckSystemdAvailable = "systemd.available"
	// CheckStorageDataDir 是数据目录可用性检查。
	CheckStorageDataDir = "storage.data-dir"
	// CheckStorageLayers 是层缓存检查。
	CheckStorageLayers = "storage.layers"
	// CheckBinaryVersion 是二进制版本与路径检查。
	CheckBinaryVersion = "binary.version"
	// CheckRootfsTest 是可选的真实容器冒烟测试。
	CheckRootfsTest = "rootfs.test"
	// CheckArchHost 是宿主架构与平台后端检查。
	CheckArchHost = "arch.host"
)

// AllCheckIDs 按 Diagnose 的执行顺序返回全部检查 ID，供 CLI 校验 --skip 参数。
func AllCheckIDs() []string {
	out := make([]string, 0, len(checkTitles))
	for _, id := range checkOrder {
		out = append(out, id)
	}
	return out
}

// checkOrder 是检查项的稳定执行顺序，跨平台一致。
var checkOrder = []string{
	CheckKernelVersion,
	CheckKernelNamespaces,
	CheckCgroupsMount,
	CheckCgroupsControllers,
	CheckSystemdAvailable,
	CheckStorageDataDir,
	CheckStorageLayers,
	CheckBinaryVersion,
	CheckRootfsTest,
	CheckArchHost,
}

// checkTitles 是各检查 ID 的中文标题，跨平台一致。
var checkTitles = map[string]string{
	CheckKernelVersion:      "内核版本",
	CheckKernelNamespaces:   "namespace 支持",
	CheckCgroupsMount:       "cgroups 挂载",
	CheckCgroupsControllers: "cgroup 控制器",
	CheckSystemdAvailable:   "systemd 可用性",
	CheckStorageDataDir:     "数据目录",
	CheckStorageLayers:      "层缓存",
	CheckBinaryVersion:      "二进制",
	CheckRootfsTest:         "容器冒烟测试",
	CheckArchHost:           "宿主架构",
}

// titleOf 返回检查 ID 对应的中文标题，未知 ID 原样返回。
func titleOf(id string) string {
	if t, ok := checkTitles[id]; ok {
		return t
	}
	return id
}

// Check 是单个环境检查项的结果。
type Check struct {
	// ID 是稳定检查 ID，如 "kernel.version"。
	ID string `json:"id"`
	// Title 是给人看的短标题。
	Title string `json:"title"`
	// Status 是结果等级。
	Status Status `json:"status"`
	// Detail 是本环境的实际观测值。
	Detail string `json:"detail,omitempty"`
	// Hint 是修复建议（具体命令或做法），仅在需要时非空。
	Hint string `json:"hint,omitempty"`
}

// Report 是一次完整自检的结果。
type Report struct {
	// Checks 按执行顺序保存全部未跳过的检查项。
	Checks []Check `json:"checks"`
	// Kernel 是宿主内核版本（uname release），非 Linux 平台可能为空。
	Kernel string `json:"kernel,omitempty"`
	// Platform 是 GOOS/GOARCH，如 "linux/amd64"。
	Platform string `json:"platform"`
	// DataDir 是被检查的数据目录绝对路径。
	DataDir string `json:"dataDir"`
}

// NewReport 返回一个只填好宿主信息的空报告。
func NewReport(dataDir string) *Report {
	return &Report{
		Platform: hostPlatform(),
		DataDir:  dataDir,
		Kernel:   hostKernel(),
	}
}

// Counts 统计各等级的检查项数量。
func (r *Report) Counts() (ok, warn, fail, skip int) {
	if r == nil {
		return 0, 0, 0, 0
	}
	for _, c := range r.Checks {
		switch c.Status {
		case StatusOK:
			ok++
		case StatusWarn:
			warn++
		case StatusFail:
			fail++
		case StatusSkip:
			skip++
		}
	}
	return ok, warn, fail, skip
}

// HasFailure 报告是否存在 StatusFail 级别的检查项。
// 警告与跳过都不算失败：环境可用但存在隐患时 doctor 仍然成功。
func (r *Report) HasFailure() bool {
	_, _, fail, _ := r.Counts()
	return fail > 0
}

// ExitCode 返回进程退出码：仅有警告或跳过时为 0，存在失败时为 1。
func (r *Report) ExitCode() int {
	if r.HasFailure() {
		return 1
	}
	return 0
}

// Checker 提供一组检查项。CLI 与测试可以注入自己的实现；
// 默认实现由平台 build tag 提供（defaultChecker）。
type Checker interface {
	// Name 是检查器名字，用于日志，如 "linux" / "stub"。
	Name() string
	// Checks 返回本次要执行的检查项，顺序即输出顺序。
	Checks() []Check
}

// versionedChecker 是可选接口：实现了它的检查器会在执行前收到
// Options.Version，从而把版本号并入 binary.version。
type versionedChecker interface {
	Checker
	// WithVersion 返回注入了版本号的检查器副本。
	WithVersion(version string) Checker
}

// smokeProvider 是可选接口：平台检查器据此提供真实的容器冒烟测试实现。
// 返回 nil 表示该平台不支持冒烟测试（将记为跳过）。
type smokeProvider interface {
	// Smoke 返回冒烟测试实现。
	Smoke() SmokeFunc
}

// SmokeFunc 是可选的真实容器冒烟测试。返回空字符串表示真实测试通过；
// 返回非空字符串表示无法真实测试（将记为 StatusSkip，并带上 --test-run 提示）。
type SmokeFunc func(ctx context.Context) (reason string)

// Options 配置一次自检。
type Options struct {
	// DataDir 是数据目录（~/.boxli 或 --data-dir），为空时按 $BOXLI_HOME、
	// ~/.boxli 的顺序推断。
	DataDir string
	// Version 是 boxli 版本字符串（由 main 经 -ldflags 注入）。
	Version string
	// Skip 是要跳过的检查 ID 列表，跳过的项不会出现在报告中。
	Skip []string
	// Checker 非空时替换平台默认检查器（测试注入 fake 用）。
	Checker Checker
	// TestRun 为 true 时启用真实容器冒烟测试（CLI 的 --test-run）。
	TestRun bool
	// Smoke 是冒烟测试实现，nil 时使用平台默认实现。
	Smoke SmokeFunc
}

// Diagnose 依次执行检查项并汇总成 Report。
//
// 除 ctx 已取消外，本函数不会因为某个检查失败而返回错误：环境问题一律体现
// 为 Check 的等级。ctx 只用于取消冒烟测试等外呼动作。
func Diagnose(ctx context.Context, opts *Options) (*Report, error) {
	if opts == nil {
		opts = &Options{}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("doctor: 诊断已取消: %w", err)
	}

	rep := NewReport(resolveDataDir(opts.DataDir))

	checker := opts.Checker
	if checker == nil {
		checker = defaultChecker()
	}
	if vc, ok := checker.(versionedChecker); ok {
		checker = vc.WithVersion(opts.Version)
	}
	smoke := opts.Smoke
	if smoke == nil {
		if sp, ok := checker.(smokeProvider); ok {
			smoke = sp.Smoke()
		}
	}
	slog.Debug("doctor: 开始环境自检", slog.String("checker", checker.Name()), slog.String("platform", rep.Platform))

	skip := make(map[string]bool, len(opts.Skip))
	for _, id := range opts.Skip {
		skip[strings.TrimSpace(id)] = true
	}

	for _, c := range checker.Checks() {
		if skip[c.ID] {
			slog.Debug("doctor: 跳过检查", slog.String("check", c.ID))
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("doctor: 诊断已取消: %w", err)
		}
		rep.Checks = append(rep.Checks, normalize(c))
	}

	rep.Checks = append(rep.Checks, runSmoke(ctx, opts, smoke))

	ok, warn, fail, skipped := rep.Counts()
	slog.Debug("doctor: 自检完成",
		slog.Int("ok", ok), slog.Int("warn", warn), slog.Int("fail", fail), slog.Int("skip", skipped))
	return rep, nil
}

// runSmoke 执行可选的容器冒烟测试。
//
// 默认（未开启 --test-run）不启动任何容器，直接记为 StatusSkip 并给出
// 开启方式；开启后仍会在受限沙箱里降级：本项目的 CI/开发沙箱禁止
// CLONE_NEWNS / CLONE_NEWPID / CLONE_NEWUTS，真实容器永远起不来，因此这里
// 只把"无法真实测试"作为跳过说明，绝不记为失败。
func runSmoke(ctx context.Context, opts *Options, smoke SmokeFunc) Check {
	c := Check{ID: CheckRootfsTest, Title: titleOf(CheckRootfsTest)}

	if !opts.TestRun {
		c.Status = StatusSkip
		c.Detail = "沙箱不支持 namespace（由 boxli doctor --test-run 触发）"
		c.Hint = "在支持 namespace 的 Linux 主机上运行 `boxli doctor --test-run` 做真实容器验证"
		return c
	}

	if smoke == nil {
		c.Status = StatusSkip
		c.Detail = "本平台暂不支持运行容器冒烟测试"
		return c
	}

	reason, err := smoke(ctx)
	switch {
	case err != nil:
		c.Status = StatusSkip
		c.Detail = fmt.Sprintf("冒烟测试无法执行: %v", err)
		c.Hint = "确认以 root 运行或开启 user namespace（sysctl kernel.unprivileged_userns_clone=1）后重试"
	case reason != "":
		c.Status = StatusSkip
		c.Detail = reason
		c.Hint = "在支持 namespace 的内核与沙箱中重试：`boxli run --rm <image> /bin/true`"
	default:
		c.Status = StatusOK
		c.Detail = "真实容器冒烟测试通过"
	}
	return c
}

// normalize 补齐检查项的必填字段，避免出现空 ID 或未知等级。
func normalize(c Check) Check {
	if c.ID == "" {
		c.ID = "unknown"
	}
	if c.Status == "" {
		c.Status = StatusSkip
	}
	return c
}

// resolveDataDir 解析数据目录：显式参数 > $BOXLI_HOME > ~/.boxli。
func resolveDataDir(dir string) string {
	if dir == "" {
		dir = os.Getenv("BOXLI_HOME")
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home + string(os.PathSeparator) + ".boxli"
		}
	}
	if abs, err := absPath(dir); err == nil {
		return abs
	}
	return dir
}
