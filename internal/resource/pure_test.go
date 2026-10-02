// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCPUWeight(t *testing.T) {
	if got := (&Limits{}).CPUWeight(); got != -1 {
		t.Fatalf("空 Limits 的 CPUWeight 应为 -1，实得 %d", got)
	}
	w := (&Limits{CPUShares: 1024}).CPUWeight()
	if w <= 0 || w > 10000 {
		t.Fatalf("CPUWeight(1024)=%d 越界", w)
	}
}

func TestSwapLimit(t *testing.T) {
	// Memory=256M, MemorySwap=512M → swap 上限 256M。
	l := &Limits{Memory: 256 << 20, MemorySwap: 512 << 20}
	if got := l.SwapLimit(); got != 256<<20 {
		t.Fatalf("SwapLimit=%d 期望 %d", got, 256<<20)
	}
	// MemorySwap=-1 → max（返回 -1 由调用方映射成 "max"）。
	if got := (&Limits{Memory: 64 << 20, MemorySwap: -1}).SwapLimit(); got != -1 {
		t.Fatalf("SwapLimit(-1)=%d 期望 -1", got)
	}
}

func TestFormatRateAndDuration(t *testing.T) {
	if got := FormatRate(1 << 30); got == "" {
		t.Error("FormatRate 空")
	}
	if got := FormatRate(0); got == "" {
		t.Error("FormatRate(0) 空")
	}
	if got := FormatDuration(0); got == "" {
		t.Error("FormatDuration(0) 空")
	}
	if got := FormatDuration(90 * time.Second); got == "" {
		t.Error("FormatDuration 空")
	}
}

// 未实现/需 root 的 cgroup 写入路径：未请求时应 nil，请求未实现项时应报错。
func TestUnimplementedResourceErrors(t *testing.T) {
	c := &Cgroup{}
	if err := c.writeNetworkBandwidth(0); err != nil {
		t.Fatalf("带宽 0 应 nil，实得 %v", err)
	}
	if err := c.writeNetworkBandwidth(1 << 20); err == nil {
		t.Fatal("带宽>0 应报错（未实现显式拒绝）")
	}
	if err := c.writeStorageAndDevices(&Limits{}); err != nil {
		t.Fatalf("空限制应 nil，实得 %v", err)
	}
	if err := c.writeStorageAndDevices(&Limits{Storage: 1 << 20}); err == nil {
		t.Fatal("--storage 应报错")
	}
	if err := c.writeStorageAndDevices(&Limits{GPU: []DeviceRequest{{Kind: "gpu", Count: 1}}}); err == nil {
		t.Fatal("--gpu 应报错")
	}
}

func TestErrUnsupportedWrap(t *testing.T) {
	c := &Cgroup{}
	err := c.writeNetworkBandwidth(1 << 10)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("应包装 ErrUnsupported，实得 %v", err)
	}
}

// TestEnableControllersReadOrMissing 验证 readOr 兜底解析（无 cgroup 时防御）。
func TestReadOrDefault(t *testing.T) {
	if got := readOr("def", filepath.Join(t.TempDir(), "nope")); got != "def" {
		t.Fatalf("readOr 默认值 %q", got)
	}
}
