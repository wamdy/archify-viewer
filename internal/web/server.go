// Package web 提供 archify-viewer 的只读 HTTP 面：一份 SPA、一个索引端点、一个按 ID 取产物的端点。
//
// 不提供目录列举、不提供按路径取文件、不改写任何产物（tasks.md 四-1/四-2/四-6）。
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"archify-viewer/internal/config"
	"archify-viewer/internal/index"
)

//go:embed static
var embedded embed.FS

// Server 复用同一份 embed 资产：原生 WebView 与系统浏览器看到的 UI 必须完全一致（impact R-4）。
type Server struct {
	cfg      *config.Config
	staticFS fs.FS
	etag     map[string]string

	// mu 保护 idx/store（Rescan 会整体替换快照）。
	mu    sync.RWMutex
	idx   *index.Index
	store *index.Store

	srv *http.Server
	ln  net.Listener
	url string
}

// New 构造服务；不监听、不启动。
func New(cfg *config.Config, idx *index.Index, store *index.Store) (*Server, error) {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		// embed 模式编译期校验，运行期不可能失败。
		return nil, fmt.Errorf("内嵌资产解包失败: %w", err)
	}
	s := &Server{cfg: cfg, idx: idx, store: store, staticFS: sub, etag: map[string]string{}}
	for _, name := range assetNames {
		raw, err := fs.ReadFile(sub, name)
		if err != nil {
			return nil, fmt.Errorf("内嵌资产 %s 缺失: %w", name, err)
		}
		sum := sha256.Sum256(raw)
		s.etag[name] = `"` + hex.EncodeToString(sum[:8]) + `"`
	}
	s.srv = &http.Server{Handler: s.router(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second}
	return s, nil
}

// 静态资产白名单：只这三份，其余路径一律 404（不提供服务目录列举）。
var assetNames = []string{"index.html", "app.js", "style.css"}

func (s *Server) router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/index", s.handleIndex)
	mux.HandleFunc("GET /a/{id}", s.handleArtifact)
	mux.HandleFunc("GET /", s.handleStatic)
	return mux
}

// Handler 暴露给 httptest。
func (s *Server) Handler() http.Handler { return s.srv.Handler }

// Start 绑定回环监听并开始服务；返回后可用 URL() 取实际地址。
func (s *Server) Start() error {
	ln, err := Bind(s.cfg.GUI.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	s.url = fmt.Sprintf("http://%s:%s/", host, port)
	go func() {
		if serr := s.srv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			log.Printf("服务异常退出: %v", serr)
		}
	}()
	return nil
}

// URL 返回服务端回报的实际地址（默认端口 0 时只能由监听器给出，勿由前端猜测）。
func (s *Server) URL() string { return s.url }

// Shutdown 优雅退出。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("rescan") == "1" {
		if err := s.Rescan(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "rescan_failed", "detail": err.Error()})
			return
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	writeJSON(w, http.StatusOK, s.idx)
}

// Rescan 重新扫描产物根并替换索引快照（仍然只读）。
// 有意为之：一次会话里 archify 可能刚交付新图，让人重启进程才能看到是体验缺陷；
// 但它不改任何文件、也不引入写面，红线 1 不受影响。
func (s *Server) Rescan() error {
	idx, store := index.Scan(s.cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idx, s.store = idx, store
	return nil
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.RLock()
	path, ok := s.store.Path(id)
	s.mu.RUnlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_artifact"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact_unreadable"})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stat_failed"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !isAsset(name) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	raw, err := fs.ReadFile(s.staticFS, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "asset_unreadable"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if want := s.etag[name]; want != "" {
		w.Header().Set("ETag", want)
		if match := r.Header.Get("If-None-Match"); match == want {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", contentTypeOf(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(raw)
}

func isAsset(name string) bool {
	for _, a := range assetNames {
		if a == name {
			return true
		}
	}
	return false
}

func contentTypeOf(name string) string {
	switch name {
	case "app.js":
		return "text/javascript; charset=utf-8"
	case "style.css":
		return "text/css; charset=utf-8"
	default:
		return "text/html; charset=utf-8"
	}
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
