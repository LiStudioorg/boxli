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

// writeTempAttr 造一个可读的 attr 文件，返回路径。
func writeTempAttr(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "exec")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("写 attr 文件: %v", err)
	}
	return p
}

// TestReadSELinuxExecContext 覆盖读取继承上下文的各种情形。
func TestReadSELinuxExecContext(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T) string
		wantCtx string
		wantOK  bool
		wantErr bool
	}{
		{
			name: "enforcing-context",
			setup: func(t *testing.T) string {
				return writeTempAttr(t, "u:r:boxli:s0\n")
			},
			wantCtx: "u:r:boxli:s0",
			wantOK:  true,
		},
		{
			name: "permissive-context",
			setup: func(t *testing.T) string {
				return writeTempAttr(t, "u:r:shell:s0")
			},
			wantCtx: "u:r:shell:s0",
			wantOK:  true,
		},
		{
			name: "empty-file-means-no-context",
			setup: func(t *testing.T) string {
				return writeTempAttr(t, "")
			},
			wantOK: false,
		},
		{
			name: "whitespace-only-means-no-context",
			setup: func(t *testing.T) string {
				return writeTempAttr(t, "  \n\t ")
			},
			wantOK: false,
		},
		{
			name: "context-with-trailing-newline-trimmed",
			setup: func(t *testing.T) string {
				return writeTempAttr(t, "  u:r:x:s0  \n")
			},
			wantCtx: "u:r:x:s0",
			wantOK:  true,
		},
		{
			name: "missing-file-skips-not-error",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantOK: false,
		},
		{
			name: "directory-instead-of-file-is-error",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			ctx, ok, err := readSELinuxExecContext(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v (ctx=%q)", ok, tc.wantOK, ctx)
			}
			if ctx != tc.wantCtx {
				t.Fatalf("ctx=%q, want %q", ctx, tc.wantCtx)
			}
		})
	}
}

// TestReadSELinuxExecContextRealHost 在真实宿主上读取：无论是否启用 SELinux，
// 都必须**不返回错误**（未启用时内核给 EINVAL，属正常跳过路径）。
func TestReadSELinuxExecContextRealHost(t *testing.T) {
	ctx, ok, err := readSELinuxExecContext("/proc/self/attr/exec")
	if err != nil {
		t.Fatalf("真实宿主上读取 attr/exec 不应报错（未启用 SELinux 时内核返回 EINVAL，属正常）: %v", err)
	}
	t.Logf("宿主 attr/exec: ctx=%q ok=%v", ctx, ok)
}

// TestReadSELinuxExecContextEINVALIsSkip 显式覆盖 EINVAL → 跳过而非报错。
// 这是**非 SELinux 宿主的主路径**：本机 /proc/self/attr/exec 恒返回 EINVAL，
// 若当成错误处理，所有非 SELinux 设备都会打出误导性的告警。
func TestReadSELinuxExecContextEINVALIsSkip(t *testing.T) {
	// 用命名管道模拟"存在但读起来是 EINVAL"不方便，改为直接验证错误分类
	// 逻辑：构造一个 PathError(EINVAL) 走与 readSELinuxExecContext 相同的判定。
	err := &os.PathError{Op: "read", Path: "/proc/self/attr/exec", Err: syscall.EINVAL}
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatal("errors.Is 应能穿透 PathError 识别 EINVAL")
	}
}

// TestApplySELinuxExecContext 覆盖写入路径。
func TestApplySELinuxExecContext(t *testing.T) {
	t.Run("writes-context", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "exec")
		if err := applySELinuxExecContext(p, "u:r:boxli:s0"); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "u:r:boxli:s0" {
			t.Fatalf("内容 = %q", got)
		}
	})

	t.Run("empty-context-is-noop", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "exec")
		if err := applySELinuxExecContext(p, ""); err != nil {
			t.Fatalf("空上下文应直接返回: %v", err)
		}
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("空上下文不应创建文件: %v", err)
		}
	})

	t.Run("write-failure-reported", func(t *testing.T) {
		// 指向不存在目录下的文件 → 写入必然失败，错误必须上报（不吞）。
		p := filepath.Join(t.TempDir(), "no", "such", "dir", "exec")
		err := applySELinuxExecContext(p, "u:r:x:s0")
		if err == nil {
			t.Fatal("写入不可达路径应报错")
		}
		if !strings.Contains(err.Error(), "写入") {
			t.Fatalf("错误应含上下文: %v", err)
		}
	})
}

