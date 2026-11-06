package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archify-viewer/internal/config"
	"archify-viewer/internal/index"
)

func buildFixture(t *testing.T) (*config.Config, *index.Index, *index.Store, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "architecture-under-test-20261106-120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	html := "<!doctype html><title>机密图纸</title>"
	if err := os.WriteFile(filepath.Join(dir, "under-test.html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "candidate.json"), []byte(`{"meta":{"title":"机密图纸"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootSource: "test", Roots: []config.Root{{Label: "fix", Path: root}}, GUI: config.GUI{Addr: config.DefaultGUIAddr}}
	idx, store := index.Scan(cfg)
	if len(idx.Artifacts) != 1 {
		t.Fatalf("fixture 没造出产物：%+v", idx.Artifacts)
	}
	return cfg, idx, store, html
}

func newServer(t *testing.T) (*Server, *index.Index, *index.Store, string) {
	t.Helper()
	cfg, idx, store, html := buildFixture(t)
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	return srv, idx, store, html
}

func TestAPIIndexNoStoreAndNoAbsolutePaths(t *testing.T) {
	srv, idx, _, _ := newServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/index", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if body["artifacts"] == nil || body["by_type"] == nil || body["roots"] == nil {
		t.Errorf("响应形状缺字段：%v", body)
	}
	for _, abs := range []string{idx.Roots[0].Path, filepath.VolumeName(idx.Roots[0].Path)} {
		if abs != "" && strings.Contains(rec.Body.String(), abs) {
			t.Errorf("绝对路径泄进响应：%s", abs)
		}
	}
}

func TestServeArtifactByIDServesOriginalBytes(t *testing.T) {
	srv, idx, _, html := newServer(t)
	id := idx.Artifacts[0].ID

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != html {
		t.Error("产物字节被改动（红线 1）")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestUnknownAndTraversalIDsAreRefused(t *testing.T) {
	srv, _, _, _ := newServer(t)
	// 期望：要么 404/400，要么被 ServeMux 规范化后 301 到 /a/（该路径不在白名单里，最终 404）。
	// 任何一支都不得回吐产物内容——红线 3 的落实方式是「只认 ID」，不是过滤穿越串。
	cases := []string{
		"/a/deadbeef123",
		"/a/000000000000",
		"/a/..",
		"/a/%2e%2e",
		"/a/%2e%2e%2f%2e%2e%2fconfig.yaml",
		"/a/..%5c..%5cwindows%5cwin.ini",
		"/a/%3Cscript%3E",
	}
	for _, path := range cases {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		switch rec.Code {
		case http.StatusOK:
			t.Errorf("%s 竟然 200：%s", path, rec.Body.String())
		case http.StatusNotFound, http.StatusBadRequest, http.StatusMovedPermanently:
		default:
			t.Errorf("%s 期望 404/400/301，得到 %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "机密图纸</title>") {
			t.Errorf("%s 回吐了产物内容", path)
		}
	}
	// 301 的落地目标本身也必须是拒绝态。
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/", nil))
	if rec.Code == http.StatusOK {
		t.Error("/a/ 不该被服务")
	}
}

func TestStaticAssetsWhitelist(t *testing.T) {
	srv, _, _, _ := newServer(t)
	for path, wantCT := range map[string]string{
		"/":           "text/html",
		"/index.html": "text/html",
		"/app.js":     "text/javascript",
		"/style.css":  "text/css",
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, rec.Code)
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), wantCT) {
			t.Errorf("%s Content-Type = %q，期望前缀 %q", path, rec.Header().Get("Content-Type"), wantCT)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s 缺 no-store", path)
		}
	}
	// 不提供目录列举、不提供任意路径透传。
	for _, path := range []string{"/static", "/static/", "/../config.yaml", "/config.yaml", "/internal/config/config.go", "/nonexistent.js"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s 不该被服务（200）", path)
		}
	}
}

func TestRescanPicksUpNewArtifact(t *testing.T) {
	cfg, root := func() (*config.Config, string) {
		dir := t.TempDir()
		c := &config.Config{RootSource: "test", Roots: []config.Root{{Label: "fix", Path: dir}}, GUI: config.GUI{Addr: config.DefaultGUIAddr}}
		return c, dir
	}()
	idx, store := index.Scan(cfg)
	if len(idx.Artifacts) != 0 {
		t.Fatalf("起点应为空库：%+v", idx.Artifacts)
	}
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟一次 archify 交付。
	dir := filepath.Join(root, "tree-late-20261106-130000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "late.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/index?rescan=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rescan status = %d", rec.Code)
	}
	var body index.Index
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Artifacts) != 1 || body.Artifacts[0].Slug != "late" {
		t.Fatalf("重扫后未看到新产物：%+v", body.Artifacts)
	}
	// 新产物必须立即可按 ID 取到。
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/a/"+body.Artifacts[0].ID, nil))
	if rec2.Code != http.StatusOK {
		t.Errorf("新产物 ID 未取得：/a/%s → %d", body.Artifacts[0].ID, rec2.Code)
	}
}

func TestBindRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.20:0", "example.com:8080"} {
		ln, err := Bind(addr)
		if err == nil {
			ln.Close()
			t.Errorf("Bind(%q) 必须拒绝启动（design D-3），却绑上了 %s", addr, ln.Addr())
		}
	}
	ln, err := Bind("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if host, _, err := net.SplitHostPort(ln.Addr().String()); err != nil || !config.IsLoopbackHost(host) {
		t.Errorf("实际监听 %s 不是回环", ln.Addr())
	}
}

func TestBindOccupiedPortFailsExplicitly(t *testing.T) {
	ln, err := Bind("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	second, err := Bind(addr)
	if err == nil {
		second.Close()
		t.Fatalf("端口 %s 被占用时第二次 Bind 必须报错，不得静默换端口", addr)
	}
	if !strings.Contains(err.Error(), "占用") {
		t.Errorf("错误信息应指路「去掉端口改用空闲端口」，得到：%v", err)
	}
}

func TestServerStartAndURL(t *testing.T) {
	cfg, idx, store, html := buildFixture(t)
	cfg.GUI.Addr = "127.0.0.1:0"
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown(context.Background())

	url := srv.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("URL = %s", url)
	}
	// 实际端口只能由服务端回报（默认 0 = 随机空闲），前端/调用方不得猜测。
	if strings.HasSuffix(url, ":0/") {
		t.Errorf("URL 里还留着占位端口：%s", url)
	}

	resp, err := http.Get(url + "api/index")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/index → %d", resp.StatusCode)
	}

	artResp, err := http.Get(url + "a/" + idx.Artifacts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer artResp.Body.Close()
	buf, err := io.ReadAll(artResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != html {
		t.Error("真实请求拿到的产物字节与磁盘不一致")
	}
}

func TestServerStartRefusesNonLoopbackConfig(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	cfg.GUI.Addr = "0.0.0.0:0"
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err == nil {
		srv.Shutdown(context.Background())
		t.Fatal("非回环配置必须启动失败")
	}
}
