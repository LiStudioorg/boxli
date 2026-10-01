// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"testing"
)

func TestRootRegistersModuleCommands(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{
		"compose", "dev", "build", "doctor", "lint", "scaffold",
		"tag", "commit", "save", "load", "export", "import",
		"images", "run", "ps", "pull",
	} {
		if !names[want] {
			t.Errorf("root 缺少命令 %q", want)
		}
	}
}

func TestCompletionCommand(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	root.SetArgs([]string{"completion", "bash"})
	if err := root.Execute(); err != nil {
		t.Fatalf("completion bash 失败: %v", err)
	}
	if out.Len() == 0 {
		t.Error("completion bash 未输出")
	}
}
