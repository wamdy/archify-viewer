// Package tags 持久化手工多级标签（键 = type/slug，跨交付稳定）。
//
// 写面纪律：本包只写用户配置目录下的 tags.yaml（原子写 + 0600），
// 绝不触碰 .archify/ 产物——与 internal/config.Editor、internal/thumbs 并列，
// 是本仓第三个也是最后一个被允许的落盘方。
package tags

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	// MaxDepth 限制一条标签路径的层级数（`a/b/c/d`）。
	MaxDepth = 4
	// MaxPerItem 限制单个产物最多挂多少条标签。
	MaxPerItem = 20
	// MaxLen 限制单条标签路径的字符数。
	MaxLen = 60
)

type file struct {
	Version   int                 `yaml:"version"`
	Artifacts map[string][]string `yaml:"artifacts"`
}

// Store 是 tags.yaml 的并发安全视图。
type Store struct {
	path string

	mu   sync.RWMutex
	data map[string][]string
}

// Key 把产物身份拼成稳定键：同一张图的重新交付共用一组标签。
func Key(typ, slug string) string {
	return strings.TrimSpace(typ) + "/" + strings.TrimSpace(slug)
}

// NewStore 读取（不存在则视为空表）。文件损坏时返回错误，由调用方降级为「无标签」。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path, data: map[string][]string{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	var f file
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("tags.yaml 解析失败: %w", err)
	}
	changed := false
	for k, v := range f.Artifacts {
		if !validKey(k) {
			changed = true
			continue
		}
		next := Normalize(v)
		if len(next) != len(v) {
			changed = true
		}
		s.data[k] = next
	}
	if changed {
		// 旧文件里的冗余父级/非法键在载入时清掉并回写，避免下次保存再把脏数据带出去。
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Path 返回存储文件路径。
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Get 返回某产物的标签副本。
func (s *Store) Get(key string) []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.data[key]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Snapshot 返回全量副本（供索引 join）。
func (s *Store) Snapshot() map[string][]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.data))
	for k, v := range s.data {
		cp := make([]string, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// Set 整体替换某产物的标签（幂等）；空列表等于删除该条。
func (s *Store) Set(key string, in []string) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("标签存储未装配")
	}
	if !validKey(key) {
		return nil, fmt.Errorf("非法产物键: %q", key)
	}
	next := Normalize(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(next) == 0 {
		delete(s.data, key)
	} else {
		s.data[key] = next
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	out := make([]string, len(next))
	copy(out, next)
	return out, nil
}

// GC 删除不在当前索引里的产物键，返回清理条数。
func (s *Store) GC(valid map[string]struct{}) (int, error) {
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k := range s.data {
		if _, ok := valid[k]; !ok {
			delete(s.data, k)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return removed, err
	}
	return removed, nil
}

func (s *Store) saveLocked() error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f := file{Version: 1, Artifacts: s.data}
	if len(f.Artifacts) == 0 {
		f.Artifacts = map[string][]string{}
	}
	raw, err := yaml.Marshal(&f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tags-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // rename 成功后是 no-op
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

// Normalize 清洗标签列表：去空白、去空段、去重、排序、截断超限项。
// 非法路径（空段、超长、超深、含控制字符）直接丢弃，不报错——用户的输入不该让整批保存失败。
func Normalize(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := cleanPath(raw)
		if !validPath(p) {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
		if len(out) >= MaxPerItem {
			break
		}
	}
	sort.Strings(out)
	return pruneAncestors(out)
}

// pruneAncestors 去掉被更具体标签覆盖的祖先：a 与 a/b 同时存在时只留 a/b。
// 按段判断（a 不该吞掉 ab）；多级递归由逐条覆盖判定自然处理（a,a/b,a/b/c → a/b/c）。
func pruneAncestors(list []string) []string {
	if len(list) < 2 {
		return list
	}
	out := make([]string, 0, len(list))
	for i, t := range list {
		covered := false
		for j, other := range list {
			if i != j && strings.HasPrefix(other, t+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, t)
		}
	}
	return out
}

func cleanPath(p string) string {
	parts := strings.Split(strings.TrimSpace(p), "/")
	segs := make([]string, 0, len(parts))
	for _, s := range parts {
		s = strings.TrimSpace(s)
		if s != "" {
			segs = append(segs, s)
		}
	}
	return strings.Join(segs, "/")
}

func validPath(p string) bool {
	if p == "" || len([]rune(p)) > MaxLen {
		return false
	}
	segs := strings.Split(p, "/")
	if len(segs) > MaxDepth {
		return false
	}
	for _, seg := range segs {
		if strings.TrimSpace(seg) == "" {
			return false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

// validKey 只接受 `type/slug` 形态（两段，非空，不含控制字符）。
func validKey(k string) bool {
	segs := strings.Split(k, "/")
	if len(segs) != 2 {
		return false
	}
	for _, seg := range segs {
		if strings.TrimSpace(seg) == "" || len(seg) > 80 {
			return false
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return false
			}
		}
	}
	return true
}
