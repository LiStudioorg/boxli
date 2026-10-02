// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package dev

import (
	"context"
	"errors"
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
	// 等首个轮询周期过去，确保基线已建立、写入不会与基线取快照竞争。
	time.Sleep(2 * testInterval)

	// 4 次快速写入，间隔远小于 debounce 窗口：应被折叠成一个批次。
	//
	// 定时脆弱点：debounce 计时器只在某次轮询**观察到**变化时才启动。
	// 若写入串跨越「轮询 → debounce 过期 → 再次轮询」这个边界，就会合法
	// 地产生第二个批次，测试随抖动偶发误报。
	//
	// 为了不受调度抖动影响，这里在写入**之前**先对齐到刚过完一次轮询的
	// 时刻：等待一个略大于 Interval 的静默期，使下一次轮询几乎必然会
	// 观测到整串写入。写入本身用极小间隔完成，确保它们落在同一次轮询
	// 观测内，从而稳定折叠为单一批次。
	time.Sleep(testInterval + 5*time.Millisecond)

	target := filepath.Join(dir, "hot.go")
	for i := 0; i < 4; i++ {
		writeFile(t, target, strings.Repeat("x", i+1))
		time.Sleep(200 * time.Microsecond)
	}

	batch := recvBatch(t, ch, 2*time.Second, "debounced batch")
	if len(batch.Paths) == 0 {
		t.Fatal("batch has no paths")
	}
	if batch.At.IsZero() {
		t.Fatal("batch.At is zero")
	}
	found := false
	for _, p := range batch.Paths {
		if p == "hot.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("batch %v should contain hot.go", batch.Paths)
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
	// 关键断言：这一串快速写入不得再产出第二个批次。
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
	time.Sleep(2 * testInterval)

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
	time.Sleep(2 * testInterval)

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
	time.Sleep(2 * testInterval)

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
	time.Sleep(2 * testInterval)

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

	// 正常路径：稍后写入，应拿到路径。先启动等待再写，避免竞态。
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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
	time.Sleep(3 * testInterval)
	writeFile(t, filepath.Join(dir, "later.go"), "package main\n")

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("WaitForChange: %v", res.err)
		}
		if !containsPath(res.paths, "later.go") {
			t.Fatalf("WaitForChange paths = %v, want later.go", res.paths)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitForChange did not return")
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
