// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiStudioorg/licore/internal/build"
)

// writeCtx 造一个最小可构建的上下文目录：Boxfile（FROM scratch + COPY 一个
// 只在本目录存在的文件）+ 该文件。文件名带 tag 以便区分不同目录。
func writeCtx(t *testing.T, tag string) string {
	t.Helper()
	dir := t.TempDir()
	box := "FROM scratch\nCOPY " + tag + ".txt /" + tag + ".txt\nENTRYPOINT [\"/x\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "Boxfile"), []byte(box), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tag+".txt"), []byte(tag), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runBuild 在给定 cwd 下执行 root build 命令，返回输出与错误。
func runBuild(t *testing.T, cwd string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(cwd)
	var buf bytes.Buffer
	root := NewRootCommand(&buf, &buf)
	root.SetArgs(append([]string{"build"}, args...))
	err := root.Execute()
	return buf.String(), err
}

// -------- 纯函数：resolveBuildContext --------

func TestResolveBuildContext(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		args    []string
		want    string
		wantErr string // 子串；空表示期望成功
	}{
		{"positional-only", "", []string{"ctxdir"}, "ctxdir", ""},
		{"flag-only", "ctxdir", nil, "ctxdir", ""},
		{"both-same", "a/ctx", []string{"a/./ctx"}, "a/ctx", ""}, // Clean 后相等
		{"both-conflict", "flag/dir", []string{"pos/dir"}, "", "冲突"},
		{"neither", "", nil, "", "必须显式指定构建上下文"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBuildContext(tc.flag, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("期望包含 %q 的错误, got %v", tc.wantErr, err)
				}
				if !errors.Is(err, build.ErrNoContext) {
					t.Errorf("错误应 wrap build.ErrNoContext: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// -------- 命令级：上下文必须真正生效 --------
//
// 原 bug 恰好在 flag 默认值与判空分支的交互上（`--context` 默认 "." 使
// `if contextDir == ""` 永假，位置参数被静默丢弃），只有走完整命令链路
// 才能暴露，纯函数测不出来，所以这里必须有端到端用例。

// TestBuildContextPositionalIsUsed 是核心回归用例：cwd 是 ctxHere、位置
// 参数指向 ctxThere，两边的 Boxfile COPY **各自独有的文件**。位置参数
// 若被忽略（旧 bug），COPY 会去 cwd 找 only_there.txt 并失败。
func TestBuildContextPositionalIsUsed(t *testing.T) {
	home := t.TempDir()
	cwd := writeCtx(t, "only_here")
	there := writeCtx(t, "only_there")

	out, err := runBuild(t, cwd, "-t", "ctx/pos:v1", "--data-dir", home, there)
	if err != nil {
		t.Fatalf("位置参数上下文应可构建（out=%s）: %v", out, err)
	}
	// 反向自查：镜像层里必须是 there 的文件而不是 cwd 的。
	if !boxliFileHas(t, filepath.Join(home, "images", "ctx", "pos", "v1", "source.boxli"), "only_there.txt") {
		t.Error("镜像应含 only_there.txt（证明 COPY 源来自位置参数目录）")
	}
	if boxliFileHas(t, filepath.Join(home, "images", "ctx", "pos", "v1", "source.boxli"), "only_here.txt") {
		t.Error("镜像不应含 cwd 专属的 only_here.txt（说明误用了 cwd 上下文）")
	}
}

// boxliFileHas 检查 .boxli（外层 tar 内含 layers/*.tar.gz）的任一层里
// 是否含以 name 结尾的文件条目。
func boxliFileHas(t *testing.T, boxPath, name string) bool {
	t.Helper()
	f, err := os.Open(boxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(h.Name, ".tar.gz") {
			continue
		}
		gz, err := gzip.NewReader(tr)
		if err != nil {
			t.Fatal(err)
		}
		lr := tar.NewReader(gz)
		for {
			lh, err := lr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(lh.Name, name) {
				return true
			}
		}
		gz.Close()
	}
}

// TestBuildContextFlagIsUsed 同构用例走 --context。
func TestBuildContextFlagIsUsed(t *testing.T) {
	home := t.TempDir()
	cwd := writeCtx(t, "only_here")
	there := writeCtx(t, "only_there")

	out, err := runBuild(t, cwd, "-t", "ctx/flag:v1", "--data-dir", home, "--context", there)
	if err != nil {
		t.Fatalf("--context 上下文应可构建（out=%s）: %v", out, err)
	}
}

// TestBuildContextDefaultIsRejected 两者都不给：必须明确报错，绝不静默
// 使用 cwd——这是"最坏失败模式"（错镜像/敏感文件被静默打进镜像）的根源。
func TestBuildContextDefaultIsRejected(t *testing.T) {
	home := t.TempDir()
	cwd := writeCtx(t, "only_here")

	_, err := runBuild(t, cwd, "-t", "ctx/none:v1", "--data-dir", home)
	if err == nil {
		t.Fatal("不给上下文必须报错（不得静默用 cwd）")
	}
	if !strings.Contains(err.Error(), "必须显式指定构建上下文") {
		t.Errorf("报错应指明缺少上下文: %v", err)
	}
	if !errors.Is(err, build.ErrNoContext) {
		t.Errorf("应 wrap build.ErrNoContext: %v", err)
	}
}

// TestBuildContextBothAgreeAccepted 位置参数与 --context 指向同一目录：
// 允许（脚本里两边都传是合法用法）。
func TestBuildContextBothAgreeAccepted(t *testing.T) {
	home := t.TempDir()
	cwd := writeCtx(t, "only_here")
	there := writeCtx(t, "only_there")

	if _, err := runBuild(t, cwd, "-t", "ctx/both:v1", "--data-dir", home, there, "--context", filepath.Clean(there)); err != nil {
		t.Fatalf("同值双传应放行: %v", err)
	}
}

// TestBuildContextConflictRejected 两者给了不同目录：拒绝，错误里列出
// 两个路径（让用户自己看出传错了哪个）。
func TestBuildContextConflictRejected(t *testing.T) {
	home := t.TempDir()
	cwd := writeCtx(t, "only_here")
	a := writeCtx(t, "only_there")

	_, err := runBuild(t, cwd, "-t", "ctx/con:v1", "--data-dir", home, a, "--context", cwd)
	if err == nil {
		t.Fatal("冲突必须拒绝")
	}
	if !strings.Contains(err.Error(), "冲突") {
		t.Errorf("错误应说明冲突: %v", err)
	}
}

// TestBuildBoxfileFoundInContext 未给 --file 时 Boxfile 从上下文目录找，
// 而不是 cwd。
func TestBuildBoxfileFoundInContext(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir() // 空的 cwd：没有 Boxfile
	there := writeCtx(t, "only_there")

	if _, err := runBuild(t, cwd, "-t", "ctx/bf:v1", "--data-dir", home, there); err != nil {
		t.Fatalf("Boxfile 应从上下文目录解析: %v", err)
	}
}
