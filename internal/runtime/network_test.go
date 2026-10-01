// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package runtime

import (
	"testing"

	"github.com/LiStudioorg/boxli/internal/network"
)

func TestNetEnvRoundTrip(t *testing.T) {
	env := NetEnv(network.ModeBridge, "abc123def456", "boxli0", "172.18.0.2", "172.18.0.1", "web", 16)
	mode, cid, name, ip, gw, host, prefix, ok := parseNetEnv(env)
	if !ok {
		t.Fatal("解析失败")
	}
	if *mode != network.ModeBridge || cid != "abc123def456" || name != "boxli0" ||
		ip != "172.18.0.2" || gw != "172.18.0.1" || host != "web" || prefix != 16 {
		t.Fatalf("回读不一致: mode=%s cid=%s name=%s ip=%s gw=%s host=%s prefix=%d",
			*mode, cid, name, ip, gw, host, prefix)
	}
}

func TestNetEnvHostNone(t *testing.T) {
	for _, mode := range []network.NetMode{network.ModeHost, network.ModeNone} {
		env := NetEnv(mode, "cid1", "", "", "", "web", 0)
		m, cid, _, _, _, host, _, ok := parseNetEnv(env)
		if !ok || *m != mode || cid != "cid1" || host != "web" {
			t.Fatalf("模式 %s 回读不一致: mode=%v cid=%s host=%s ok=%v", mode, *m, cid, host, ok)
		}
	}
}

func TestParseNetEnvMissing(t *testing.T) {
	if _, _, _, _, _, _, _, ok := parseNetEnv([]string{"FOO=1", "BAR=2"}); ok {
		t.Fatal("无明显 MODE 的环境不应解析成功")
	}
}
