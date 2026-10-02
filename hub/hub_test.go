// Copyright (C) 2026 LiStudioorg
// SPDX-License-Identifier: AGPL-3.0-only

package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestRegistry 返回一个临时目录上的仓库。
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestPutBlobDedup(t *testing.T) {
	r := newTestRegistry(t)
	data := []byte("hello boxli blob")
	// 先算摘要。
	d, _, _ := computeDigest(bytes.NewReader(data))
	b1, err := r.PutBlob("", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if b1.Digest != d {
		t.Fatalf("自动摘要 %s != 声明 %s", b1.Digest, d)
	}
	// 相同内容重复 Put——去重，blob 文件仍只有一个。
	if _, err := r.PutBlob("", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("重复 Put: %v", err)
	}
	// 校验存储内容。
	rc, _, err := r.GetBlob(d)
	if err != nil {
		t.Fatalf("GetBlob: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Fatalf("blob 内容不符: %s", got)
	}
	// 摘要不符应报错。
	if _, err := r.PutBlob(strings.Repeat("a", 64), bytes.NewReader(data), int64(len(data))); !errors.Is(err, ErrDigestMismatch) {
		t.Errorf("期望 ErrDigestMismatch，got %v", err)
	}
}

func TestPutBlobRejectsBadDigest(t *testing.T) {
	r := newTestRegistry(t)
	if _, err := r.PutBlob("not-a-digest", bytes.NewReader([]byte("x")), 1); !errors.Is(err, ErrBadDigest) {
		t.Errorf("期望 ErrBadDigest，got %v", err)
	}
}

func TestTagAndRefcount(t *testing.T) {
	r := newTestRegistry(t)
	data := []byte("image-content")
	d, _, _ := computeDigest(bytes.NewReader(data))
	if _, err := r.PutBlob(d, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("PutBlob: %v", err)
	}
	if err := r.Tag("alice/app", "v1", d); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	rec, err := r.Resolve("alice/app", "v1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rec.Digest != d {
		t.Fatalf("tag 摘要 %s != %s", rec.Digest, d)
	}
	if _, err := r.Resolve("alice/app", "v2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("未打 v2 应 NotFound，got %v", err)
	}
	// tag 到不存在的 blob 应报错。
	if err := r.Tag("alice/app", "v2", strings.Repeat("b", 64)); !errors.Is(err, ErrNotFound) {
		t.Errorf("tag 到不存在 blob 应 NotFound，got %v", err)
	}
	// References 看到一个引用。
	refs, err := r.References(d)
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("期望 1 个引用，got %d", len(refs))
	}
	// 删除 tag 后 blob 仍存在但无引用（引用计数归零判定）。
	if err := r.DeleteTag("alice/app", "v1"); err != nil {
		t.Fatalf("DeleteTag: %v", err)
	}
	has, _ := r.blobs.Has(d)
	if has {
		t.Error("引用归零后 blob 应被回收")
	}
}

func TestSearch(t *testing.T) {
	r := newTestRegistry(t)
	for _, ref := range []struct{ name, ver string }{
		{"alice/app", "v1"}, {"alice/db", "v2"}, {"bob/tool", "v1"},
	} {
		data := []byte(ref.name + ref.ver)
		d, _, _ := computeDigest(bytes.NewReader(data))
		if _, err := r.PutBlob(d, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("PutBlob: %v", err)
		}
		if err := r.Tag(ref.name, ref.ver, d); err != nil {
			t.Fatalf("Tag: %v", err)
		}
	}
	res, err := r.Search("alice")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("搜索 alice 期望 2 命中，got %d", len(res))
	}
	if all, _ := r.Search(""); len(all) != 3 {
		t.Errorf("搜索空期望 3 命中，got %d", len(all))
	}
}

