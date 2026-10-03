// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client 是 hub 的 HTTP 客户端，作为 `licore login/pull/push/search` 的底层。
type Client struct {
	BaseURL string
	Token   string
	http    *http.Client
}

// NewClient 返回指向 baseURL 的客户端。
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 60 * time.Second}}
}

// Login 用用户名/口令换取令牌并缓存到客户端。
func (c *Client) Login(username, password string) error {
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	resp, err := c.http.Post(c.BaseURL+"/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("登录失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK {
		return interr(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("解析登录响应: %w", err)
	}
	c.Token = out.Token
	return nil
}

// Push 把本地 .licore 文件上传为 name:version 的镜像：
// 先上传其全部 blob（此处整文件作为一个 manifest blob：sha256 寻址），再打 tag。
func (c *Client) Push(ref, filePath string) error {
	name, version, err := splitRefPath(ref)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("读取镜像文件: %w", err)
	}
	digest, err := digestBytes(data)
	if err != nil {
		return err
	}
	if err := c.uploadBlob(digest, data); err != nil {
		return err
	}
	return c.setTag(name, version, digest)
}

// Pull 把 name:version 镜像下载到 dstPath。
func (c *Client) Pull(ref, dstPath string) error {
	name, version, err := splitRefPath(ref)
	if err != nil {
		return err
	}
	digest, err := c.resolveTag(name, version)
	if err != nil {
		return err
	}
	data, err := c.downloadBlob(digest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("创建输出目录: %w", err)
	}
	if err := os.WriteFile(dstPath, data, 0o644); err != nil {
		return fmt.Errorf("写镜像文件: %w", err)
	}
	return nil
}

// Search 按关键字搜索镜像。
func (c *Client) Search(query string) ([]SearchResult, error) {
	var out struct {
		Total   int            `json:"total"`
		Results []SearchResult `json:"results"`
	}
	resp, err := c.get("/search?q=" + url.QueryEscape(query))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, interr(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析搜索响应: %w", err)
	}
	return out.Results, nil
}

// ------------------ 内部 ------------------

func (c *Client) get(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return c.http.Do(req)
}

func (c *Client) uploadBlob(digest string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, c.BaseURL+"/blobs/"+digest, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("上传 blob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return interr(resp)
	}
	return nil
}

func (c *Client) downloadBlob(digest string) ([]byte, error) {
	resp, err := c.get("/blobs/" + digest)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, interr(resp)
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) setTag(name, version, digest string) error {
	body, _ := json.Marshal(map[string]string{"digest": digest})
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/tags/"+name+"/"+version, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("打 tag: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return interr(resp)
	}
	return nil
}

func (c *Client) resolveTag(name, version string) (string, error) {
	resp, err := c.get("/tags/" + name + "/" + version)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", interr(resp)
	}
	var rec TagRecord
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return "", fmt.Errorf("解析 tag 响应: %w", err)
	}
	return rec.Digest, nil
}

// interr 从非 2xx 响应里提取错误。
func interr(resp *http.Response) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("hub %s: %s", resp.Status, e.Error)
	}
	return fmt.Errorf("hub %s", resp.Status)
}

func splitRefPath(ref string) (name, version string, err error) {
	i := lastColon(ref)
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("镜像引用 %q 应为 name:version", ref)
	}
	return ref[:i], ref[i+1:], nil
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// digestBytes 返回字节内容的 sha256 十六进制。
func digestBytes(data []byte) (string, error) {
	h := sha256.New()
	if _, err := h.Write(data); err != nil {
		return "", fmt.Errorf("计算摘要: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
