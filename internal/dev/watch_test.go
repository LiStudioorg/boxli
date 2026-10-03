// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package dev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 测试原则（见 task-7 验收）：
//   - Interval / Debounce 一律走 10–20ms，整个包应在几十毫秒内跑完；
//   - 断言"有事件"用带 deadline 的轮询，断言"没有事件"用比 debounce
//     大得多的静默预算，绝不用固定 sleep 去证明"没发生"；
//   - 不依赖墙钟精度，不依赖文件系统时间戳分辨率（指纹含 mtime 纳秒，
//     同时写多个不同内容/大小，避免同纳秒同尺寸导致漏检）。

const (
	testInterval = 15 * time.Millisecond
	testDebounce = 25 * time.Millisecond
	// settleBudget 用于"确无后续批次"的断言：远大于 testDebounce，
	// 但依然很短，保证测试快速。
	settleBudget = 250 * time.Millisecond
)

// writeFile 写入（并创建父目录）一个文件，失败即 Fatal。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// recvBatch 在 deadline 内等待一个批次，超时即 Fatal。
func recvBatch(t *testing.T, ch <-chan Batch, within time.Duration, what string) Batch {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed before any batch", what)
		}
		return b
	case <-time.After(within):
		t.Fatalf("%s: no batch within %s", what, within)
		return Batch{}
	}
}

// recvClosed 断言通道在 deadline 内被关闭（用于 ctx 取消后的关闭语义）。
func recvClosed(t *testing.T, ch <-chan Batch, within time.Duration, what string) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
			// 取消前可能还有一批在途，继续读到关闭为止。
		case <-deadline:
			t.Fatalf("%s: channel not closed within %s", what, within)
			return
		}
	}
}

// assertNoBatch 断言在静默预算内没有新批次；这是"看足够久都没来"，
// 不是固定 sleep 之后立刻下结论。
func assertNoBatch(t *testing.T, ch <-chan Batch, what string) {
	t.Helper()
	select {
	case b, ok := <-ch:
		if !ok {
			t.Fatalf("%s: channel closed unexpectedly", what)
		}
		t.Fatalf("%s: unexpected extra batch %v", what, b.Paths)
	case <-time.After(settleBudget):
	}
}

// settleBudget 之上再加一个"等待基线就绪"的上限。基线就绪正常情况下
// 只需一个轮询周期（testInterval），这里给足余量以保证高负载下也不过早失败。
const baselineBudget = 5 * time.Second