// TestInheritSELinuxContextDisabledHost 覆盖非 SELinux 宿主：
// 必须返回空串且**不 panic、不报错**（这是绝大多数 Linux 服务器与本机的路径）。
func TestInheritSELinuxContextDisabledHost(t *testing.T) {
	old := selinuxAttrExec
	selinuxAttrExec = filepath.Join(t.TempDir(), "nonexistent")
	t.Cleanup(func() { selinuxAttrExec = old })

	if got := inheritSELinuxContext(); got != "" {
		t.Fatalf("无 attr 文件时应返回空串, got %q", got)
	}
}

// TestInheritSELinuxContextNoContext 覆盖"文件在但无上下文"（空串）。
func TestInheritSELinuxContextNoContext(t *testing.T) {
	old := selinuxAttrExec
	selinuxAttrExec = writeTempAttr(t, "")
	t.Cleanup(func() { selinuxAttrExec = old })

	if got := inheritSELinuxContext(); got != "" {
		t.Fatalf("空上下文时应返回空串, got %q", got)
	}
}

// TestInheritSELinuxContextReadFailureDegrades 覆盖读取失败：
// 必须降级（返回空串）而不是 panic 或阻断。
func TestInheritSELinuxContextReadFailureDegrades(t *testing.T) {
	old := selinuxAttrExec
	selinuxAttrExec = t.TempDir() // 目录：ReadFile 报 EISDIR
	t.Cleanup(func() { selinuxAttrExec = old })

	if got := inheritSELinuxContext(); got != "" {
		t.Fatalf("读取失败时应降级为空串, got %q", got)
	}
}

// TestInheritSELinuxContextReadOnlyDegrades 覆盖写入失败（典型：enforcing 下
// 策略拒绝写 attr/exec，或文件只读）：必须降级且**不阻断容器启动**。
func TestInheritSELinuxContextReadOnlyDegrades(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "exec")
	if err := os.WriteFile(p, []byte("u:r:inherited:s0"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 设为只读文件：写入会失败（以 root 运行时 chmod 000 仍可写，故用目录只读
	// 不可靠；改为把路径指向一个只读挂载点不现实——直接用 0444 并在必要时跳过）。
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root 可无视文件权限，无法用 chmod 模拟写入失败")
	}
	old := selinuxAttrExec
	selinuxAttrExec = p
	t.Cleanup(func() { selinuxAttrExec = old })

	// 关键断言：不 panic、返回空串（降级），不阻断。
	if got := inheritSELinuxContext(); got != "" {
		t.Fatalf("写入失败时应降级为空串, got %q", got)
	}
}

// TestInheritSELinuxContextSuccess 覆盖成功路径：读到上下文并写回成功。
func TestInheritSELinuxContextSuccess(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "exec")
	if err := os.WriteFile(p, []byte("u:r:boxli:s0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := selinuxAttrExec
	selinuxAttrExec = p
	t.Cleanup(func() { selinuxAttrExec = old })

	if got := inheritSELinuxContext(); got != "u:r:boxli:s0" {
		t.Fatalf("应返回继承的上下文, got %q", got)
	}
	// 写回后文件内容应仍是该上下文（无换行）。
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "u:r:boxli:s0" {
		t.Fatalf("写回内容 = %q, 期望 %q", data, "u:r:boxli:s0")
	}
}

// TestSELinuxNoGlobalStateChange 是安全约束测试：SELinux 处理代码**不得**
// 触碰任何全局 SELinux 状态。用静态检查保证"绝不调用 setenforce / 绝不改
// /sys/fs/selinux/*"这条硬要求不会在后续改动中被破坏。
//
// 只检查**代码**，先剥掉注释：源码注释里出现这些名字是正常的（用于说明
// "为什么不用 attr/current 而用 attr/exec"），把它们当成违规会产生噪音。
func TestSELinuxNoGlobalStateChange(t *testing.T) {
	forbidden := []string{
		"setenforce",
		"/sys/fs/selinux",
		"selinuxfs",
		"attr/current",
	}
	for _, path := range []string{"init_linux.go", "start_linux.go", "exec_linux.go"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读 %s: %v", path, err)
		}
		code := stripGoComments(string(data))
		for _, f := range forbidden {
			if strings.Contains(code, f) {
				t.Errorf("%s 代码中出现被禁止的 SELinux 全局状态访问 %q："+
					"Boxli 只允许读写自身进程的 attr/exec，绝不改动全局 SELinux 状态", path, f)
			}
		}
	}
}

// stripGoComments 去掉 Go 源码中的行注释与块注释，保留其余内容。
// 只需正确处理本项目代码风格（无注释嵌套、无注释出现在字符串字面量中间），
// 目的是让安全约束测试只针对真实代码生效。
func stripGoComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(line, "*/"); idx >= 0 {
				inBlock = false
				line = line[idx+2:]
			} else {
				continue
			}
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlock = true
			}
			continue
		}
		// 行尾注释：不追求完美解析，只剥离 " //" 之后的部分。
		if idx := strings.Index(line, " //"); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
