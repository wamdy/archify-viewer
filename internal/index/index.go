// Package index 只读扫描 archify 产物库并建立「ID → 产物」白名单。
//
// 两条结构性纪律（对应 tasks.md 四-1 / 四-2）：
//   - 全包不出现任何写文件系统的路径（只 os.Stat / os.ReadFile / os.ReadDir）；
//   - 对外唯一寻址键是 ID（产物 HTML 绝对路径的 sha256 前 12 位），绝对路径只存在于
//     进程内白名单 map，不进 JSON、不进 stdout（--verbose 例外）。
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"archify-viewer/internal/config"
)

// knownTypes 与 archify 的 Type router 一致（技能包 SKILL.md §Type router）+ explainer。
var knownTypes = map[string]bool{
	"architecture": true, "workflow": true, "sequence": true, "dataflow": true,
	"lifecycle": true, "erd": true, "class": true, "tree": true, "timeline": true,
	"waterfall": true, "explainer": true,
}

// 目录名约定 <type>-<slug>-<YYYYMMDD-HHMMSS>（技能包 SKILL.md:14）。
var dirnameRE = regexp.MustCompile(`^([a-z]+)-(.+)-(\d{8}-\d{6})$`)

// Artifact 是一件可浏览产物。json 形态即 /api/index 的元素，绝对路径以 json:"-" 排除。
type Artifact struct {
	ID         string   `json:"id"`
	Root       string   `json:"root"`
	Type       string   `json:"type"`
	Slug       string   `json:"slug"`
	Stamp      string   `json:"stamp,omitempty"`
	RelPath    string   `json:"rel_path"`
	HTMLName   string   `json:"html"`
	Title      string   `json:"title"`
	Subtitle   string   `json:"subtitle,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Quality    string   `json:"quality,omitempty"`
	Delivery   string   `json:"delivery"`
	Size       int64    `json:"size"`
	ModTime    int64    `json:"mtime"`
	Warnings   []string `json:"warnings,omitempty"`

	htmlPath string // 白名单内部：产物 HTML 绝对路径
}

// HTMLPath 供 desktop/open 使用；不得进入任何 HTTP 响应或默认日志。
func (a *Artifact) HTMLPath() string { return a.htmlPath }

// RootStatus 是一处产物根的扫描结果。Path 同样不出 JSON；
// Err 只放规则码（ROOT-UNREADABLE / ARTIFACT-UNREADABLE: <目录名>），
// 原始错误串内含根绝对路径，一律只进进程日志（红线 4，评审 BLOCK-2）。
type RootStatus struct {
	Label string `json:"label"`
	Path  string `json:"-"`
	Count int    `json:"count"`
	Err   string `json:"error,omitempty"`
}

// Index 是一次扫描的结果快照。
type Index struct {
	GeneratedAt   int64          `json:"generated_at"`
	RootSource    string         `json:"root_source"`
	Roots         []RootStatus   `json:"roots"`
	Artifacts     []*Artifact    `json:"artifacts"`
	ByType        map[string]int `json:"by_type"`
	ParseWarnings int            `json:"parse_warnings"`
}

// Store 是 ID 白名单；服务端只认它，不认路径。
type Store struct {
	list []*Artifact
	byID map[string]*Artifact
}

// Path 返回 ID 命中的绝对路径；未命中 ok=false。
func (s *Store) Path(id string) (string, bool) {
	art, ok := s.byID[id]
	if !ok {
		return "", false
	}
	return art.htmlPath, true
}

// All 返回索引结果（按 mtime 倒序）。
func (s *Store) All() []*Artifact { return s.list }

// ErrNotFound 表示终端检索无命中。
var ErrNotFound = errors.New("未找到匹配的产物")

// ErrAmbiguous 表示命中多件，Candidates 携带 ID 清单供用户改选。
type ErrAmbiguous struct {
	Term       string
	Candidates []string
}

func (e *ErrAmbiguous) Error() string {
	return fmt.Sprintf("%q 命中 %d 件产物，请用 ID 精确指定：%s", e.Term, len(e.Candidates), strings.Join(e.Candidates, ", "))
}

// Find 解析终端入参：完整 ID → 唯一前缀 ID → slug/title 子串（唯一命中）。
func (s *Store) Find(term string) (*Artifact, error) {
	if term == "" {
		return nil, ErrNotFound
	}
	if art, ok := s.byID[term]; ok {
		return art, nil
	}
	if hits := s.prefixHits(term); len(hits) == 1 {
		return hits[0], nil
	} else if len(hits) > 1 {
		return nil, ambiguous(term, hits)
	}

	needle := strings.ToLower(term)
	var hits []*Artifact
	for _, art := range s.list {
		if strings.Contains(strings.ToLower(art.Slug), needle) ||
			strings.Contains(strings.ToLower(art.Title), needle) ||
			strings.Contains(strings.ToLower(art.DirName()), needle) {
			hits = append(hits, art)
		}
	}
	switch len(hits) {
	case 0:
		return nil, ErrNotFound
	case 1:
		return hits[0], nil
	default:
		return nil, ambiguous(term, hits)
	}
}

func (s *Store) prefixHits(term string) []*Artifact {
	var hits []*Artifact
	for _, art := range s.list {
		if strings.HasPrefix(art.ID, term) {
			hits = append(hits, art)
		}
	}
	return hits
}

func ambiguous(term string, hits []*Artifact) error {
	ids := make([]string, 0, len(hits))
	for _, art := range hits {
		ids = append(ids, art.ID)
	}
	sort.Strings(ids)
	return &ErrAmbiguous{Term: term, Candidates: ids}
}

// DirName 是产物所在目录的 basename（RelPath 的最后一段）。
func (a *Artifact) DirName() string { return filepath.Base(a.RelPath) }

// Scan 遍历所有产物根，返回索引快照与 ID 白名单。
// 单个根不可读只在该根记 Err 并继续（design §六 ROOT-UNREADABLE）；不因为一件坏产物崩溃。
func Scan(cfg *config.Config) (*Index, *Store) {
	idx := &Index{
		GeneratedAt: nowUnix(),
		RootSource:  cfg.Source(),
		ByType:      map[string]int{},
	}
	store := &Store{byID: map[string]*Artifact{}}

	// 设置页可在运行中改目录：必须取锁保护的快照，否则与 cfg.SetRoots 形成数据竞争。
	for _, root := range cfg.RootSnapshot() {
		status := RootStatus{Label: root.Label, Path: root.Path}
		entries, err := os.ReadDir(root.Path)
		if err != nil {
			// 红线 4：/api/index 与界面只出规则码，原始错误（内含根绝对路径）进进程日志。
			status.Err = "ROOT-UNREADABLE"
			log.Printf("产物根不可读 %s: %v", root.Path, err)
			idx.Roots = append(idx.Roots, status)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			art, warnings, err := readArtifact(root, entry.Name())
			if err != nil {
				// 不是产物目录（无 HTML 主件）属正常噪音，不计警告。
				if errors.Is(err, errNoHTML) {
					continue
				}
				status.Err = appendUnique(status.Err, "ARTIFACT-UNREADABLE: "+entry.Name())
				log.Printf("产物目录读取失败 %s/%s: %v", root.Path, entry.Name(), err)
				continue
			}
			idx.ParseWarnings += len(warnings)
			art.Warnings = warnings
			idx.Artifacts = append(idx.Artifacts, art)
			idx.ByType[art.Type]++
			status.Count++
			store.list = append(store.list, art)
			store.byID[art.ID] = art
		}
		idx.Roots = append(idx.Roots, status)
	}

	sort.SliceStable(idx.Artifacts, func(i, j int) bool {
		a, b := idx.Artifacts[i], idx.Artifacts[j]
		if a.ModTime != b.ModTime {
			return a.ModTime > b.ModTime
		}
		return a.ID < b.ID
	})
	store.list = idx.Artifacts
	return idx, store
}

var errNoHTML = errors.New("no-html")

func readArtifact(root config.Root, dirName string) (*Artifact, []string, error) {
	dir := filepath.Join(root.Path, dirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var htmls []string
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".html") {
			htmls = append(htmls, e.Name())
		}
	}
	if len(htmls) == 0 {
		return nil, nil, errNoHTML
	}

	var warnings []string
	art := &Artifact{Root: root.Label, Type: "unknown", Slug: dirName}
	art.Type, art.Slug, art.Stamp = parseDirName(dirName)
	if art.Type == "unknown" {
		warnings = append(warnings, "DIRNAME-UNPARSED")
	}

	// candidate 先读：主件选择要用它声明的 meta.output（次序见 selectMainFile 注释）。
	cand, candWarnings := readCandidate(dir)
	warnings = append(warnings, candWarnings...)

	htmlName, extraWarnings := selectMainFile(dir, htmls, art.Slug, strings.TrimSpace(cand.Meta.Output))
	warnings = append(warnings, extraWarnings...)
	art.HTMLName = htmlName
	art.htmlPath = filepath.Join(dir, htmlName)
	art.RelPath = dirName

	info, err := os.Stat(art.htmlPath)
	if err != nil {
		return nil, nil, err
	}
	art.Size = info.Size()
	art.ModTime = info.ModTime().Unix()

	art.Title = strings.TrimSpace(cand.Meta.Title)
	art.Subtitle = strings.TrimSpace(cand.Meta.Subtitle)
	art.Quality = strings.TrimSpace(cand.Meta.QualityProfile)
	art.Repository = normalizeRepository(cand.Meta.Repository)
	if art.Title == "" {
		art.Title = art.Slug
	}

	delivery, sidecarWarnings := readDelivery(dir, art.HTMLName)
	warnings = append(warnings, sidecarWarnings...)
	art.Delivery = delivery

	art.ID = idFor(art.htmlPath)
	return art, warnings, nil
}

func parseDirName(name string) (typ, slug, stamp string) {
	m := dirnameRE.FindStringSubmatch(name)
	if m == nil {
		return "unknown", name, ""
	}
	if !knownTypes[m[1]] {
		return "unknown", name, ""
	}
	return m[1], m[2], m[3]
}

// pickHTML 优先 <slug>.html（生产者按约定命名主件）；否则单张直取；多张歧义取 mtime 最新并记警告。
// selectMainFile 定主件，优先级固定（评审 QUESTION-1 裁决：以 AGENTS.md §4 的文档序为准）：
//
//  1. <slug>.html            —— 目录名里的 slug 就是产物身份（技能包 SKILL.md:14 的约定）；
//  2. candidate 的 meta.output —— 生产者显式声明的主件名，仅在 1 落空时生效；
//  3. 目录内唯一 HTML；
//  4. mtime 最新一张 + HTML-AMBIGUOUS（同 mtime 按字典序取第一张，保证可重复）。
func selectMainFile(dir string, htmls []string, slug, declaredOutput string) (string, []string) {
	if slug != "" {
		want := slug + ".html"
		for _, h := range htmls {
			if strings.EqualFold(h, want) {
				return h, nil
			}
		}
	}
	if declaredOutput != "" {
		for _, h := range htmls {
			if strings.EqualFold(h, declaredOutput) {
				return h, nil
			}
		}
		// 声明名不在顶层 HTML 清单里：只有确实存在该文件时才采纳，避免指到子目录取证件。
		if fileExists(filepath.Join(dir, declaredOutput)) {
			return declaredOutput, nil
		}
	}
	if len(htmls) == 1 {
		return htmls[0], nil
	}
	best, bestMod := htmls[0], int64(-1)
	for _, h := range htmls {
		info, err := os.Stat(filepath.Join(dir, h))
		if err != nil {
			continue
		}
		if info.ModTime().Unix() > bestMod {
			best, bestMod = h, info.ModTime().Unix()
		}
	}
	return best, []string{"HTML-AMBIGUOUS"}
}

type candidateDoc struct {
	Meta struct {
		Title          string `json:"title"`
		Subtitle       string `json:"subtitle"`
		Output         string `json:"output"`
		QualityProfile string `json:"quality_profile"`
		Repository     any    `json:"repository"`
	} `json:"meta"`
}

func readCandidate(dir string) (candidateDoc, []string) {
	var doc candidateDoc
	path := filepath.Join(dir, "candidate.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, []string{"CANDIDATE-MISSING"}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, []string{"CANDIDATE-INVALID"}
	}
	return doc, nil
}

// normalizeRepository 兼容对象（common.schema.json#/$defs/repository）与裸字符串两种形态。
func normalizeRepository(v any) string {
	switch value := v.(type) {
	case string:
		return strings.TrimSuffix(value, ".git")
	case map[string]any:
		if url, ok := value["url"].(string); ok {
			return strings.TrimSuffix(url, ".git")
		}
	}
	return ""
}

// readDelivery 把 sidecar 的 status 映射为 current / failed / unknown（design D-8）。
func readDelivery(dir, htmlName string) (string, []string) {
	path := filepath.Join(dir, strings.TrimSuffix(htmlName, filepath.Ext(htmlName))+".delivery.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown", []string{"SIDECAR-MISSING"}
	}
	var doc struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "unknown", []string{"SIDECAR-INVALID"}
	}
	switch strings.ToLower(strings.TrimSpace(doc.Status)) {
	case "":
		return "unknown", nil
	case "current", "failed":
		return strings.ToLower(doc.Status), nil
	default:
		return "unknown", nil
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func appendUnique(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}
