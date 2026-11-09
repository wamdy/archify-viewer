// Package thumbs 为产物首屏生成并缓存缩略图（PNG）。
//
// 边界（对应 AGENTS.md 的只读红线）：
//   - 本包是全仓除 internal/config.Editor 之外唯一的写盘方，只写派生缓存目录，
//     绝不触碰 .archify/ 下的产物；
//   - viewer 自身不渲染：截图由外部无头浏览器进程完成（见 browser.go），
//     产物字节在截图前后不变，internal/index 的 TestScanIsReadOnly 继续成立。
//
// 并发模型：同一 key 单飞（并发请求共享一次采集），跨 key 全局并发 1
// （一次翻页滚到 N 张卡也只排一个浏览器进程）。
package thumbs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrUnavailable 表示本机没有可用的无头浏览器，或未装配缓存目录。
	ErrUnavailable = errors.New("没有可用的无头浏览器（Chrome / Edge / Chromium）")
	// ErrFailed 表示采集失败（进程异常、超时、未产出有效 PNG）。
	ErrFailed = errors.New("缩略图生成失败")
)

// Capturer 把 URL 渲染成 PNG 字节。theme 只会是 "light" / "dark"。
// 实现不得改动任何产物文件。
type Capturer interface {
	Capture(ctx context.Context, url, theme string) ([]byte, error)
}

// Job 是一次待生成的缩略图：缓存键、采集地址、主题。
type Job struct {
	Key   string
	URL   string
	Theme string
}

type call struct {
	done chan struct{}
	png  []byte
	err  error
}

// Store 是缩略图缓存：命中读盘，未命中串行采集并原子落盘。
type Store struct {
	dir string
	cap Capturer
	log *log.Logger

	mu    sync.Mutex
	calls map[string]*call
	sem   chan struct{}
}

// NewStore 创建缓存。dir 为空表示「未装配」，所有读取返回 ErrUnavailable。
func NewStore(dir string, cap Capturer, lg *log.Logger) *Store {
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return &Store{
		dir:   dir,
		cap:   cap,
		log:   lg,
		calls: map[string]*call{},
		sem:   make(chan struct{}, 1),
	}
}

// Dir 返回缓存目录；nil 安全。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// GetOrCreate 返回 key 对应的 PNG：命中直接读盘，未命中采集一次并落盘。
// 同一 key 的并发调用共享同一次采集；不同 key 之间串行。
func (s *Store) GetOrCreate(ctx context.Context, key, url, theme string) ([]byte, error) {
	if s == nil || s.dir == "" {
		return nil, ErrUnavailable
	}
	if !safeKey(key) {
		return nil, fmt.Errorf("%w: 非法缓存键 %q", ErrFailed, key)
	}
	path := filepath.Join(s.dir, key+".png")
	if b, ok := readValid(path); ok {
		return b, nil
	}

	s.mu.Lock()
	if c, ok := s.calls[key]; ok {
		s.mu.Unlock()
		select {
		case <-c.done:
			return c.png, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	s.calls[key] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.calls, key)
		s.mu.Unlock()
		close(c.done)
	}()

	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		c.err = ctx.Err()
		return nil, c.err
	}
	defer func() { <-s.sem }()

	// 排队期间可能已被别的请求生成。
	if b, ok := readValid(path); ok {
		c.png = b
		return b, nil
	}
	if s.cap == nil {
		c.err = ErrUnavailable
		return nil, c.err
	}

	if theme != "dark" {
		theme = "light"
	}
	png, err := s.cap.Capture(ctx, url, theme)
	if err != nil {
		c.err = err
		return nil, err
	}
	if !isValidPNG(png) {
		c.err = fmt.Errorf("%w: 采集结果不是有效 PNG", ErrFailed)
		return nil, c.err
	}
	c.png = png
	if err := writeAtomic(path, png); err != nil {
		// 落盘失败不影响本次显示，但下次会重新采集。
		s.log.Printf("缩略图缓存写入失败 %s: %v", key, err)
		return png, nil
	}
	s.log.Printf("缩略图已生成 %s (%d bytes)", key, len(png))
	return png, nil
}

