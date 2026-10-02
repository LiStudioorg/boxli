// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import "testing"

// TestRunLimitsRejectsUnimplemented 验证未实现的资源能力在 CLI 层显式拒绝。
func TestRunLimitsRejectsUnimplemented(t *testing.T) {
	if _, err := runLimits(0, 0, 0, 0, 0, "", 0, 512, "", 0, 0); err == nil {
		t.Error("--storage 应显式拒绝")
	}
	if _, err := runLimits(0, 0, 0, 0, 0, "", 0, 0, "10mbps", 0, 0); err == nil {
		t.Error("--network-bandwidth 应显式拒绝")
	}
	if _, err := runLimits(0, 0, 0, 0, 0, "", 0, 0, "", 1, 0); err == nil {
		t.Error("--gpu 应显式拒绝")
	}
	if _, err := runLimits(0, 0, 0, 0, 0, "", 0, 0, "", 0, 1); err == nil {
		t.Error("--npu 应显式拒绝")
	}
	l, err := runLimits(256, 0, 0, 1, 128, "", 0, 0, "", 0, 0)
	if err != nil {
		t.Fatalf("常规限制不应报错: %v", err)
	}
	if l.Memory != 256<<20 || l.PidsLimit != 128 {
		t.Fatalf("常规限制解析错误: %+v", l)
	}
}
