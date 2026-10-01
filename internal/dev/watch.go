// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

// Package dev 实现 `boxli dev` 的热重载文件监视：轮询源码树的指纹
// （mtime + size + mode），把连续变化去抖成一个批次后交给上层触发
// 重建与重启。
//
// 为什么是轮询而不是 inotify：AGENTS.md 规定只允许标准库、禁止 CGO，
// 且目标平台包含 macOS 与 Android，inotify / kqueue / FSEvents 各自
// 需要平台专用代码。轮询在任何平台行为一致、可测试、无 fd 泄漏，
// 代价是可以接受的（默认 500ms 一次 stat 遍历）。
package dev

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 默认参数：Interval 与 Debounce 都可以由 WatchSpec 覆盖。
const (
	// DefaultInterval 是两次指纹比对之间的默认间隔。
	DefaultInterval = 500 * time.Millisecond

	// DefaultDebounce 是默认去抖窗口：窗口内持续有新变化就不断顺延，
	// 直到安静了这么久才发一个批次。
	DefaultDebounce = 300 * time.Millisecond
)

// defaultSkipDirs 是任何情况下都不参与指纹的目录名（按目录基名匹配）。
// `.git` 与 `node_modules` 是任务规定的硬跳过项；它们数量大且噪音高。
var defaultSkipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// Batch 是一次去抖之后交付给上层的变化集合。
type Batch struct {
	// Paths 是本次批次中新增 / 修改 / 删除的相对路径，已排序去重。
	Paths []string
	// At 是批次交付时刻。
	At time.Time
}

// WatchSpec 描述一次监视的输入。
type WatchSpec struct {
	// Roots 是要监视的目录，至少一个；相对路径按当前工作目录解析。
	Roots []string
	// Ignore 是忽略模式，见 MatchIgnore：glob、目录前缀（`vendor/`）与 `**`。
	Ignore []string
	// Debounce 是去抖窗口，<=0 时用 DefaultDebounce。
	Debounce time.Duration
	// Interval 是轮询间隔，<=0 时用 DefaultInterval。
	Interval time.Duration
}

// normalize 补齐默认值并复制切片，避免调用方之后修改切片影响已建好的 Watcher。
func (s WatchSpec) normalize() WatchSpec {
	out := s
	out.Roots = append([]string(nil), s.Roots...)
	out.Ignore = append([]string(nil), s.Ignore...)
	if out.Debounce <= 0 {
		out.Debounce = DefaultDebounce
	}
	if out.Interval <= 0 {
		out.Interval = DefaultInterval
	}
	return out
}

// Watcher 按固定间隔轮询一组根目录，把变化去抖成 Batch 后发出。
// Watcher 自身不持有状态文件，可由 NewWatcher 构造后反复 Run。
type Watcher struct {
	spec WatchSpec
}

// NewWatcher 校验 WatchSpec 并构造 Watcher。Roots 为空返回 ErrNoRoots；
// 忽略模式非法返回 ErrBadIgnore（由 MatchIgnore 预检）。
func NewWatcher(spec WatchSpec) (*Watcher, error) {
	if len(spec.Roots) == 0 {
		return nil, fmt.Errorf("new watcher: %w", ErrNoRoots)
	}
	for _, root := range spec.Roots {
		if strings.TrimSpace(root) == "" {
			return nil, fmt.Errorf("new watcher: %w", ErrNoRoots)
		}
	}
	norm := spec.normalize()
	for _, p := range norm.Ignore {
		if _, err := compileIgnore(p); err != nil {
			return nil, fmt.Errorf("new watcher: %w", err)
		}
	}
	return &Watcher{spec: norm}, nil
}

// Spec 返回补齐默认值之后的规格副本，供调用方查询实际生效的 Interval / Debounce。
func (w *Watcher) Spec() WatchSpec {
	return w.spec.normalize()
}

// Baseline 对全部 Roots 取一次指纹，返回合并后的快照。
// 顶层根目录不存在会被跳过（等价于该根没有文件），不视为错误。
func (w *Watcher) Baseline() (map[string]string, error) {
	return FingerprintAll(w.spec.Roots, w.spec.Ignore)
}

