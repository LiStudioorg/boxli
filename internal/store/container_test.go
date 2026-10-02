// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newCfg(t *testing.T, s *Store, name string) *ContainerConfig {
	t.Helper()
	id, err := NewContainerID()
	if err != nil {
		t.Fatal(err)
	}
	return &ContainerConfig{
		ConfigVersion: 1,
		ID:            id,
		Name:          name,
		ImageRef:      "demo/hello:v1",
		Rootfs:        filepath.Join(s.ContainerDir(id), "rootfs"),
		Restart:       RestartUnlessStoped,
		Hostname:      name,
		Cmd:           []string{"/bin/sh"},
		Env:           []string{"PATH=/bin"},
		CreatedAt:     nowUTC(),
	}
}

func TestContainerCreateLoad(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	cfg := newCfg(t, s, "web")
	if err := s.CreateContainer(cfg); err != nil {
		t.Fatalf("创建容器: %v", err)
	}
	got, err := s.LoadContainer(cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "web" || got.Restart != RestartUnlessStoped || got.Cmd[0] != "/bin/sh" {
		t.Fatalf("字段回读不一致: %+v", got)
	}
	// config.json 落位且目录结构符合约定
	if _, err := os.Stat(filepath.Join(s.ContainerDir(cfg.ID), "config.json")); err != nil {
		t.Fatal(err)
	}
}

func TestContainerNameConflict(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if err := s.CreateContainer(newCfg(t, s, "web")); err != nil {
		t.Fatal(err)
	}
	err := s.CreateContainer(newCfg(t, s, "web"))
	if !errors.Is(err, ErrContainerExists) {
		t.Fatalf("同名容器期望 ErrContainerExists，实得 %v", err)
	}
}

func TestContainerValidate(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	bad := []struct {
		name  string
		tweak func(*ContainerConfig)
	}{
		{"版本", func(c *ContainerConfig) { c.ConfigVersion = 2 }},
		{"ID 含斜杠", func(c *ContainerConfig) { c.ID = "a/b" }},
		{"名字含点前缀", func(c *ContainerConfig) { c.Name = ".hidden" }},
		{"空 cmd", func(c *ContainerConfig) { c.Cmd = nil }},
		{"非法策略", func(c *ContainerConfig) { c.Restart = Restart("never") }},
	}
	for _, b := range bad {
		cfg := newCfg(t, s, "t"+b.name)
		b.tweak(cfg)
		if err := s.CreateContainer(cfg); !errors.Is(err, ErrBadContainerConfig) {
			t.Fatalf("%s: 期望 ErrBadContainerConfig，实得 %v", b.name, err)
		}
	}
}

func TestFindContainer(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	c1 := newCfg(t, s, "alpha")
	c2 := newCfg(t, s, "beta")
	for _, c := range []*ContainerConfig{c1, c2} {
		if err := s.CreateContainer(c); err != nil {
			t.Fatal(err)
		}
	}
	// 按名字
	if got, err := s.FindContainer("alpha"); err != nil || got.ID != c1.ID {
		t.Fatalf("按名字查找失败: %v %+v", err, got)
	}
	// 按 ID 前缀：完整 ID 必命中；再验证存在某个非空前缀即可命中（前缀过短
	// 可能与另一容器歧义，属正确行为）
	if got, err := s.FindContainer(c2.ID); err != nil || got.ID != c2.ID {
		t.Fatalf("完整 ID 查找失败: %v", err)
	}
	resolved := false
	for i := 1; i <= len(c2.ID); i++ {
		if got, err := s.FindContainer(c2.ID[:i]); err == nil {
			if got.ID != c2.ID {
				t.Fatalf("前缀 %s 命中了错误容器", c2.ID[:i])
			}
			resolved = true
			break
		}
	}
	if !resolved {
		t.Fatal("完整 ID 未能命中")
	}
	// 不存在
	if _, err := s.FindContainer("ghost"); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("期望 ErrContainerNotFound，实得 %v", err)
	}
}

