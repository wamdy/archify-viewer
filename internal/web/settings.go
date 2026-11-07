package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"archify-viewer/internal/config"
)

const maxSettingsBody = 1 << 20 // 与 ssh-tunnel-ui 同口径：限制单个设置请求体大小

// SettingsStore 是设置页的写面接口。实现在 internal/config.Editor：
// web 包只调方法，不出现任何 os.WriteFile / os.Rename / os.MkdirAll（分层红线）。
type SettingsStore interface {
	Settings() config.SettingsView
	AddRoot(path string) (config.SettingsView, error)
	RemoveRoot(path string) (config.SettingsView, error)
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
	default:
		err = errors.New("action 只能是 add 或 remove")
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