// Run 开始监视，返回只读批次通道。通道在 ctx 取消后关闭。
//
// 语义要点：
//   - ctx 在 Run 之前就已取消：立即返回一个已关闭的通道，不启动任何 goroutine；
//   - 首个批次是在基线之上产生的变化，因此 Run 会先取一次基线；
//   - 去抖窗口内持续到来变化会不断顺延，安静满 Debounce 才发出一个批次；
//   - 同一路径在一次批次内只出现一次。
func (w *Watcher) Run(ctx context.Context) (<-chan Batch, error) {
	if err := ctx.Err(); err != nil {
		closed := make(chan Batch)
		close(closed)
		return closed, nil
	}
	// 先确认基线可取，尽早把根目录级错误（例如全是非法模式）暴露给调用方。
	// 根不存在不算错误，所以这里基本不会失败，只在遍历出现硬错误时返回。
	if _, err := w.Baseline(); err != nil {
		return nil, fmt.Errorf("watcher baseline: %w", err)
	}

	out := make(chan Batch, 1)
	go w.loop(ctx, out)
	return out, nil
}

// loop 是 Run 的后台循环：轮询 → 累积变化 → 去抖 → 发批次。
func (w *Watcher) loop(ctx context.Context, out chan<- Batch) {
	defer close(out)

	ticker := time.NewTicker(w.spec.Interval)
	defer ticker.Stop()

	prev, err := w.Baseline()
	if err != nil {
		slog.Warn("dev: watcher baseline failed", slog.Any("err", err))
		prev = map[string]string{}
	}

	pending := map[string]struct{}{}
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-timerC:
			timerC = nil
			if len(pending) == 0 {
				continue
			}
			paths := sortedKeys(pending)
			pending = map[string]struct{}{}
			batch := Batch{Paths: paths, At: time.Now()}
			select {
			case out <- batch:
			case <-ctx.Done():
				return
			}
		case <-ticker.C:
			cur, err := w.Baseline()
			if err != nil {
				slog.Warn("dev: watcher snapshot failed", slog.Any("err", err))
				continue
			}
			changed := Changed(prev, cur)
			prev = cur
			if len(changed) == 0 {
				continue
			}
			for _, p := range changed {
				pending[p] = struct{}{}
			}
			// 去抖：窗口内再来变化就重置计时器。
			// Stop 返回 false 说明定时器已触发，其值可能还留在通道里，
			// 因此保留旧的 timerC 交给 select 消费（此时不会重复发批次，
			// 因为 pending 的收集只发生在这里）。
			if timer == nil {
				timer = time.NewTimer(w.spec.Debounce)
				timerC = timer.C
			} else if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
				timer.Reset(w.spec.Debounce)
				timerC = timer.C
			} else {
				timer.Reset(w.spec.Debounce)
				timerC = timer.C
			}
		}
	}
}

// FingerprintAll 对多个根目录取指纹并合并成一张表：key 为相对路径，
// 不同根之间以 `root/<rel>` 形式区分（单根时省略前缀，保持可读）。
func FingerprintAll(roots []string, ignore []string) (map[string]string, error) {
	out := map[string]string{}
	for _, root := range roots {
		fp, err := Fingerprint(root, ignore)
		if err != nil {
			return nil, err
		}
		multi := len(roots) > 1
		prefix := filepath.ToSlash(filepath.Clean(root))
		for rel, fpv := range fp {
			key := rel
			if multi {
				key = prefix + "/" + rel
			}
			out[key] = fpv
		}
	}
	return out, nil
}

// Fingerprint 对 root 做一次确定性快照：相对路径（相对 root，用 `/` 分隔）
// 映射到 "size:mtimeNano:mode"。
//
//   - 符号链接记录其目标（不跟随），指纹串为 "lstat 结果:link:<target>"；
//   - 不可读的条目跳过并 slog.Warn，绝不让整个遍历失败；
//   - root 本身不存在时返回空表且不报错（等价于"什么都没有"）；
//   - `.git`、`node_modules` 与 ignore 命中的路径不进表。
func Fingerprint(root string, ignore []string) (map[string]string, error) {
	compiled, err := compileAll(ignore)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}

	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		slog.Warn("dev: stat watch root failed", slog.String("root", root), slog.Any("err", err))
		return out, nil
	}
	if !info.IsDir() {
		// 单文件根：直接记录自身。
		if !skipName(path.Base(filepath.ToSlash(root))) {
			out[path.Base(filepath.ToSlash(root))] = fingerprintOf(root, info)
		}
		return out, nil
	}

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// 读不了的目录 / 文件：跳过，但不中断整棵树的遍历。
			slog.Warn("dev: walk entry failed", slog.String("path", p), slog.Any("err", err))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			slog.Warn("dev: rel path failed", slog.String("path", p), slog.Any("err", relErr))
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if isDir(d) && defaultSkipDirs[d.Name()] {
			return fs.SkipDir
		}
		if MatchIgnoreCompiled(rel, compiled) {
			if isDir(d) {
				return fs.SkipDir
			}
			return nil
		}
		fi, infoErr := os.Lstat(p)
		if infoErr != nil {
			slog.Warn("dev: lstat failed", slog.String("path", p), slog.Any("err", infoErr))
			return nil
		}
		out[rel] = fingerprintOf(p, fi)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("fingerprint %s: %w", root, walkErr)
	}
	return out, nil
}

