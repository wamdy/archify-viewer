package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"archify-viewer/internal/config"
)

const maxSettingsBody = 1 << 20 // 与 ssh-tunnel-ui 同口径：限制单个设置请求体大小

// FolderPicker 由 main 注入（gui/run 形态），返回用户选中的目录。
// 用函数注入而不是直接 import desktop，避免 web 与 desktop 互相依赖。
type FolderPicker func(title string) (string, error)

// WithFolderPicker 注入原生目录选择能力；未装配时端点返回 503。
func WithFolderPicker(p FolderPicker) Option {
	return func(s *Server) { s.pickFolder = p }
}

// BrowserOpener 由 main 注入：把回环 URL 交给系统默认浏览器。
type BrowserOpener func(url string) error

// WithBrowserOpener 注入「用浏览器打开」能力；未装配时端点 503。
func WithBrowserOpener(fn BrowserOpener) Option {
	return func(s *Server) { s.openBrowser = fn }
}

// handleOpenInBrowser 打开产物（带 id）或首页（不带 id）。
func (s *Server) handleOpenInBrowser(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	opener := s.openBrowser
	base := strings.TrimRight(s.url, "/")
	s.mu.RUnlock()
	if opener == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "browser_open_unavailable",
			"detail": "当前形态未装配系统浏览器打开",
		})
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
	target := base + "/"
	if req.ID != "" {
		s.mu.RLock()
		art := findArtifact(s.store, req.ID)
		s.mu.RUnlock()
		if art == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
			return
		}
		target = base + "/a/" + art.ID
	}
	if err := opener(target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "open_failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"opened": target})
}

func (s *Server) handlePickFolder(w http.ResponseWriter, _ *http.Request) {
	if s.pickFolder == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "picker_unavailable",
			"detail": "当前形态未装配目录选择（仅 gui / run 支持）",
		})
		return
	}
	path, err := s.pickFolder("选择 .archify 产物目录")
	if err != nil {
		if strings.Contains(err.Error(), "取消") {
			writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "picker_failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// SettingsStore 是设置页的写面接口。实现在 internal/config.Editor：
// web 包只调方法，不出现任何 os.WriteFile / os.Rename / os.MkdirAll（分层红线）。
type SettingsStore interface {
	Settings() config.SettingsView
	AddRoot(path string) (config.SettingsView, error)
	RemoveRoot(path string) (config.SettingsView, error)
	RenameRoot(path, label string) (config.SettingsView, error)
	SetCloseAction(value string) (config.SettingsView, error)
}

func (s *Server) settingsOrUnavailable(w http.ResponseWriter) bool {
	if s.settings == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "settings_unavailable",
			"detail": "本形态未装配设置编辑器（仅 gui / run 支持）",
		})
		return false
	}
	return true
}

func (s *Server) handleSettings(w http.ResponseWriter, _ *http.Request) {
	if !s.settingsOrUnavailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.settings.Settings())
}

func (s *Server) handleRoots(w http.ResponseWriter, r *http.Request) {
	if !s.settingsOrUnavailable(w) {
		return
	}
	var body struct {
		Action string `json:"action"`
		Path   string `json:"path"`
		Label  string `json:"label"`
	}
	if err := decodeSettingsBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}

	var view config.SettingsView
	var err error
	switch body.Action {
	case "add":
		view, err = s.settings.AddRoot(body.Path)
	case "remove":
		view, err = s.settings.RemoveRoot(body.Path)
	case "rename":
		view, err = s.settings.RenameRoot(body.Path, body.Label)
	default:
		err = errors.New("action 只能是 add / remove / rename")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_root", "detail": err.Error()})
		return
	}

	// 目录变更后立即重建索引快照，用户不必再点一次「重新扫描」；
	// 先落盘成功才走到这里，失败时当前会话保持原状。
	if err := s.Rescan(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rescan_failed", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCloseAction(w http.ResponseWriter, r *http.Request) {
	if !s.settingsOrUnavailable(w) {
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeSettingsBody(w, r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request", "detail": err.Error()})
		return
	}
	view, err := s.settings.SetCloseAction(body.Value)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_close_action", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func decodeSettingsBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxSettingsBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return errors.New("请求体不是合法 JSON 或超出大小限制")
	}
	return nil
}
