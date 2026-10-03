// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LiStudioorg/licore/hub"
)

// 默认 Hub 地址：优先 --hub 标志，其次 $BOXLI_HUB，最后本地开发默认地址。
const defaultHubURL = "http://127.0.0.1:3727"

// hubBaseURL 解析 Hub 服务端地址。
func hubBaseURL(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("BOXLI_HUB"); v != "" {
		return v
	}
	return defaultHubURL
}

// hubDataRoot 返回数据目录默认根（$BOXLI_HOME → ~/.boxli），与 store.Open 一致。
func hubDataRoot(dataDir string) string {
	if dataDir != "" {
		return dataDir
	}
	if v := os.Getenv("BOXLI_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".boxli")
}

// hubAuthFile 返回 Hub 凭证文件路径（令牌绑定到具体 Hub 地址）。
func hubAuthFile(dataDir, baseURL string) string {
	return filepath.Join(hubDataRoot(dataDir), "hub", "auth.json")
}

// hubAuth 是一次已保存的 Hub 凭证。
type hubAuth struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// loadHubToken 读取已保存的 Hub 令牌；未登录或地址不符时返回空令牌。
func loadHubToken(dataDir, baseURL string) string {
	data, err := os.ReadFile(hubAuthFile(dataDir, baseURL))
	if err != nil {
		return ""
	}
	var a hubAuth
	if json.Unmarshal(data, &a) != nil || a.URL != baseURL {
		return ""
	}
	return a.Token
}

// saveHubToken 把 Hub 令牌落盘（一次性写入 + rename 原子替换，权限 0600）。
func saveHubToken(dataDir, baseURL, token string) error {
	dir := filepath.Dir(hubAuthFile(dataDir, baseURL))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 hub 凭证目录: %w", err)
	}
	a := hubAuth{URL: baseURL, Token: token}
	data, err := json.Marshal(&a)
	if err != nil {
		return fmt.Errorf("编码 hub 凭证: %w", err)
	}
	path := hubAuthFile(dataDir, baseURL)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写 hub 凭证: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落位 hub 凭证: %w", err)
	}
	return nil
}

// newHubClient 构造指向 baseURL 的客户端，并尽可能带上已保存令牌。
func newHubClient(baseURL, dataDir string) *hub.Client {
	c := hub.NewClient(baseURL)
	c.Token = loadHubToken(dataDir, baseURL)
	return c
}
