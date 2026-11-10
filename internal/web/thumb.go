package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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

	theme := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("theme")))
	if theme != "dark" {
		theme = "light"
	}
	png, err := st.GetOrCreate(r.Context(), thumbKeyTheme(art, theme), base+"/s/"+art.ID, theme)
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

// handleShot 是缩略图专用的派生视图：产物 HTML 原样读取，只在 </head> 前注入一段
// 样式隐藏自带工具栏与标题块，让截图只剩图形部分。/a/{id} 仍字节原样，不受影响。
func (s *Server) handleShot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	art := findArtifact(s.store, id)
	s.mu.RUnlock()
	if art == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
		return
	}
	raw, err := os.ReadFile(art.HTMLPath())
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact_unreadable"})
		return
	}
	const shotStyle = "<style id=\"shot-style\">.toolbar{display:none!important}.header{display:none!important}" +
		".container{padding-top:10px!important}</style>"
	html := string(raw)
	if i := strings.Index(html, "</head>"); i >= 0 {
		html = html[:i] + shotStyle + html[i:]
	} else {
		html = shotStyle + html
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(html))
}

// thumbKey 让缓存随产物内容变化（同路径重生成时 ID 不变、mtime/size 会变）。
func thumbKey(a *index.Artifact) string {
	return fmt.Sprintf("%s-%d-%d", a.ID, a.ModTime, a.Size)
}

// thumbKeyTheme 让亮/暗两套缩略图各占一个缓存键：查看器切主题时取对应那张。
func thumbKeyTheme(a *index.Artifact, theme string) string {
	if theme != "dark" {
		theme = "light"
	}
	return thumbKey(a) + "-" + theme
}

// thumbJobs 返回当前索引的「双主题」待生成任务。
func (s *Server) thumbJobs() []thumbs.Job {
	s.mu.RLock()
	base := strings.TrimRight(s.url, "/")
	var arts []*index.Artifact
	if s.idx != nil {
		arts = s.idx.Artifacts
	}
	s.mu.RUnlock()
	jobs := make([]thumbs.Job, 0, len(arts)*2)
	for _, a := range arts {
		for _, theme := range []string{"light", "dark"} {
			jobs = append(jobs, thumbs.Job{Key: thumbKeyTheme(a, theme), URL: base + "/s/" + a.ID, Theme: theme})
		}
	}
	return jobs
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

// handleRebuildThumbs 手动重建：先删当前索引范围的缓存，再后台排队重生成。
// handleRebuildThumbs 重建缩略图：请求体带 {"id": "..."} 时只重建该件，
// 不带（或空体）则重建当前索引范围内的全部。
func (s *Server) handleRebuildThumbs(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	st := s.thumbs
	s.mu.RUnlock()
	if st == nil || st.Dir() == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "thumb_unavailable"})
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	// 空请求体合法（全量重建）；解析失败只当作没带 id。
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)

	jobs := s.thumbJobs()
	if req.ID != "" {
		s.mu.RLock()
		art := findArtifact(s.store, req.ID)
		s.mu.RUnlock()
		if art == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
			return
		}
		// 单件刷新同时重建亮/暗两张，切主题时不用等。
		keys := []string{thumbKeyTheme(art, "light"), thumbKeyTheme(art, "dark")}
		removed := st.DeleteKeys(keys)
		keep := make([]thumbs.Job, 0, 2)
		for _, j := range jobs {
			if j.Key == keys[0] || j.Key == keys[1] {
				keep = append(keep, j)
			}
		}
		queued := st.EnsureMissing(keep)
		writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "queued": queued})
		return
	}

	keys := make([]string, 0, len(jobs))
	for _, j := range jobs {
		keys = append(keys, j.Key)
	}
	removed := st.DeleteKeys(keys)
	queued := st.EnsureMissing(jobs)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "queued": queued})
}

// ThumbsOption 注入缩略图缓存。nil 表示未装配：/t/{id} 返回 thumb_unavailable，
// 前端保持占位（能力缺失不伪装成功）。
func ThumbsOption(st *thumbs.Store) Option {
	return func(s *Server) { s.thumbs = st }
}