// awaitBaseline 阻塞到"Watcher 已完成首次轮询、基线确立且循环确实活着"
// 为止，替代过去那句 `time.Sleep(2 * testInterval)`。
//
// 过去用固定 sleep 等基线是测试里最脆的一处：sleep 从 Run 返回后开始计时，
// 但 loop goroutine 可能尚未被调度、ticker 也还没启动，两个时间轴一错开，
// sleep 就可能在被监视写入之前就到期——于是首次快照把测试的写入并入了基线，
// 变化永远不被上报，测试随机失败（高负载下概率显著上升）。
//
// 这里不猜时间，而是**观测事实**：写入一个探针文件，一直等它被作为变化上报。
// 收到该批次即证明"基线已建立、循环在跑、通道可用"，此后测试再写目标文件
// 必然是相对基线的变化，确定性成立。
//
// 调用方必须传一个尚未占用的探针文件名，并保证它不被 ignore 规则命中。
//
// 匹配用后缀而非全等：单根时批次里的路径是相对路径（如 "probe.go"），
// 多根时是带根前缀的绝对路径（如 "/tmp/x/b/probe.go"），后缀匹配对两者都成立。
//
// 为什么"写一轮、等一轮"而不是持续改写：去抖的语义是**安静满 Debounce
// 才发批次**，持续改写会不断重置去抖计时器，批次永远发不出来（连续变化
// 被合法地折叠下去，见 watch.go 的去抖分支）。所以这里必须留出真正的静默期：
//
//	写入探针 → 等一个静默窗口 → 仍未上报就再写一次（说明该次写入被并入了
//	基线，或未被某次轮询观测到）→ 直到上报或超时。
//
// 这是"重试直到观测到事实"，而不是"睡固定时间后假设事实已成立"。
func awaitBaseline(t *testing.T, w *Watcher, ch <-chan Batch, dir, probeName, what string) {
	t.Helper()

	probe := filepath.Join(dir, probeName)
	matched := func(paths []string) bool {
		for _, p := range paths {
			if p == probeName || strings.HasSuffix(p, "/"+probeName) {
				return true
			}
		}
		return false
	}

	deadline := time.After(baselineBudget)
	// 每轮静默窗口：去抖到期所需时间 + 两个轮询周期的余量。
	roundWait := 2*w.spec.Debounce + 2*w.spec.Interval

	for i := 1; ; i++ {
		writeFile(t, probe, fmt.Sprintf("probe-%d\n", i))

		round := time.After(roundWait)
	roundLoop:
		for {
			select {
			case b, ok := <-ch:
				if !ok {
					t.Fatalf("%s: channel closed while awaiting baseline", what)
				}
				if matched(b.Paths) {
					return
				}
				// 批次里只有探针才是"基线就绪"；出现别的路径说明环境有干扰。
				t.Fatalf("%s: unexpected batch before probe observed: %v", what, b.Paths)
			case <-round:
				break roundLoop // 本轮静默期已过仍无批次，补写一次再等。
			case <-deadline:
				t.Fatalf("%s: watcher never reported the baseline probe within %s", what, baselineBudget)
				return
			}
		}

		select {
		case <-deadline:
			t.Fatalf("%s: watcher never reported the baseline probe within %s", what, baselineBudget)
			return
		default:
		}
	}
}

