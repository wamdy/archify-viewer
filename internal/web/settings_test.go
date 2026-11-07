package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archify-viewer/internal/config"
	"archify-viewer/internal/index"
)

// makeRootWithArtifact 造一处含 1 件产物的产物根（目录约定与 archify 一致）。
func makeRootWithArtifact(t *testing.T, slug string) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "architecture-"+slug+"-20261107-120000")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	html := []byte("<!doctype html><title>" + slug + "</title>")
	if err := os.WriteFile(filepath.Join(dir, slug+".html"), html, 0o644); err != nil {
		t.Fatal(err)
	}
	return root, html
}

func newSettingsServer(t *testing.T) (*Server, *config.Config, *config.Editor) {
	t.Helper()
	rootA, _ := makeRootWithArtifact(t, "alpha")
	cfg := &config.Config{
		RootSource: "config",
		Roots:      []config.Root{{Label: "alpha", Path: rootA}},
		GUI:        config.GUI{Addr: config.DefaultGUIAddr, CloseAction: config.DefaultCloseAction},
	}
	idx, store := index.Scan(cfg)
	editor, err := config.NewEditor(cfg, filepath.Join(t.TempDir(), "conf", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(cfg, idx, store, WithSettings(editor))
	if err != nil {
		t.Fatal(err)
	}
	return srv, cfg, editor
}

func callJSON(t *testing.T, srv *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("%s %s 缺 no-store: %q", method, path, got)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s 响应不是 JSON: %v / %s", method, path, err, rec.Body.String())
	}
	return rec.Code, out
}

func rootsOf(t *testing.T, body map[string]any) []any {
	t.Helper()
	roots, ok := body["roots"].([]any)
	if !ok {
		t.Fatalf("响应缺 roots: %+v", body)
	}
	return roots
}

func TestSettingsEndpointsUnavailableWithoutEditor(t *testing.T) {
	cfg, idx, store, _ := buildFixture(t)
	srv, err := New(cfg, idx, store)
	if err != nil {
		t.Fatal(err)
	}
	code, body := callJSON(t, srv, http.MethodGet, "/api/settings", "")
	if code != http.StatusServiceUnavailable || body["error"] != "settings_unavailable" {
		t.Fatalf("未装配时应 503，实际 %d / %+v", code, body)
	}
}

func TestSettingsRootsAddRemoveRescansImmediately(t *testing.T) {
	srv, cfg, editor := newSettingsServer(t)
	rootB, _ := makeRootWithArtifact(t, "beta")

	code, view := callJSON(t, srv, http.MethodGet, "/api/settings", "")
	if code != http.StatusOK || len(rootsOf(t, view)) != 1 {
		t.Fatalf("初始设置不符: %d / %+v", code, view)
	}
	if view["close_action"] != config.DefaultCloseAction || view["editable"] != true {
		t.Errorf("设置快照字段不符: %+v", view)
	}

	// 添加一处新根：响应应立即含两处，且 /api/index 无需手动 rescan 就能看到 2 件产物。
	code, view = callJSON(t, srv, http.MethodPost, "/api/settings/roots",
		fmt.Sprintf(`{"action":"add","path":%q}`, rootB))
	if code != http.StatusOK {
		t.Fatalf("添加目录失败: %d / %+v", code, view)
	}
	if len(rootsOf(t, view)) != 2 || cfg.Source() != "config" {
		t.Fatalf("运行态未同步: roots=%+v source=%s", view["roots"], cfg.Source())
	}
	code, idxBody := callJSON(t, srv, http.MethodGet, "/api/index", "")
	if code != http.StatusOK || len(idxBody["artifacts"].([]any)) != 2 {
		t.Fatalf("添加目录后索引未立即刷新: %d / %+v", code, idxBody["artifacts"])
	}

	// 移除初始根后，索引应回落到 1 件。
	rootA := cfg.RootSnapshot()[0].Path
	code, view = callJSON(t, srv, http.MethodPost, "/api/settings/roots",
		fmt.Sprintf(`{"action":"remove","path":%q}`, rootA))
	if code != http.StatusOK || len(rootsOf(t, view)) != 1 {
		t.Fatalf("移除目录失败: %d / %+v", code, view)
	}
	_, idxBody = callJSON(t, srv, http.MethodGet, "/api/index", "")
	if len(idxBody["artifacts"].([]any)) != 1 {
		t.Errorf("移除目录后索引未刷新: %+v", idxBody["artifacts"])
	}
	if editor.Path() != view["config_path"] {
		t.Errorf("配置路径不一致: %s vs %v", editor.Path(), view["config_path"])
	}
}

func TestSettingsRejectsInvalidInput(t *testing.T) {
	srv, _, _ := newSettingsServer(t)
	cases := []string{
		`{"action":"bogus","path":"x"}`,
		`{"action":"add","path":""}`,
		`{"action":"add","path":"Z:/definitely-missing-archify"}`,
		`{"action":"remove","path":"not-in-list"}`,
		`not-json`,
	}
	for _, body := range cases {
		code, out := callJSON(t, srv, http.MethodPost, "/api/settings/roots", body)
		if code != http.StatusBadRequest {
			t.Errorf("%s 期望 400，实际 %d / %+v", body, code, out)
		}
	}
}

func TestSettingsCloseActionPersistsAndAffectsRuntime(t *testing.T) {
	srv, cfg, editor := newSettingsServer(t)
	code, view := callJSON(t, srv, http.MethodPost, "/api/settings/close-action", `{"value":"quit"}`)
	if code != http.StatusOK || view["close_action"] != "quit" {
		t.Fatalf("保存关闭行为失败: %d / %+v", code, view)
	}
	if cfg.CloseActionValue() != "quit" {
		t.Errorf("托盘读取的运行态未更新: %q", cfg.CloseActionValue())
	}
	// 配置文件里应含 close_action，重新加载仍在。
	raw, err := os.ReadFile(editor.Path())
	if err != nil || !strings.Contains(string(raw), "close_action: quit") {
		t.Errorf("关闭行为未落盘: %v / %s", err, raw)
	}
	code, view = callJSON(t, srv, http.MethodPost, "/api/settings/close-action", `{"value":"nonsense"}`)
	if code != http.StatusOK || view["close_action"] != config.DefaultCloseAction {
		t.Errorf("未知值应收敛到默认: %d / %+v", code, view)
	}
}

func TestFaviconServedFromWhitelist(t *testing.T) {
	srv, _, _, _ := newServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.svg", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/favicon.svg = %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") {
		t.Errorf("favicon Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("favicon 缺 no-store")
	}
}

// 设置页只返回用户配置的目录路径，不得把索引内部发现的产物绝对路径泄进 /api/index。
func TestSettingsDoesNotLeakPathsIntoIndex(t *testing.T) {
	srv, cfg, _ := newSettingsServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/index", nil))
	for _, r := range cfg.RootSnapshot() {
		if strings.Contains(rec.Body.String(), r.Path) {
			t.Errorf("/api/index 泄漏了绝对路径 %s", r.Path)
		}
	}
}
