// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner 记录 systemctl 调用并按脚本返回错误。
type fakeRunner struct {
	calls    [][]string
	fail     map[string]error // 以 args 拼接为键
	disabled bool             // is-enabled 模拟未注册服务
}

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) (string, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	if err, ok := f.fail[key]; ok {
		// 与 execRunner.Output 同形态：stderr 文本拼进错误消息。
		return "Interactive authentication required.",
			fmt.Errorf("%s: %w（Interactive authentication required.）", key, err)
	}
	switch {
	case strings.HasSuffix(key, "is-enabled licore.service"):
		if f.disabled {
			return "not-found\n", errors.New("exit status 1")
		}
		return "enabled\n", nil
	case strings.HasSuffix(key, "is-active licore.service"):
		return "active\n", nil
	}
	return "", nil
}

func withSystemd(t *testing.T, has bool) {
	t.Helper()
	old := detectsSystemd
	detectsSystemd = func() bool { return has }
	t.Cleanup(func() { detectsSystemd = old })
}

func opts(t *testing.T, r Runner) *Options {
	t.Helper()
	return &Options{
		UnitDir:  t.TempDir(),
		Runner:   r,
		ExecPath: "/usr/local/bin/licore",
		DataDir:  "/data/licore",
	}
}

func TestUnitContent(t *testing.T) {
	o := opts(t, &fakeRunner{})
	got, err := o.UnitContent()
	if err != nil {
		t.Fatal(err)
	}
	want := "[Unit]\n" +
		"Description=LiCore container engine\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"Type=oneshot\n" +
		"RemainAfterExit=yes\n" +
		"ExecStart=/usr/local/bin/licore boot --data-dir /data/licore\n" +
		"ExecStop=/usr/local/bin/licore shutdown --data-dir /data/licore\n\n" +
		"[Install]\n" +
		"WantedBy=multi-user.target\n"
	if got != want {
		t.Fatalf("unit 内容不一致：\n%s\n期望：\n%s", got, want)
	}
}

func TestEnableWritesFileAndRegisters(t *testing.T) {
	withSystemd(t, true)
	fr := &fakeRunner{}
	o := opts(t, fr)
	res, err := Enable(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Systemd {
		t.Fatal("有 systemd 时应完成注册")
	}
	data, err := os.ReadFile(res.UnitPath)
	if err != nil {
		t.Fatalf("unit 文件未落盘: %v", err)
	}
	if !strings.Contains(string(data), "ExecStart=/usr/local/bin/licore boot") {
		t.Fatalf("unit 内容错误: %s", data)
	}
	wantCalls := [][]string{
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "licore.service"},
	}
	if len(fr.calls) != 2 || fr.calls[0][1] != "daemon-reload" || fr.calls[1][2] != "licore.service" {
		t.Fatalf("systemctl 调用序列错误: %v 期望 %v", fr.calls, wantCalls)
	}
}

func TestEnableNoSystemdStillWritesFile(t *testing.T) {
	withSystemd(t, false)
	fr := &fakeRunner{}
	o := opts(t, fr)
	res, err := Enable(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Systemd || res.Note == "" {
		t.Fatalf("无 systemd 时应生成文件并给出提示: %+v", res)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("无 systemd 时不得调用 systemctl: %v", fr.calls)
	}
}

func TestEnablePermissionDenied(t *testing.T) {
	withSystemd(t, true)
	fr := &fakeRunner{fail: map[string]error{
		"systemctl daemon-reload": errors.New("exit status 1"),
	}}
	o := opts(t, fr)
	_, err := Enable(context.Background(), o)
	if !errors.Is(err, ErrNotPermitted) {
		t.Fatalf("systemctl 认证失败应包装 ErrNotPermitted: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo licore boot enable") {
		t.Fatalf("错误信息应给出手动命令: %v", err)
	}
}

func TestEnableUnitDirPermission(t *testing.T) {
	withSystemd(t, true)
	if os.Geteuid() == 0 {
		t.Skip("root 下无法模拟目录权限")
	}
	o := &Options{UnitDir: "/proc/definitely-not-writable", Runner: &fakeRunner{}, ExecPath: "/usr/bin/licore"}
	_, err := Enable(context.Background(), o)
	if err == nil {
		t.Fatal("写不可写目录应失败")
	}
}

func TestDisableRemovesFile(t *testing.T) {
	withSystemd(t, true)
	fr := &fakeRunner{}
	o := opts(t, fr)
	if err := os.WriteFile(o.UnitPath(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Disable(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unit 文件应被删除")
	}
	joined := []string{}
	for _, c := range fr.calls {
		joined = append(joined, strings.Join(c, " "))
	}
	if !strings.Contains(strings.Join(joined, "|"), "systemctl disable --now licore.service") ||
		!strings.Contains(strings.Join(joined, "|"), "systemctl daemon-reload") {
		t.Fatalf("disable 调用序列错误: %v", joined)
	}
	// 幂等：文件不存在再 Disable 不报错
	if err := Disable(context.Background(), o); err != nil {
		t.Fatalf("幂等 Disable 报错: %v", err)
	}
}

func TestStatusEnabled(t *testing.T) {
	withSystemd(t, true)
	o := opts(t, &fakeRunner{})
	if err := os.WriteFile(o.UnitPath(), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Status(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.FileExists || st.Kind != "systemd" || st.UnitState != "enabled / active" {
		t.Fatalf("状态错误: %+v", st)
	}
}

func TestStatusNoSystemd(t *testing.T) {
	withSystemd(t, false)
	o := opts(t, &fakeRunner{})
	st, err := Status(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Kind != "none" || st.Enabled || st.FileExists || st.Note == "" {
		t.Fatalf("无 systemd 状态错误: %+v", st)
	}
}

func TestStatusUnitMissing(t *testing.T) {
	withSystemd(t, true)
	fr := &fakeRunner{disabled: true}
	o := opts(t, fr)
	st, err := Status(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || st.FileExists || st.Kind != "systemd" {
		t.Fatalf("未注册服务状态错误: %+v", st)
	}
	if st.UnitState != "not-found" {
		t.Fatalf("is-enabled 失败应回退状态原文: %q", st.UnitState)
	}
}

func TestUnitPathJoinsDir(t *testing.T) {
	o := &Options{UnitDir: "/tmp/x"}
	if got := o.UnitPath(); got != filepath.Join("/tmp/x", "licore.service") {
		t.Fatalf("UnitPath = %s", got)
	}
	if (&Options{}).UnitPath() != "/etc/systemd/system/licore.service" {
		t.Fatal("默认路径错误")
	}
}

func TestFoldUnitState(t *testing.T) {
	cases := map[string]string{
		"enabled\n": "enabled",
		"disabled":  "disabled",
		"Failed to get unit file state for licore.service: No such file or directory": "not-found",
		"":                      "not-found",
		"static\n":              "static",
		"some weird error text": "unknown",
	}
	for in, want := range cases {
		if got := foldUnitState(in); got != want {
			t.Errorf("foldUnitState(%q)=%q 期望 %q", in, got, want)
		}
	}
}
