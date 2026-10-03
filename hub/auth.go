// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// JWTHeader 是 JWT 的头部（自研 HS256 子集）。
type JWTHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// JWTClaims 是 LiCore hub 令牌的声明。
type JWTClaims struct {
	Sub    string   `json:"sub"`              // 用户
	Iat    int64    `json:"iat"`              // 签发秒
	Exp    int64    `json:"exp"`              // 过期秒
	Scopes []string `json:"scopes,omitempty"` // 授予的权限（read/write/admin）
}

// Authenticator 以共享密钥签发与校验 JWT。
type Authenticator struct {
	secret []byte
	ttl    time.Duration
}

// NewAuthenticator 返回 HMAC-SHA256 JWT 签发器。
func NewAuthenticator(secret string, ttl time.Duration) *Authenticator {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Authenticator{secret: []byte(secret), ttl: ttl}
}

// Sign 为用户签发令牌。
func (a *Authenticator) Sign(user string, scopes []string) (string, error) {
	now := time.Now().Unix()
	hdr, err := json.Marshal(JWTHeader{Alg: "HS256", Typ: "JWT"})
	if err != nil {
		return "", fmt.Errorf("编码 JWT 头: %w", err)
	}
	claims, err := json.Marshal(JWTClaims{Sub: user, Iat: now, Exp: now + int64(a.ttl.Seconds()), Scopes: scopes})
	if err != nil {
		return "", fmt.Errorf("编码 JWT 声明: %w", err)
	}
	h := base64.RawURLEncoding.EncodeToString(hdr)
	p := base64.RawURLEncoding.EncodeToString(claims)
	sig := a.sign(h + "." + p)
	return h + "." + p + "." + sig, nil
}

// Verify 校验令牌并返回其声明。非法/过期返回 ErrUnauthorized。
func (a *Authenticator) Verify(token string) (*JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("JWT 段数非法: %w", ErrUnauthorized)
	}
	// 恒定时间比较签名，防时序侧信道。
	want := a.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return nil, fmt.Errorf("JWT 签名非法: %w", ErrUnauthorized)
	}
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("解码 JWT 声明: %w", ErrUnauthorized)
	}
	var c JWTClaims
	if err := json.Unmarshal(claimBytes, &c); err != nil {
		return nil, fmt.Errorf("解析 JWT 声明: %w", ErrUnauthorized)
	}
	if c.Exp > 0 && time.Now().Unix() > c.Exp {
		return nil, fmt.Errorf("JWT 已过期: %w", ErrUnauthorized)
	}
	return &c, nil
}

// HasScope 报告声明是否授予指定权限。
func (c *JWTClaims) HasScope(want string) bool {
	if c == nil {
		return false
	}
	for _, s := range c.Scopes {
		if s == "admin" || s == want {
			return true
		}
	}
	return false
}

func (a *Authenticator) sign(m string) string {
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(m))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