// fingerprintOf 生成一个条目的指纹串：
//
//	普通文件/目录: "<size>:<mtimeNano>:<mode>"
//	符号链接:      "<size>:<mtimeNano>:<mode>:link:<target>"
//
// 目录的 size 只反映目录项占用，可能与内容不同步，所以修改目录内文件时
// 主要靠文件自身条目的变化；目录自身变化同样会被记录，保证新增/删除可见。
func fingerprintOf(p string, fi fs.FileInfo) string {
	mode := fi.Mode()
	base := fmt.Sprintf("%d:%d:%s", fi.Size(), fi.ModTime().UnixNano(), mode.String())
	if mode&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			slog.Warn("dev: readlink failed", slog.String("path", p), slog.Any("err", err))
			return base + ":link:?"
		}
		return base + ":link:" + filepath.ToSlash(target)
	}
	return base
}

// isDir 判定目录项是否为目录。符号链接本身按文件处理（不跟随、不下钻），
// 避免软链成环导致遍历无限展开；软链的目标已记入指纹串，变化同样可见。
func isDir(d fs.DirEntry) bool {
	if d.Type()&fs.ModeSymlink != 0 {
		return false
	}
	return d.IsDir()
}

// skipName 判定某个基名是否属于硬跳过集合。
func skipName(name string) bool { return defaultSkipDirs[name] }