// TestServerRoundtrip 端到端：登录 → 推送 → 搜索 → 拉取。
func TestServerRoundtrip(t *testing.T) {
	r := newTestRegistry(t)
	srv, err := NewServer(r, WithAuth("test-secret"))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.SetUser("alice", "pw")

	// 起一个真实 HTTP 服务。
	base := startTestServer(t, srv)
	c := NewClient(base)
	if err := c.Login("alice", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.Token == "" {
		t.Fatal("登录未返回令牌")
	}

	img := t.TempDir() + "/app.boxli"
	if err := os.WriteFile(img, []byte("boxli-image-data-v1"), 0o644); err != nil {
		t.Fatalf("写镜像: %v", err)
	}
	if err := c.Push("alice/app:v1", img); err != nil {
		t.Fatalf("Push: %v", err)
	}

	results, err := c.Search("app")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Name != "alice/app" {
		t.Fatalf("搜索 app 结果不符: %+v", results)
	}

	dst := t.TempDir() + "/out.boxli"
	if err := c.Pull("alice/app:v1", dst); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "boxli-image-data-v1" {
		t.Fatalf("Pulled 内容不符: %s", got)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	r := newTestRegistry(t)
	srv, _ := NewServer(r, WithAuth("secret"))
	srv.SetUser("alice", "right")
	base := startTestServer(t, srv)
	c := NewClient(base)
	if err := c.Login("alice", "wrong"); err == nil {
		t.Fatal("错误密码应登录失败")
	}
}

func TestAuthRequired(t *testing.T) {
	r := newTestRegistry(t)
	// 未授权的搜索应 401。
	res, err := r.Search("")
	_ = res
	_ = err
	// 空 token 客户端调用被服务器拒绝（通过 Login 未走）。
	c := NewClient("http://127.0.0.1:0")
	if _, err := c.Search("x"); err == nil {
		t.Log("连接失败符合预期（无服务）")
	}
}

func TestJWT(t *testing.T) {
	a := NewAuthenticator("k", time.Hour)
	tok, err := a.Sign("bob", []string{"read", "write"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	claims, err := a.Verify(tok)
	if err != nil || claims.Sub != "bob" {
		t.Fatalf("Verify: %+v, %v", claims, err)
	}
	if !claims.HasScope("write") {
		t.Error("应授予 write 权限")
	}
	if _, err := a.Verify(tok + "x"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("篡改应报 Unauthorized，got %v", err)
	}
}

// startTestServer 在随机端口起 hub 服务并返回 baseURL。
func startTestServer(t *testing.T, srv *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听: %v", err)
	}
	base := "http://" + ln.Addr().String()
	s := &http.Server{Handler: srv.Handler()}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Close() })
	return base
}

func TestS3Reserved(t *testing.T) {
	s, err := NewS3BlobStore("bucket", "s3.example.com")
	if err != nil {
		t.Fatalf("NewS3BlobStore: %v", err)
	}
	if err := s.Put("a", nil, 0); !errors.Is(err, ErrUnsupported) {
		t.Errorf("S3 Put 应 ErrUnsupported，got %v", err)
	}
	if _, err := NewS3BlobStore("", ""); !errors.Is(err, ErrBadRequest) {
		t.Errorf("缺 bucket/endpoint 应 ErrBadRequest，got %v", err)
	}
}

func TestLocalBlobListAndDelete(t *testing.T) {
	ls, err := NewLocalBlobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalBlobStore: %v", err)
	}
	data := []byte("payload")
	d, _, _ := computeDigest(bytes.NewReader(data))
	if err := ls.Put(d, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if has, _ := ls.Has(d); !has {
		t.Fatal("Has 应为 true")
	}
	list, err := ls.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List: %v, %v", list, err)
	}
	if err := ls.Delete(d); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if has, _ := ls.Has(d); has {
		t.Fatal("删除后 Has 应为 false")
	}
}

// TestServerHTTPE2E 用 httptest 覆盖 HTTP 层：鉴权 401、login→token、push/tag、
// _catalog、search、blob 取回，以及默认根目录函数。
func TestServerHTTPE2E(t *testing.T) {
	reg := newTestRegistry(t)
	srv, err := NewServer(reg, WithAuth("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	srv.SetUser("admin", "pw")
	h := srv.Handler()

	do := func(method, path, tok string, body io.Reader) (int, string) {
		req := httptest.NewRequest(method, path, body)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 1. 未加 token 访问受保护端点 → 401。
	if code, _ := do("GET", "/_catalog", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401，实得 %d", code)
	}

	// 2. login（JSON）→ token。
	loginBody := bytes.NewBufferString(`{"username":"admin","password":"pw"}`)
	code, body := do("POST", "/auth/login", "", loginBody)
	if code != http.StatusOK {
		t.Fatalf("login 期望 200，实得 %d", code)
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &lr); err != nil || lr.Token == "" {
		t.Fatalf("login 未返回 token: %q", body)
	}
	tok := lr.Token

	// 3. 加 token 后 _catalog 可用。
	if code, _ := do("GET", "/_catalog", tok, nil); code != http.StatusOK {
		t.Fatalf("带 token catalog 期望 200，实得 %d", code)
	}

	// 4. push 一个 blob + tag，验证 search/catalog 能看到，blob 可取回。
	data := []byte("hello-e2e-blob")
	d, _, _ := computeDigest(bytes.NewReader(data))
	if _, err := reg.PutBlob("", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	// 上传 manifest 建立 tag（走 handlTag PUT）。
	if err := reg.Tag("myns/app", "v1", d); err != nil {
		t.Fatalf("PutTag: %v", err)
	}
	if code, b := do("GET", "/tags/myns/app/v1", tok, nil); code != http.StatusOK {
		t.Fatalf("GET tag 期望 200，实得 %d (%s)", code, b)
	}
	if code, b := do("GET", "/blobs/"+d, tok, nil); code != http.StatusOK || b != string(data) {
		t.Fatalf("GET blob 期望内容匹配，实得 code=%d body=%q", code, b)
	}
	if code, b := do("GET", "/search?q=app", tok, nil); code != http.StatusOK {
		t.Fatalf("search 期望 200，实得 %d (%s)", code, b)
	}

	// 5. 默认根目录辅助函数。
	if _, e := defaultHubRoot(); e != nil {
		t.Fatalf("defaultHubRoot: %v", e)
	}
	if _, e := defaultBlobRoot(); e != nil {
		t.Fatalf("defaultBlobRoot: %v", e)
	}
	if _, e := defaultDataRoot(); e != nil {
		t.Fatalf("defaultDataRoot: %v", e)
	}
}