func TestRuntimeStateRoundtrip(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	cfg := newCfg(t, s, "web")
	if err := s.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	// 未运行过：零值 + false
	st, ok, err := s.ReadRuntimeState(cfg.ID)
	if err != nil || ok || st.Running || st.ExitCode != -1 {
		t.Fatalf("初始状态错误: %+v %v %v", st, ok, err)
	}
	st.ShimPID, st.InitPID, st.Running, st.StartedAt = 1234, 1235, true, nowUTC()
	if err := s.WriteRuntimeState(cfg.ID, st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.ReadRuntimeState(cfg.ID)
	if err != nil || !ok || got.ShimPID != 1234 || !got.Running {
		t.Fatalf("回读不一致: %+v %v %v", got, ok, err)
	}
	// 未知容器
	if err := s.WriteRuntimeState("deadbeef0000", st); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("期望 ErrContainerNotFound，实得 %v", err)
	}
}

func TestStoppedByUserAndBootEligible(t *testing.T) {
	s := &Store{Root: t.TempDir()}

	// unless-stopped + 标记 → 不自启
	cfg := newCfg(t, s, "web")
	cfg.Restart = RestartUnlessStoped
	if err := s.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if !s.BootEligible(cfg) {
		t.Fatal("无标记时 unless-stopped 应自启")
	}
	if err := s.MarkStoppedByUser(cfg.ID); err != nil {
		t.Fatal(err)
	}
	if !s.IsStoppedByUser(cfg.ID) || s.BootEligible(cfg) {
		t.Fatal("有标记时 unless-stopped 不应自启")
	}
	if err := s.ClearStoppedByUser(cfg.ID); err != nil {
		t.Fatal(err)
	}
	if s.IsStoppedByUser(cfg.ID) || !s.BootEligible(cfg) {
		t.Fatal("清除标记后应恢复自启")
	}

	// always + 标记 → 仍自启（Docker 语义）
	cfg2 := newCfg(t, s, "db")
	cfg2.Restart = RestartAlways
	if err := s.CreateContainer(cfg2); err != nil {
		t.Fatal(err)
	}
	_ = s.MarkStoppedByUser(cfg2.ID)
	if !s.BootEligible(cfg2) {
		t.Fatal("always 应无视停止标记")
	}

	// no / on-failure → 永不自启
	for _, r := range []Restart{RestartNo, RestartOnFailure} {
		c := newCfg(t, s, "x"+string(r))
		c.Restart = r
		if err := s.CreateContainer(c); err != nil {
			t.Fatal(err)
		}
		if s.BootEligible(c) {
			t.Fatalf("%s 不应自启", r)
		}
	}
}

func TestListContainersSkipsCorrupt(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	good := newCfg(t, s, "good")
	if err := s.CreateContainer(good); err != nil {
		t.Fatal(err)
	}
	badDir := s.ContainerDir("bad000000000")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "config.json"), []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListContainers()
	if err != nil {
		t.Fatalf("损坏目录不应导致整体失败: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("期望仅 good: %+v", list)
	}
	// FindContainer 对损坏目录：跳过；对坏 JSON 报 not found 是合法语义
	if _, err := s.FindContainer("bad000000000"); !errors.Is(err, ErrContainerNotFound) {
		t.Fatalf("损坏目录 Find 期望 not found，实得 %v", err)
	}
}

func TestContainerIDFormat(t *testing.T) {
	id, err := NewContainerID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 12 || strings.ContainsAny(id, "ghijklmnopqrstuvwxyz") {
		t.Fatalf("ID 格式异常: %s", id)
	}
	id2, _ := NewContainerID()
	if id == id2 {
		t.Fatal("两次 ID 相同")
	}
}

// TestContainerNameConflictConcurrent 并发创建同名容器：必须只有一个成功，
// 其余 ErrContainerExists（修复名字唯一性 TOCTOU —— 并发 run 同名竞态）。
func TestContainerNameConflictConcurrent(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	const n = 8
	var wg sync.WaitGroup
	ok := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok <- s.CreateContainer(newCfg(t, s, "web"))
		}()
	}
	wg.Wait()
	close(ok)
	success := 0
	for err := range ok {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrContainerExists) {
			t.Fatalf("非重名错误: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("并发同名期望恰好 1 个成功，实得 %d", success)
	}
	// 名字锁仍占用，改不同名可建。
	if err := s.CreateContainer(newCfg(t, s, "web2")); err != nil {
		t.Fatalf("不同名仍报错: %v", err)
	}
	// 删掉 web 后可复用名字。
	all, _ := s.ListContainers()
	for _, c := range all {
		if c.Name == "web" {
			if err := s.RemoveContainer(c.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.CreateContainer(newCfg(t, s, "web")); err != nil {
		t.Fatalf("删除后不能复用名字: %v", err)
	}
}
