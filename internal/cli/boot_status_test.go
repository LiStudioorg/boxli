// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LiStudioorg/licore/internal/store"
)

func TestBootStatusOutput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := store.NewContainerID()
	cfg := &store.ContainerConfig{
		ConfigVersion: 1, ID: id, Name: "web", ImageRef: "demo:v1",
		Rootfs:  filepath.Join(st.ContainerDir(id), "rootfs"),
		Restart: store.RestartAlways, Cmd: []string{"/bin/sh"},
		CreatedAt: "2026-10-01T10:00:00Z",
	}
	if err := st.CreateContainer(cfg); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteRuntimeState(id, &store.RuntimeState{ExitCode: 3, RestartCount: 2}); err != nil {
		t.Fatal(err)
	}
	// no 策略容器不应出现在列表里
	id2, _ := store.NewContainerID()
	if err := st.CreateContainer(&store.ContainerConfig{
		ConfigVersion: 1, ID: id2, Name: "oneshot", ImageRef: "demo:v1",
		Rootfs:  filepath.Join(st.ContainerDir(id2), "rootfs"),
		Restart: store.RestartNo, Cmd: []string{"/bin/sh"},
		CreatedAt: "2026-10-01T10:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := newBootTestCommand(&buf)
	cmd.SetArgs([]string{"status", "--data-dir", root})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status 执行失败: %v\n%s", err, buf.String())
	}
	got := buf.String()
	for _, want := range []string{"开机自启：", "服务类型：", "服务文件：", "自启容器：", "web", "restart=always", "退出码 3", "累计重启 2 次"} {
		if !strings.Contains(got, want) {
			t.Errorf("status 输出缺少 %q：\n%s", want, got)
		}
	}
	if strings.Contains(got, "oneshot") {
		t.Errorf("no 策略容器不应出现在自启列表：\n%s", got)
	}
}

func TestBootTestNoContainers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	var buf bytes.Buffer
	cmd := newBootTestCommand(&buf)
	cmd.SetArgs([]string{"--data-dir", root})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "拉起 0，跳过 0，失败 0") {
		t.Fatalf("空目录 boot 输出错误: %s", buf.String())
	}
}
