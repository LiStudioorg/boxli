// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package resource

import (
	"errors"
	"strings"
	"testing"
)

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"1024", 1024}, {"1k", 1 << 10}, {"2mi", 2 << 20},
		{"1.5g", int64(1.5 * (1 << 30))}, {"-1", -1}, {"512kb", 512 << 10},
	}
	for _, c := range cases {
		got, err := ParseBytes(c.in)
		if err != nil {
			t.Errorf("ParseBytes(%q) err: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseBytes(%q)=%d, want %d", c.in, got, c.want)
		}
	}
	if _, err := ParseBytes("abc"); !errors.Is(err, ErrBadParam) {
		t.Errorf("非法单位应报 ErrBadParam, got %v", err)
	}
}

func TestParseBandwidth(t *testing.T) {
	if v, err := ParseBandwidth("1mbps"); err != nil || v != (1<<20)/8 {
		t.Errorf("ParseBandwidth(1mbps)=%d,%v", v, err)
	}
	if v, err := ParseBandwidth("10M"); err != nil || v != 10<<20 {
		t.Errorf("ParseBandwidth(10M)=%d,%v", v, err)
	}
	if _, err := ParseBandwidth("0"); !errors.Is(err, ErrBadParam) {
		t.Errorf("0 带宽应报错, got %v", err)
	}
}

func TestConvertMemorySwap(t *testing.T) {
	if v, _ := ConvertMemorySwap(0, 0); v != 0 {
		t.Errorf("swap 0 → 0, got %d", v)
	}
	if v, _ := ConvertMemorySwap(-1, 100); v != -1 {
		t.Errorf("swap -1 → -1, got %d", v)
	}
	if v, _ := ConvertMemorySwap(300, 100); v != 200 {
		t.Errorf("swap(300,memory100) → 200, got %d", v)
	}
	if _, err := ConvertMemorySwap(50, 100); !errors.Is(err, ErrBadParam) {
		t.Errorf("swap<mem 应报错, got %v", err)
	}
}

func TestSharesToWeightRoundtrip(t *testing.T) {
	for _, s := range []int64{2, 128, 1024, 262144} {
		w := SharesToWeight(s)
		if w < 1 || w > 10000 {
			t.Errorf("shares %d → weight %d 越界", s, w)
		}
		back := WeightToShares(w)
		if back > s*2+1024 { // 粗略：roundtrip 应在合理范围
			t.Errorf("roundtrip 失真过大: %d → %d → %d", s, w, back)
		}
	}
}

func TestBlkioToIOWeight(t *testing.T) {
	if BlkioToIOWeight(500) <= 0 {
		t.Error("blkio 500 应映射为正权重")
	}
	if BlkioToIOWeight(0) != 0 {
		t.Error("blkio 0 应映射为 0(不写)")
	}
}

func TestCPUQuota(t *testing.T) {
	l := &Limits{CPUs: 1}
	if q := l.CPUQuota(); q != 100000 {
		t.Errorf("1 cpu → quota %d, want 100000", q)
	}
	l2 := &Limits{}
	if q := l2.CPUQuota(); q != -1 {
		t.Errorf("0 cpu 应返回 -1(max), got %d", q)
	}
}

func TestLimitsValidate(t *testing.T) {
	bad := []*Limits{
		{CPUs: -1},
		{CPUShares: 1},
		{Memory: -5},
		{OOMKillDisable: true}, // 无 memory 时禁用 oom 非法
		{BlkioWeight: 5},
		{NetworkBandwidth: -1},
	}
	for i, l := range bad {
		if err := l.Validate(); !errors.Is(err, ErrBadParam) {
			t.Errorf("case %d 应报 ErrBadParam, got %v", i, err)
		}
	}
	good := &Limits{CPUs: 0.5, Memory: 1 << 30, PidsLimit: 100, CPUSet: "0-3,7", BlkioWeight: 500}
	if err := good.Validate(); err != nil {
		t.Errorf("合法 Limits 应通过: %v", err)
	}
	if good.Empty() {
		t.Error("好 Limits 不应是空")
	}
	if empty := (&Limits{}).Empty(); !empty {
		t.Error("零值 Limits 应为空")
	}
}

func TestCPUSetValid(t *testing.T) {
	for _, ok := range []string{"0", "0-3", "0,2,4-7"} {
		if !CPUSetValid(ok) {
			t.Errorf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"a", "4-2", "0,", "0-"} {
		if CPUSetValid(bad) {
			t.Errorf("%q 应非法", bad)
		}
	}
}

func TestParseDeviceID(t *testing.T) {
	if _, err := ParseDeviceID("8:0"); err != nil {
		t.Errorf("major:minor 应合法: %v", err)
	}
	if _, err := ParseDeviceID("/dev/char/1:3"); err != nil {
		t.Errorf("/dev/char 应合法: %v", err)
	}
	if _, err := ParseDeviceID("garbage"); !errors.Is(err, ErrBadDevice) {
		t.Errorf("非法设备应报 ErrBadDevice, got %v", err)
	}
	if _, err := ParseDeviceID(""); !errors.Is(err, ErrBadDevice) {
		t.Errorf("空设备应报 ErrBadDevice, got %v", err)
	}
}

func TestFormatBytes(t *testing.T) {
	if !strings.Contains(FormatBytes(1<<30), "GiB") {
		t.Errorf("FormatBytes(1GiB) 应含 GiB: %s", FormatBytes(1<<30))
	}
	if FormatBytes(-1) != "unlimited" {
		t.Errorf("FormatBytes(-1) 应为 unlimited")
	}
}

func TestDeviceRequestValidate(t *testing.T) {
	r := &DeviceRequest{Kind: "gpu", Count: 1}
	if err := r.Validate(); err != nil {
		t.Errorf("合法 GPU 请求应通过: %v", err)
	}
	if err := (&DeviceRequest{Kind: "cuda", Count: 1}).Validate(); !errors.Is(err, ErrBadDevice) {
		t.Errorf("非法 kind 应报错, got %v", err)
	}
}
