// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootRegistersHub(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "hub" {
			found = true
			serves := false
			for _, sub := range c.Commands() {
				if sub.Name() == "serve" {
					serves = true
					for _, want := range []string{"storage", "port", "data-dir"} {
						if sub.Flags().Lookup(want) == nil {
							t.Errorf("hub serve 缺少 flag %q", want)
						}
					}
				}
			}
			if !serves {
				t.Error("hub 缺 serve 子命令")
			}
		}
	}
	if !found {
		t.Error("root 缺少 hub 命令")
	}
}

func TestHubServeInvalidStorage(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	root.SetArgs([]string{"hub", "serve", "--storage", "nope", "--data-dir", "/tmp/x"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "非法 --storage") {
		t.Fatalf("非法 storage 应报错: %v", err)
	}
}

func TestHubServeS3Unimplemented(t *testing.T) {
	var out bytes.Buffer
	root := NewRootCommand(&out, &out)
	root.SetArgs([]string{"hub", "serve", "--storage", "s3", "--data-dir", "/tmp/x"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "尚未实现") {
		t.Fatalf("s3 应明确报未实现: %v", err)
	}
}