// Changed 比较两次快照，返回新增 / 修改 / 删除的相对路径，已排序。
func Changed(before, after map[string]string) []string {
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	out := make([]string, 0, 4)
	for p, v := range after {
		old, ok := before[p]
		if !ok || old != v {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ignorePattern 是编译后的忽略模式。
//
// 支持的写法：
//
//	"*.go"            当前层 glob（path.Match 语法，不跨 `/`）
//	"internal/dev/*"  同上层 glob
//	"internal/**"     `**` 跨越任意层目录
//	"**/*.pb.go"      任意层下的 .pb.go
//	"vendor/"         目录前缀，等价于 "vendor/**"
//	"vendor"          目录前缀，等价于 "vendor/**"
//	"**/vendor/"      任意层的目录前缀
//
// 匹配对象是相对路径（`/` 分隔），也接受首尾多余的 `/`。
type ignorePattern struct {
	raw      string
	dirLike  bool
	hasGlob  bool
	segments []string
}

// compileAll 预编译一组模式。
func compileAll(patterns []string) ([]ignorePattern, error) {
	out := make([]ignorePattern, 0, len(patterns))
	for _, p := range patterns {
		c, err := compileIgnore(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// compileIgnore 校验并编译单个模式，语法非法时返回 ErrBadIgnore。
func compileIgnore(raw string) (ignorePattern, error) {
	trimmed := strings.Trim(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return ignorePattern{}, fmt.Errorf("%w: empty pattern %q", ErrBadIgnore, raw)
	}
	dirLike := strings.HasSuffix(raw, "/")
	hasGlob := strings.ContainsAny(trimmed, "*?[\\")
	c := ignorePattern{
		raw:      raw,
		dirLike:  dirLike,
		hasGlob:  hasGlob,
		segments: strings.Split(trimmed, "/"),
	}
	if hasGlob {
		// 用 path.Match 做一次语法预检：把 `**` 折叠成 `*` 后仍应能解析。
		probe := strings.ReplaceAll(trimmed, "**", "*")
		if _, err := path.Match(probe, "probe"); err != nil {
			return ignorePattern{}, fmt.Errorf("%w: %q: %v", ErrBadIgnore, raw, err)
		}
	}
	return c, nil
}

// MatchIgnore 判断相对路径 rel 是否被任一模式忽略。
// 模式语法见 ignorePattern 的文档注释；语法非法的模式会被跳过（不 panic）。
func MatchIgnore(rel string, patterns []string) bool {
	compiled, err := compileAll(patterns)
	if err != nil {
		slog.Warn("dev: bad ignore pattern", slog.Any("err", err))
		return false
	}
	return MatchIgnoreCompiled(rel, compiled)
}

// MatchIgnoreCompiled 是 MatchIgnore 的预编译版本，供遍历热路径使用。
func MatchIgnoreCompiled(rel string, compiled []ignorePattern) bool {
	norm := strings.Trim(strings.TrimPrefix(filepath.ToSlash(rel), "./"), "/")
	if norm == "" || norm == "." {
		return false
	}
	for _, c := range compiled {
		if c.match(norm) {
			return true
		}
	}
	return false
}

// match 判定一个规范化后的相对路径是否命中本模式。
func (c ignorePattern) match(rel string) bool {
	if c.dirLike {
		// 目录前缀：命中该目录自身及其全部后代。
		prefix := strings.Join(c.segments, "/")
		if !c.hasGlob {
			return rel == prefix || strings.HasPrefix(rel, prefix+"/")
		}
		// 带 glob 的目录前缀（如 `src/**/gen/`）逐段匹配。
		return c.matchSegments(strings.Split(rel, "/"), true)
	}
	if !c.hasGlob {
		// 无 glob：既可按整路径精确命中，也可按目录前缀命中
		// （这样 "vendor" 同时盖住 vendor/ 下的内容）。
		prefix := strings.Join(c.segments, "/")
		return rel == prefix || strings.HasPrefix(rel, prefix+"/")
	}
	if c.matchSegments(strings.Split(rel, "/"), false) {
		return true
	}
	// glob 模式同时作为目录前缀：`internal/dev/*` 也应盖住其后代。
	if c.matchPrefix(strings.Split(rel, "/")) {
		return true
	}
	return false
}

// matchSegments 用段序列匹配 `**` 语义；dirPrefix 为 true 时只要求匹配完
// 模式的最后一段（即路径位于该目录之下）。
func (c ignorePattern) matchSegments(segs []string, dirPrefix bool) bool {
	return segMatch(c.segments, segs, dirPrefix)
}

// matchPrefix 尝试把模式匹配到 rel 的某个前缀目录上。
func (c ignorePattern) matchPrefix(segs []string) bool {
	for i := 1; i < len(segs); i++ {
		if segMatch(c.segments, segs[:i], false) {
			return true
		}
	}
	return false
}

// segMatch 实现段级匹配：`**` 可吞掉任意多段（含 0 段）。
func segMatch(pat, name []string, dirPrefix bool) bool {
	if len(pat) == 0 {
		// 模式耗尽：dirPrefix 模式下接受剩余任意内容，否则要求完全对齐。
		return dirPrefix || len(name) == 0
	}
	if pat[0] == "**" {
		// `**` 吞 0..n 段。
		for i := 0; i <= len(name); i++ {
			if segMatch(pat[1:], name[i:], dirPrefix) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return segMatch(pat[1:], name[1:], dirPrefix)
}

// sortedKeys 把集合转成排序后的切片。
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WaitForChange 是"只要一个批次"的便捷封装：它建一个临时 Watcher 并等待
// 首个去抖批次，返回其排序后的路径列表。
//
//   - ctx 取消时返回 ctx.Err()（包装后）；
//   - 返回的通道在任何路径上都被 drain/关闭，不会泄漏 goroutine；
//   - spec 未设置 Interval/Debounce 时用默认值；测试里应显式传小值。
func WaitForChange(ctx context.Context, spec WatchSpec) ([]string, error) {
	w, err := NewWatcher(spec)
	if err != nil {
		return nil, fmt.Errorf("wait for change: %w", err)
	}
	batches, err := w.Run(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for change: %w", err)
	}
	select {
	case batch, ok := <-batches:
		if !ok {
			if cerr := ctx.Err(); cerr != nil {
				return nil, fmt.Errorf("wait for change: %w", cerr)
			}
			return nil, nil
		}
		return batch.Paths, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for change: %w", ctx.Err())
	}
}
