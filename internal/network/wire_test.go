// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteDNSFilesToCreatesEtc 回归：FROM scratch 镜像没有 /etc，写
// resolv.conf/hosts 前必须 MkdirAll /etc，否则 init 报
// "写 /etc/resolv.conf: no such file or directory" 并退出。
func TestWriteDNSFilesToCreatesEtc(t *testing.T) {
	root := t.TempDir()
	if err := writeDNSFilesTo(root, "172.18.0.3", "172.18.0.1", "demo"); err != nil {
		t.Fatalf("writeDNSFilesTo: %v", err)
	}
	rc, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rc) != "nameserver 172.18.0.1\n" {
		t.Fatalf("resolv.conf= %q", rc)
	}
	hf, err := os.ReadFile(filepath.Join(root, "etc", "hosts"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(hf)
	if !strings.Contains(s, "172.18.0.3 demo") || !strings.Contains(s, "172.18.0.1 boxli-gw") {
		t.Fatalf("hosts 缺本机名/网关: %q", s)
	}
}

// TestWriteDNSFilesToOverwrites 验证可重跑（写现有文件不报错）。
func TestWriteDNSFilesToOverwrites(t *testing.T) {
	root := t.TempDir()
	if err := writeDNSFilesTo(root, "172.18.0.3", "172.18.0.1", "demo"); err != nil {
		t.Fatal(err)
	}
	if err := writeDNSFilesTo(root, "172.18.0.3", "172.18.0.1", "demo"); err != nil {
		t.Fatalf("二次调用: %v", err)
	}
}
