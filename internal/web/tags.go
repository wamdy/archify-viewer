package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"archify-viewer/internal/index"
	"archify-viewer/internal/tags"
)

// handleSetTags 整体替换某产物（type + slug）的标签。幂等：同一请求重放结果一致。
// 只接受索引里存在的产物；标签存储未装配时 503（能力缺失不伪装成功）。
func (s *Server) handleSetTags(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.tags
	s.mu.RUnlock()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tags_unavailable"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Type string   `json:"type"`
		Slug string   `json:"slug"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	if len(req.Tags) > tags.MaxPerItem {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "too_many_tags"})
		return
	}

	s.mu.RLock()
	art := findByTypeSlug(s.store, req.Type, req.Slug)
	s.mu.RUnlock()
	if art == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
		return
	}

	key := tags.Key(art.Type, art.Slug)
	saved, err := st.Set(key, req.Tags)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "tags_write_denied"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "tags_write_failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "tags": saved})
}

func findByTypeSlug(st *index.Store, typ, slug string) *index.Artifact {
	if st == nil || typ == "" || slug == "" {
		return nil
	}
	for _, a := range st.All() {
		if a.Type == typ && a.Slug == slug {
			return a
		}
	}
	return nil
}

// indexView 在索引快照上叠加标签（键 = type/slug），不改 index 包的数据结构。
func (s *Server) indexView() any {
	s.mu.RLock()
	idx, st, mode := s.idx, s.tags, s.clientMode
	s.mu.RUnlock()
	if mode == "" {
		mode = "native"
	}
	if st == nil || idx == nil || len(idx.Artifacts) == 0 {
		return struct {
			*index.Index
			ClientMode string `json:"client_mode"`
		}{Index: idx, ClientMode: mode}
	}
	snap := st.Snapshot()
	type artifactView struct {
		*index.Artifact
		Tags []string `json:"tags,omitempty"`
	}
	out := struct {
		*index.Index
		Artifacts  []artifactView `json:"artifacts"`
		ClientMode string         `json:"client_mode"`
	}{Index: idx, Artifacts: make([]artifactView, 0, len(idx.Artifacts)), ClientMode: mode}
	for _, a := range idx.Artifacts {
		v := artifactView{Artifact: a}
		if t := snap[tags.Key(a.Type, a.Slug)]; len(t) > 0 {
			v.Tags = t
		}
		out.Artifacts = append(out.Artifacts, v)
	}
	return out
}

// TagsOption 注入标签存储；nil 表示未装配（/api/tags 返回 503，索引不带 tags）。
func TagsOption(st *tags.Store) Option {
	return func(s *Server) { s.tags = st }
}
