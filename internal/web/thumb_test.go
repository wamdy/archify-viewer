package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"archify-viewer/internal/thumbs"
)

type countingCapturer struct {
	calls int32
	url   string
	err   error
}

func (c *countingCapturer) Capture(ctx context.Context, url, theme string) ([]byte, error) {
	atomic.AddInt32(&c.calls, 1)
	c.url = url
	if c.err != nil {
		return nil, c.err
	}
	// 结构合法的 PNG 头（魔数 + IHDR + 1280x800），web 层不做像素解码。
	b := make([]byte, 33)
	copy(b, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	b[8], b[9], b[10], b[11] = 0, 0, 0, 13
	copy(b[12:16], "IHDR")
	b[19], b[23] = 1, 1
	b[18], b[22] = 5, 3 // 1280 = 0x0500, 800 = 0x0320
	b[17], b[21] = 0, 2
	return b, nil
}

func newThumbServer(t *testing.T, cap thumbs.Capturer) (*Server, string, string) {
	t.Helper()
	cfg, idx, store, _ := buildFixture(t)
	dir := t.TempDir()
	st := thumbs.NewStore(dir, cap, nil)
	srv, err := New(cfg, idx, store, ThumbsOption(st))
	if err != nil {
		t.Fatal(err)
	}
	srv.url = "http://127.0.0.1:1/"
	return srv, idx.Artifacts[0].ID, dir
}

func TestThumbServesPNGAndCaches(t *testing.T) {
	capt := &countingCapturer{}
	srv, id, dir := newThumbServer(t, capt)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if x := rec.Header().Get("X-Content-Type-Options"); x != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", x)
	}
	if !strings.Contains(capt.url, "/s/"+id) {
		t.Errorf("采集 URL 应指向截图派生视图 /s/{id}，实际 %q", capt.url)
	}

	// 二次请求命中缓存，不再采集。
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
	if got := atomic.LoadInt32(&capt.calls); got != 1 {
		t.Errorf("二次请求应命中缓存，calls=%d", got)
	}
	if rec2.Body.String() != rec.Body.String() {
		t.Error("两次响应字节不一致")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.png")); len(files) != 1 {
		t.Errorf("缓存目录应只有 1 个 PNG，实际 %v", files)
	}
}

func TestThumbRejectsUnknownAndBadIDs(t *testing.T) {
	srv, _, _ := newThumbServer(t, &countingCapturer{})
	for _, path := range []string{
		"/t/000000000000",
		"/t/../../../etc/passwd",
		"/t/..%2F..%2Fsecret",
		"/t/short",
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d，应为 404/301/400", path, rec.Code)
		}
	}
}

func TestThumbUnavailableWithoutStore(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store) // 未装配 thumbs
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+idx.Artifacts[0].ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "thumb_unavailable") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestThumbErrorCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{thumbs.ErrUnavailable, "thumb_unavailable"},
		{thumbs.ErrFailed, "thumb_failed"},
		{errors.New("boom"), "thumb_failed"},
	}
	for _, c := range cases {
		srv, id, _ := newThumbServer(t, &countingCapturer{err: c.err})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%v: status = %d", c.err, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%v: body = %s, want %s", c.err, rec.Body.String(), c.code)
		}
	}
}

func TestRescanDropsStaleThumbs(t *testing.T) {
	srv, id, dir := newThumbServer(t, &countingCapturer{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	stale := filepath.Join(dir, "deadbeef0000-1-1.png")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("Rescan 后陈旧缓存应被清理")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.png")); len(files) != 1 {
		t.Errorf("当前产物的缓存应保留，实际 %v", files)
	}
}

func TestRebuildThumbsDeletesAndRequeues(t *testing.T) {
	capt := &countingCapturer{}
	srv, id, dir := newThumbServer(t, capt)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("首次生成 status = %d", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/api/thumbs/rebuild", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("rebuild status = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	var out struct {
		Removed int `json:"removed"`
		Queued  int `json:"queued"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Removed != 1 || out.Queued != 2 {
		t.Fatalf("rebuild = %+v, want removed=1 queued=2（亮/暗两张）", out)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
		tmp, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
		if len(files) >= 2 && len(tmp) == 0 {
			time.Sleep(50 * time.Millisecond) // 让 goroutine 收尾，避免 TempDir 清理竞态
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("重建后缓存未恢复")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&capt.calls); got < 3 {
		t.Errorf("重建应补齐亮/暗两张，calls=%d", got)
	}
}

func TestRebuildUnavailableWithoutStore(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/thumbs/rebuild", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRebuildSingleThumbOnlyTouchesThatOne(t *testing.T) {
	capt := &countingCapturer{}
	srv, id, dir := newThumbServer(t, capt)
	// 先让唯一一件生成好
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/t/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("首次生成 status = %d", rec.Code)
	}

	body := `{"id":"` + id + `"}`
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/thumbs/rebuild", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec2.Code, rec2.Body.String())
	}
	var out struct {
		Removed int `json:"removed"`
		Queued  int `json:"queued"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Removed != 1 || out.Queued != 2 {
		t.Fatalf("single rebuild = %+v, want removed=1 queued=2（亮/暗两张）", out)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
		tmp, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
		if len(files) >= 2 && len(tmp) == 0 {
			time.Sleep(50 * time.Millisecond)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("单件重建后缓存未恢复")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 未知 id → 404
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodPost, "/api/thumbs/rebuild", strings.NewReader(`{"id":"000000000000"}`))
	req3.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d", rec3.Code)
	}
}