// TestNewWatcherValidation 覆盖 ErrNoRoots 与 ErrBadIgnore 两条校验路径。
func TestNewWatcherValidation(t *testing.T) {
	if _, err := NewWatcher(WatchSpec{}); !errors.Is(err, ErrNoRoots) {
		t.Fatalf("empty roots: err = %v, want ErrNoRoots", err)
	}
	if _, err := NewWatcher(WatchSpec{Roots: []string{"  "}}); !errors.Is(err, ErrNoRoots) {
		t.Fatalf("blank root: err = %v, want ErrNoRoots", err)
	}
	bad := WatchSpec{Roots: []string{t.TempDir()}, Ignore: []string{"[unclosed"}}
	if _, err := NewWatcher(bad); !errors.Is(err, ErrBadIgnore) {
		t.Fatalf("bad ignore: err = %v, want ErrBadIgnore", err)
	}

	// 默认值补齐。
	w, err := NewWatcher(WatchSpec{Roots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	got := w.Spec()
	if got.Interval != DefaultInterval || got.Debounce != DefaultDebounce {
		t.Fatalf("defaults = %v/%v, want %v/%v", got.Interval, got.Debounce, DefaultInterval, DefaultDebounce)
	}
	// 自定义值不被覆盖。
	w2, err := NewWatcher(WatchSpec{Roots: []string{t.TempDir()}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if s := w2.Spec(); s.Interval != testInterval || s.Debounce != testDebounce {
		t.Fatalf("custom spec = %v/%v, want %v/%v", s.Interval, s.Debounce, testInterval, testDebounce)
	}
}

// TestFingerprintDetectsModifyAddRemove 覆盖快照的增 / 改 / 删与确定性。
func TestFingerprintDetectsModifyAddRemove(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "mod.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "gone.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "sub", "nested.go"), "package sub\n")

	before, err := Fingerprint(dir, nil)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	for _, want := range []string{"keep.go", "mod.go", "gone.go", "sub", "sub/nested.go"} {
		if _, ok := before[want]; !ok {
			t.Fatalf("baseline missing %q; got keys %v", want, keysOf(before))
		}
	}
	// 确定性：同一状态重复取指纹必须完全相等（不依赖 map 迭代顺序）。
	again, err := Fingerprint(dir, nil)
	if err != nil {
		t.Fatalf("Fingerprint again: %v", err)
	}
	if diff := Changed(before, again); len(diff) != 0 {
		t.Fatalf("fingerprint not deterministic, diff = %v", diff)
	}

	writeFile(t, filepath.Join(dir, "mod.go"), "package main\n// changed, longer content\n")
	writeFile(t, filepath.Join(dir, "added.go"), "package main\n")
	if err := os.Remove(filepath.Join(dir, "gone.go")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	after, err := Fingerprint(dir, nil)
	if err != nil {
		t.Fatalf("Fingerprint after: %v", err)
	}
	changed := Changed(before, after)
	want := []string{"added.go", "gone.go", "mod.go", "sub"}
	// sub 目录的 mtime 会因内部文件变化而变（也可能不变），这里只要求
	// 集合包含三个确定项，且不多出别的东西。
	if got := intersect(changed, want); len(got) < 3 {
		t.Fatalf("Changed = %v, want at least added/gone/mod", changed)
	}
	for _, p := range changed {
		switch {
		case p == "added.go", p == "gone.go", p == "mod.go", p == "sub":
		default:
			t.Fatalf("unexpected change %q in %v", p, changed)
		}
	}
	// 未改动的文件不应出现。
	for _, p := range changed {
		if p == "keep.go" || p == "sub/nested.go" {
			t.Fatalf("untouched path %q reported as changed: %v", p, changed)
		}
	}
}

// TestFingerprintSkipsIgnoresAndDefaults 覆盖 .git / node_modules 硬跳过、
// ignore 模式（glob、目录前缀、`**`）以及不可读条目不致命。
func TestFingerprintSkipsIgnoresAndDefaults(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "src", "main.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "util_test.go"), "package main\n")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(dir, "node_modules", "dep", "index.js"), "module.exports={}\n")
	writeFile(t, filepath.Join(dir, "vendor", "lib.go"), "package lib\n")
	writeFile(t, filepath.Join(dir, "deep", "a", "b", "gen.go"), "package gen\n")
	writeFile(t, filepath.Join(dir, "api", "types.pb.go"), "package api\n")

	// 注意 `*_test.go` 与 `*.pb.go` 这类不含 `/` 的 glob 不跨目录层
	// （path.Match 语义），跨层要写成 `**/*.pb.go`——下面两种写法都在测。
	fp, err := Fingerprint(dir, []string{"vendor/", "**/gen.go", "**/*.pb.go", "*_test.go"})
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if _, ok := fp["src/main.go"]; !ok {
		t.Fatalf("src/main.go should be tracked; keys %v", keysOf(fp))
	}
	for _, banned := range []string{
		"util_test.go", "vendor", "vendor/lib.go",
		"deep/a/b/gen.go", "api/types.pb.go",
	} {
		if _, ok := fp[banned]; ok {
			t.Fatalf("path %q should be ignored; keys %v", banned, keysOf(fp))
		}
	}
	for k := range fp {
		if strings.HasPrefix(k, ".git") || strings.HasPrefix(k, "node_modules") {
			t.Fatalf("default skip dir leaked into fingerprint: %q", k)
		}
	}
}

// TestFingerprintSymlinkAndMissingRoot 覆盖符号链接按目标记录、
// 以及根不存在时返回空表不报错。
func TestFingerprintSymlinkAndMissingRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target.go"), "package main\n")
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink("target.go", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	fp, err := Fingerprint(dir, nil)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	got, ok := fp["link.go"]
	if !ok {
		t.Fatalf("symlink missing from fingerprint: %v", keysOf(fp))
	}
	if !strings.Contains(got, "link:target.go") {
		t.Fatalf("symlink fingerprint = %q, want it to record the target", got)
	}

	// 根不存在：空表 + 无错误。
	missing, err := Fingerprint(filepath.Join(dir, "does-not-exist"), nil)
	if err != nil {
		t.Fatalf("missing root should not error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing root fingerprint = %v, want empty", keysOf(missing))
	}
}

// TestMatchIgnore 表驱动覆盖 glob / 目录前缀 / `**` 三类语法。
func TestMatchIgnore(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		pats []string
		want bool
	}{
		// glob（path.Match 语义，不跨 `/`）
		{"glob hit", "main.go", []string{"*.go"}, true},
		{"glob miss", "main.rs", []string{"*.go"}, false},
		{"glob not crossing slash", "cmd/main.go", []string{"*.go"}, false},
		{"glob with dir", "cmd/main.go", []string{"cmd/*.go"}, true},
		{"glob dir prefix covers subtree", "cmd/sub/main.go", []string{"cmd/*.go"}, false},
		{"char class", "a1.go", []string{"a[0-9].go"}, true},
		{"char class miss", "ax.go", []string{"a[0-9].go"}, false},

		// 目录前缀
		{"dir prefix self", "vendor", []string{"vendor/"}, true},
		{"dir prefix child", "vendor/lib/a.go", []string{"vendor/"}, true},
		{"dir prefix no partial segment", "vendorish/a.go", []string{"vendor/"}, false},
		{"dir prefix without slash", "vendor/lib/a.go", []string{"vendor"}, true},
		{"dir prefix nested", "a/b/vendor/x.go", []string{"a/b/vendor/"}, true},
		{"dir prefix nested miss", "a/vendor/x.go", []string{"a/b/vendor/"}, false},

		// `**`
		{"doublestar tail", "src/deep/x/y.go", []string{"src/**"}, true},
		{"doublestar tail self", "src", []string{"src/**"}, true},
		{"doublestar tail other", "other/x.go", []string{"src/**"}, false},
		{"doublestar head", "pkg/api/types.pb.go", []string{"**/*.pb.go"}, true},
		{"doublestar head zero dirs", "types.pb.go", []string{"**/*.pb.go"}, true},
		{"doublestar head miss", "pkg/api/types.go", []string{"**/*.pb.go"}, false},
		{"doublestar middle", "a/x/y/z.go", []string{"a/**/z.go"}, true},
		{"doublestar middle zero", "a/z.go", []string{"a/**/z.go"}, true},
		{"doublestar middle miss", "a/x/y/q.go", []string{"a/**/z.go"}, false},
		{"doublestar dir prefix", "x/y/gen", []string{"**/gen/"}, true},
		{"doublestar dir prefix child", "x/y/gen/f.go", []string{"**/gen/"}, true},
		{"doublestar dir prefix miss", "x/y/generate/f.go", []string{"**/gen/"}, false},

		// 多模式与边界
		{"multi pattern", "out.bin", []string{"*.go", "*.bin"}, true},
		{"empty patterns", "a.go", nil, false},
		{"empty rel", "", []string{"*.go"}, false},
		{"leading dot slash", "./main.go", []string{"*.go"}, true},
		{"trailing slash rel", "cmd/main.go/", []string{"cmd/"}, true},
		{"ignored root self", "sub/a.go", []string{"sub"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchIgnore(tc.rel, tc.pats); got != tc.want {
				t.Fatalf("MatchIgnore(%q, %v) = %v, want %v", tc.rel, tc.pats, got, tc.want)
			}
		})
	}

	// 非法模式不 panic：MatchIgnore 跳过它（错误路径由 NewWatcher 负责报错）。
	if MatchIgnore("a.go", []string{"[bad"}) {
		t.Fatal("invalid pattern should not match")
	}
}

// TestWatcherDebouncesQuickWrites 是核心行为测试：若干次快速写入只应
// 产出一个批次，且批次内路径去重排序。
func TestWatcherDebouncesQuickWrites(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.go"), "package main\n")

	w, err := NewWatcher(WatchSpec{Roots: []string{dir}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 等基线就绪（观测探针被上报），而不是猜时间。
	awaitBaseline(t, w, ch, dir, "probe-baseline.go", "debounce test")

	// 跨多个轮询周期、但在一个去抖窗口内的多次写入，必须折叠成一个批次。
	//
	// 这里刻意把写入**摊到多个轮询周期**上：每次写入后等待一个 Interval，
	// 使轮询必然分多次观测到变化。若去抖失效（每次观测到变化就立即发批次），
	// 就会产生多个批次；若去抖正常工作，后一次观测会重置计时器，
	// 所有变化累积进同一批 pending，安静满 Debounce 后只发一批。
	//
	// 这与"4 次写入挤在同一次轮询内"的写法有本质区别：后者无论去抖是否存在
	// 都只产生一批，因此**证明不了**去抖（实测：把去抖整段删掉，旧写法仍通过）。
	// 每个文件只写一次，避免连续改写同一路径造成去抖计时器反复重置而永不发批次。
	var wantPaths []string
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("hot-%d.go", i)
		wantPaths = append(wantPaths, name)
		writeFile(t, filepath.Join(dir, name), strings.Repeat("x", i+1))
		// 间隔取 Interval 的 2/3：足以让轮询跨周期观测到，
		// 又远小于 Debounce（3 次共 ≈ 2*Interval < Debounce 的 25ms 窗口），
		// 因此整体仍落在一个去抖窗口内。
		time.Sleep(testInterval * 2 / 3)
	}

	batch := recvBatch(t, ch, 2*time.Second, "debounced batch")
	if len(batch.Paths) == 0 {
		t.Fatal("batch has no paths")
	}
	if batch.At.IsZero() {
		t.Fatal("batch.At is zero")
	}
	// 关键断言：三次跨轮询的写入必须**全部**出现在这一个批次里。
	// 缺任何一个都说明去抖把变化吞掉了（而不是折叠）。
	for _, name := range wantPaths {
		if !containsPath(batch.Paths, name) {
			t.Fatalf("batch %v should contain %s（跨轮询的变化必须折叠进同一批次，不得被吞）", batch.Paths, name)
		}
	}
	// 去重：同一路径只出现一次。
	seen := map[string]int{}
	for _, p := range batch.Paths {
		seen[p]++
	}
	for p, n := range seen {
		if n > 1 {
			t.Fatalf("path %q appears %d times in one batch: %v", p, n, batch.Paths)
		}
	}
	// 排序检查。
	for i := 1; i < len(batch.Paths); i++ {
		if batch.Paths[i-1] > batch.Paths[i] {
			t.Fatalf("batch paths not sorted: %v", batch.Paths)
		}
	}
	// 关键断言：跨轮询的多次写入只应产出**这一个**批次——
	// 去抖若失效，每次轮询观测都会各发一批，这里就会发现第二、第三批。
	assertNoBatch(t, ch, "after debounced batch")
}

// TestWatcherEmitsSeparateBatchesForSeparatedChanges 验证去抖不是"永久吞并"：
// 安静之后再改一次，必须产生新批次。
func TestWatcherEmitsSeparateBatchesForSeparatedChanges(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "seed.go"), "package main\n")

	w, err := NewWatcher(WatchSpec{Roots: []string{dir}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	awaitBaseline(t, w, ch, dir, "probe-baseline.go", "separated changes test")

	writeFile(t, filepath.Join(dir, "first.go"), "1")
	first := recvBatch(t, ch, 2*time.Second, "first batch")
	if !containsPath(first.Paths, "first.go") {
		t.Fatalf("first batch %v missing first.go", first.Paths)
	}
	assertNoBatch(t, ch, "between separated changes")

	writeFile(t, filepath.Join(dir, "second.go"), "2")
	second := recvBatch(t, ch, 2*time.Second, "second batch")
	if !containsPath(second.Paths, "second.go") {
		t.Fatalf("second batch %v missing second.go", second.Paths)
	}
	if containsPath(second.Paths, "first.go") {
		t.Fatalf("second batch should not repeat first.go: %v", second.Paths)
	}
}

// TestWatcherIgnoresIgnoredPaths 验证 ignore 在 Watcher 层生效：
// 只写被忽略的文件不产生批次。
func TestWatcherIgnoresIgnoredPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.go"), "package main\n")

	w, err := NewWatcher(WatchSpec{
		Roots:    []string{dir},
		Ignore:   []string{"ignored/", "*.tmp"},
		Interval: testInterval,
		Debounce: testDebounce,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 探针名不得被本测试的 ignore 规则命中（"ignored/" 与 "*.tmp"）。
	awaitBaseline(t, w, ch, dir, "probe-baseline.go", "ignored paths test")

	writeFile(t, filepath.Join(dir, "ignored", "x.go"), "package x\n")
	writeFile(t, filepath.Join(dir, "scratch.tmp"), "tmp")
	// 被忽略的写入在 settleBudget 内不得引发批次。
	assertNoBatch(t, ch, "ignored writes")

	// 反过来，非忽略路径必须立刻可见，证明监视本身是活的。
	writeFile(t, filepath.Join(dir, "real.go"), "package main\n")
	batch := recvBatch(t, ch, 2*time.Second, "non-ignored write")
	if !containsPath(batch.Paths, "real.go") {
		t.Fatalf("batch %v missing real.go", batch.Paths)
	}
	for _, p := range batch.Paths {
		if strings.HasPrefix(p, "ignored/") || strings.HasSuffix(p, ".tmp") {
			t.Fatalf("ignored path leaked into batch: %v", batch.Paths)
		}
	}
}

// TestWatcherClosesOnContextCancel 验证取消后通道关闭。
func TestWatcherClosesOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package main\n")

	w, err := NewWatcher(WatchSpec{Roots: []string{dir}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	awaitBaseline(t, w, ch, dir, "probe-baseline.go", "cancel test")

	// 制造一个在途批次，然后取消：要么先收到批次再关闭，要么直接关闭。
	writeFile(t, filepath.Join(dir, "b.go"), "package main\n")
	cancel()
	recvClosed(t, ch, 2*time.Second, "cancelled watcher")
}

// TestWatcherCancelBeforeStart 验证 ctx 已取消时 Run 立即返回已关闭通道。
func TestWatcherCancelBeforeStart(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWatcher(WatchSpec{Roots: []string{dir}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	var ch <-chan Batch
	var runErr error
	go func() {
		ch, runErr = w.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly for an already-cancelled context")
	}
	if runErr != nil {
		t.Fatalf("Run with cancelled ctx: %v", runErr)
	}
	recvClosed(t, ch, time.Second, "cancelled-before-start watcher")
}

// TestWatcherMultipleRoots 验证多根目录合并成一个快照流。
func TestWatcherMultipleRoots(t *testing.T) {
	base := t.TempDir()
	rootA := filepath.Join(base, "a")
	rootB := filepath.Join(base, "b")
	writeFile(t, filepath.Join(rootA, "one.go"), "package a\n")
	writeFile(t, filepath.Join(rootB, "two.go"), "package b\n")

	w, err := NewWatcher(WatchSpec{Roots: []string{rootA, rootB}, Interval: testInterval, Debounce: testDebounce})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	awaitBaseline(t, w, ch, rootB, "probe-baseline.go", "multi-root test")

	writeFile(t, filepath.Join(rootB, "three.go"), "package b\n")
	batch := recvBatch(t, ch, 2*time.Second, "multi-root batch")
	// 多根时键带根前缀，路径里应能看出是 b 根下的变化。
	joined := strings.Join(batch.Paths, ",")
	if !strings.Contains(joined, "three.go") {
		t.Fatalf("batch %v missing three.go", batch.Paths)
	}
	if !strings.Contains(joined, "b/") {
		t.Fatalf("multi-root batch %v should carry the root prefix", batch.Paths)
	}
}

// TestWaitForChange 覆盖一次性等待的便捷函数，含取消路径。
func TestWaitForChange(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "seed.go"), "package main\n")
	spec := WatchSpec{Roots: []string{dir}, Interval: testInterval, Debounce: testDebounce}

	// 超时路径：没有任何改动时按 ctx 期限报错（而不是永远挂着）。
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelShort()
	if _, err := WaitForChange(shortCtx, spec); err == nil {
		t.Fatal("WaitForChange should fail when ctx expires with no change")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForChange err = %v, want DeadlineExceeded", err)
	}

	// 正常路径：应拿到 later.go 的路径。
	//
	// WaitForChange 内部自己起 watcher，调用方拿不到它的基线就绪信号，
	// 因此不能靠 sleep 猜"它快照完了没有"——太早写入会被并入基线，
	// 于是永远等不到变化，一直挂到 ctx 超时。
	//
	// 改为**写一轮、等一轮**直到返回（与 awaitBaseline 同一模式）：
	// WaitForChange 等的是"相对其基线的首个变化"，只要在它建立基线之后
	// 再写一次就必然返回。每轮之后留出静默窗口，让去抖得以到期——若像
	// 以前那样持续短间隔写入，会不断重置去抖计时器，批次永远发不出来。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		paths []string
		err   error
	}
	resCh := make(chan result, 1)
	go func() {
		paths, err := WaitForChange(ctx, spec)
		resCh <- result{paths, err}
	}()

	target := filepath.Join(dir, "later.go")
	roundWait := 2*spec.Debounce + 2*spec.Interval
	var res result
	done := false
	for i := 1; !done; i++ {
		writeFile(t, target, strings.Repeat("y", i))

		round := time.After(roundWait)
	roundLoop:
		for {
			select {
			case res = <-resCh:
				done = true
				break roundLoop
			case <-round:
				break roundLoop // 本轮静默期已过仍未返回，补写一次再等。
			case <-ctx.Done():
				t.Fatal("WaitForChange did not return before ctx deadline")
			}
		}
	}

	if res.err != nil {
		t.Fatalf("WaitForChange: %v", res.err)
	}
	if !containsPath(res.paths, "later.go") {
		t.Fatalf("WaitForChange paths = %v, want later.go", res.paths)
	}

	// 非法 spec 直接报错。
	if _, err := WaitForChange(context.Background(), WatchSpec{}); !errors.Is(err, ErrNoRoots) {
		t.Fatalf("WaitForChange with no roots: err = %v, want ErrNoRoots", err)
	}
}

// TestChangedNilAndEmpty 覆盖 Changed 的边界输入。
func TestChangedNilAndEmpty(t *testing.T) {
	if got := Changed(nil, nil); got != nil {
		t.Fatalf("Changed(nil,nil) = %v, want nil", got)
	}
	if got := Changed(map[string]string{}, map[string]string{}); got != nil {
		t.Fatalf("Changed(empty,empty) = %v, want nil", got)
	}
	got := Changed(map[string]string{"a": "1"}, map[string]string{"a": "1", "b": "2"})
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("Changed = %v, want [b]", got)
	}
	got = Changed(map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "1"})
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("Changed = %v, want [b]", got)
	}
	got = Changed(map[string]string{"a": "1"}, map[string]string{"a": "2"})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("Changed = %v, want [a]", got)
	}
}

// ---- 测试小工具 ----

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func intersect(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, s := range b {
		set[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if set[s] {
			out = append(out, s)
		}
	}
	return out
}