// GC 删除不在 valid 集合内的缓存文件，并清理崩溃残留的临时文件与 profile 目录。
func (s *Store) GC(valid map[string]struct{}) {
	if s == nil || s.dir == "" {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		keep := false
		switch {
		case strings.HasPrefix(name, ".tmp-"):
			// 原子写中途退出留下的临时文件。
		case strings.HasPrefix(name, ".profile-"):
			// 采集进程异常退出留下的 profile 目录。
		case strings.HasSuffix(name, ".png"):
			keep = hasKey(valid, strings.TrimSuffix(name, ".png"))
		default:
			keep = true // 不认识的条目不动（用户可能放了说明文件）
		}
		if keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, name)); err != nil {
			s.log.Printf("缩略图缓存清理失败 %s: %v", name, err)
		}
	}
}

func hasKey(valid map[string]struct{}, key string) bool {
	if valid == nil {
		return false
	}
	_, ok := valid[key]
	return ok
}

// safeKey 只允许 [a-z0-9_-]：缓存文件名由索引字段派生，这道校验保证任何调用方
// 也拼不出 `../` 之类逃出缓存目录的键（纵深防御，不依赖上游是否守规矩）。
func safeKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	for _, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// isValidPNG 只做结构自检：魔数 + IHDR 尺寸。不解码像素。
func isValidPNG(b []byte) bool {
	if len(b) < 33 || !bytes.Equal(b[:8], pngMagic) || string(b[12:16]) != "IHDR" {
		return false
	}
	w := binary.BigEndian.Uint32(b[16:20])
	h := binary.BigEndian.Uint32(b[20:24])
	return w > 0 && h > 0
}

// readValid 读缓存并自检；坏文件顺手删掉（自愈），让调用方重新采集。
func readValid(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if isValidPNG(b) {
		return b, true
	}
	_ = os.Remove(path)
	return nil, false
}

// writeAtomic 先写同目录临时文件再 rename，半截文件不会成为命中。
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.png")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // rename 成功后这句是 no-op
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Missing 返回给定键中「尚无有效缓存」的键集合（只读检查，不触发采集）。
func (s *Store) Missing(keys []string) []string {
	if s == nil || s.dir == "" {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !safeKey(k) {
			continue
		}
		if _, ok := readValid(filepath.Join(s.dir, k+".png")); !ok {
			out = append(out, k)
		}
	}
	return out
}

// EnsureMissing 在后台串行补齐缺失的缩略图。
// 立即返回排队数；生成走既有的全局并发 1 与单飞，不会阻塞 /api/index。
func (s *Store) EnsureMissing(jobs []Job) int {
	if s == nil || s.dir == "" || len(jobs) == 0 {
		return 0
	}
	todo := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		if !safeKey(j.Key) {
			continue
		}
		if _, ok := readValid(filepath.Join(s.dir, j.Key+".png")); !ok {
			todo = append(todo, j)
		}
	}
	if len(todo) == 0 {
		return 0
	}
	s.log.Printf("缩略图补生成排队 %d 件", len(todo))
	go func() {
		for _, j := range todo {
			if _, err := s.GetOrCreate(context.Background(), j.Key, j.URL, j.Theme); err != nil {
				s.log.Printf("缩略图补生成失败 %s: %v", j.Key, err)
			}
		}
		s.log.Printf("缩略图补生成完成 %d 件", len(todo))
	}()
	return len(todo)
}

// DeleteKeys 删除给定键的缓存文件（手动重建用），返回删除数。
func (s *Store) DeleteKeys(keys []string) int {
	if s == nil || s.dir == "" {
		return 0
	}
	n := 0
	for _, k := range keys {
		if !safeKey(k) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, k+".png")); err == nil {
			n++
		}
	}
	return n
}
