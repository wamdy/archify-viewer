package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"archify-viewer/internal/index"
	"archify-viewer/internal/thumbs"
)

// handleThumb 按 ID 返回缩略图：命中读盘、未命中串行采集。
//
// 红线延续：id 必须先在索引里存在；缓存键由索引字段派生（不来自请求），
// 因此这条取文件能力比 /a/{id} 更窄——只可能命中我们自己生成的缓存文件。
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	art := findArtifact(s.store, id)
	base := strings.TrimRight(s.url, "/")
	st := s.thumbs
	s.mu.RUnlock()

	if art == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
		return
	}
	if st == nil || st.Dir() == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "thumb_unavailable"})
		return
	}

	png, err := st.GetOrCreate(r.Context(), thumbKey(art), base+"/a/"+art.ID)
	if err != nil {
		code := "thumb_failed"
		if errors.Is(err, thumbs.ErrUnavailable) {
			code = "thumb_unavailable"
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": code})
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}

// thumbKey 让缓存随产物内容变化（同路径重生成时 ID 不变、mtime/size 会变）。
func thumbKey(a *index.Artifact) string {
	return fmt.Sprintf("%s-%d-%d", a.ID, a.ModTime, a.Size)
}

func findArtifact(st *index.Store, id string) *index.Artifact {
	if st == nil || id == "" {
		return nil
	}
	for _, a := range st.All() {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// thumbItems 返回当前索引的「缓存键 → 采集 URL」映射。
// 采集地址与真实查看同源（/a/{id}），保证产物内相对资源行为一致。
func (s *Server) thumbItems() map[string]string {
	s.mu.RLock()
	base := strings.TrimRight(s.url, "/")
	var arts []*index.Artifact
	if s.idx != nil {
		arts = s.idx.Artifacts
	}
	s.mu.RUnlock()
	out := make(map[string]string, len(arts))
	for _, a := range arts {
		out[thumbKey(a)] = base + "/a/" + a.ID
	}
	return out
}

// handleRebuildThumbs 手动重建：先删当前索引范围的缓存，再后台排队重生成。
func (s *Server) handleRebuildThumbs(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.thumbs
	s.mu.RUnlock()
	if st == nil || st.Dir() == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "thumb_unavailable"})
		return
	}
	items := s.thumbItems()
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	removed := st.DeleteKeys(keys)
	queued := st.EnsureMissing(items)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "queued": queued})
}

// ThumbsOption 注入缩略图缓存。nil 表示未装配：/t/{id} 返回 thumb_unavailable，
// 前端保持占位（能力缺失不伪装成功）。
func ThumbsOption(st *thumbs.Store) Option {
	return func(s *Server) { s.thumbs = st }
}
