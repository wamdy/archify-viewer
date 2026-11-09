package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"archify-viewer/internal/tags"
)

func newTagsServer(t *testing.T) (*Server, string) {
	t.Helper()
	cfg, idx, store, _ := buildFixture(t)
	st, err := tags.NewStore(filepath.Join(t.TempDir(), "tags.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, idx, store, TagsOption(st))
	if err != nil {
		t.Fatal(err)
	}
	return srv, idx.Artifacts[0].Type + "/" + idx.Artifacts[0].Slug
}

func postTags(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/tags", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestSetTagsThenIndexCarriesThem(t *testing.T) {
	srv, key := newTagsServer(t)
	body := `{"type":"architecture","slug":"under-test","tags":["架构/平台","架构/平台"," 权限 "]}`
	rec := postTags(t, srv, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Key  string   `json:"key"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Key != key || strings.Join(resp.Tags, "|") != "权限|架构/平台" {
		t.Errorf("resp = %+v", resp)
	}

	idxRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(idxRec, httptest.NewRequest(http.MethodGet, "/api/index", nil))
	if !strings.Contains(idxRec.Body.String(), `"tags":["权限","架构/平台"]`) {
		t.Errorf("/api/index 未带标签: %s", idxRec.Body.String())
	}
	if !strings.Contains(idxRec.Body.String(), `"artifacts"`) {
		t.Error("/api/index 形状被破坏")
	}
}

func TestSetTagsValidation(t *testing.T) {
	srv, _ := newTagsServer(t)
	cases := []struct {
		name string
		body string
		code int
	}{
		{"坏 JSON", `{`, http.StatusBadRequest},
		{"未知产物", `{"type":"architecture","slug":"nope","tags":["a"]}`, http.StatusNotFound},
		{"空 type", `{"slug":"under-test","tags":["a"]}`, http.StatusNotFound},
		{"超量", `{"type":"architecture","slug":"under-test","tags":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q","r","s","t","u"]}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := postTags(t, srv, c.body); rec.Code != c.code {
			t.Errorf("%s: status = %d, want %d", c.name, rec.Code, c.code)
		}
	}
}

func TestSetTagsUnavailableWithoutStore(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store) // 未装配
	if err != nil {
		t.Fatal(err)
	}
	rec := postTags(t, srv, `{"type":"architecture","slug":"under-test","tags":["a"]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestClearTags(t *testing.T) {
	srv, _ := newTagsServer(t)
	postTags(t, srv, `{"type":"architecture","slug":"under-test","tags":["a/b"]}`)
	if rec := postTags(t, srv, `{"type":"architecture","slug":"under-test","tags":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/index", nil))
	if strings.Contains(rec.Body.String(), `"tags"`) {
		t.Errorf("清空后不应再有 tags: %s", rec.Body.String())
	}
}
