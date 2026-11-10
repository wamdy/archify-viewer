package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenInBrowser(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	var opened string
	srv, err := New(cfg, idx, store, WithBrowserOpener(func(u string) error { opened = u; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	srv.url = "http://127.0.0.1:1/"

	id := idx.Artifacts[0].ID
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/open-browser", strings.NewReader(`{"id":"`+id+`"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.HasSuffix(opened, "/a/"+id) {
		t.Errorf("opened = %q", opened)
	}

	// 未知 id
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/open-browser", strings.NewReader(`{"id":"000000000000"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id status = %d", rec.Code)
	}
}

func TestOpenInBrowserUnavailable(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/open-browser", strings.NewReader(`{}`)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestOpenInBrowserFailure(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store, WithBrowserOpener(func(string) error { return errors.New("boom") }))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/open-browser", strings.NewReader(`{}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", rec.Code)
	}
}
