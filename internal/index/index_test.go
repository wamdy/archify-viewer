package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"archify-viewer/internal/config"
)

type fixture struct {
	root string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return &fixture{root: t.TempDir()}
}

// put 按 archify 约定造一件产物：目录名 + 主件 HTML + 可选 candidate/sidecar。
func (f *fixture) put(t *testing.T, dirName, htmlName, candidate, sidecar string) string {
	t.Helper()
	dir := filepath.Join(f.root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	htmlPath := filepath.Join(dir, htmlName)
	if err := os.WriteFile(htmlPath, []byte("<!doctype html><title>"+htmlName+"</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if candidate != "" {
		if err := os.WriteFile(filepath.Join(dir, "candidate.json"), []byte(candidate), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sidecar != "" {
		stem := strings.TrimSuffix(htmlName, filepath.Ext(htmlName))
		if err := os.WriteFile(filepath.Join(dir, stem+".delivery.json"), []byte(sidecar), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *fixture) scan(t *testing.T) (*Index, *Store) {
	t.Helper()
	return Scan(&config.Config{RootSource: "test", Roots: []config.Root{{Label: "fix", Path: f.root}}})
}

func TestParseDirNameCoversAllTypes(t *testing.T) {
	types := []string{"architecture", "workflow", "sequence", "dataflow", "lifecycle", "erd", "class", "tree", "timeline", "waterfall", "explainer"}
	for _, typ := range types {
		dir := typ + "-demo-" + "20261106-120000"
		gotType, gotSlug, gotStamp := parseDirName(dir)
		if gotType != typ || gotSlug != "demo" || gotStamp != "20261106-120000" {
			t.Errorf("%s 解析为 type=%q slug=%q stamp=%q", dir, gotType, gotSlug, gotStamp)
		}
	}
	// slug 自带连字符时时间戳仍在末尾。
	gotType, gotSlug, _ := parseDirName("architecture-multi-tenant-home-20261106-010203")
	if gotType != "architecture" || gotSlug != "multi-tenant-home" {
		t.Errorf("带连字符 slug 解析错：%q / %q", gotType, gotSlug)
	}
	// 类型不在词表内 ⇒ unknown + 警告。
	if typ, _, _ := parseDirName("mystery-x-20261106-010203"); typ != "unknown" {
		t.Errorf("未知类型应回落 unknown，得到 %q", typ)
	}
	if typ, _, _ := parseDirName("no-timestamp-here"); typ != "unknown" {
		t.Errorf("非法目录名应 unknown，得到 %q", typ)
	}
}

func TestScanHappyPath(t *testing.T) {
	f := newFixture(t)
	f.put(t, "architecture-web-app-20261106-120000", "web-app.html",
		`{"meta":{"title":"Web App 架构","subtitle":"三层","quality_profile":"showcase","repository":{"url":"https://gitlab.wamdy.com/wamdy.com/appollo.git","revision":"0123456789abcdef0123456789abcdef01234567"}}}`,
		`{"schemaVersion":1,"status":"current"}`)

	idx, store := f.scan(t)
	if len(idx.Artifacts) != 1 {
		t.Fatalf("期望 1 件产物，得到 %d", len(idx.Artifacts))
	}
	art := idx.Artifacts[0]
	if art.Type != "architecture" || art.Slug != "web-app" || art.Stamp != "20261106-120000" {
		t.Errorf("目录解析不符：%+v", art)
	}
	if art.Title != "Web App 架构" || art.Subtitle != "三层" || art.Quality != "showcase" {
		t.Errorf("candidate meta 未消费：%+v", art)
	}
	if art.Repository != "https://gitlab.wamdy.com/wamdy.com/appollo" {
		t.Errorf("repository 未去 .git：%q", art.Repository)
	}
	if art.Delivery != "current" {
		t.Errorf("delivery = %q，期望 current", art.Delivery)
	}
	if art.RelPath != "architecture-web-app-20261106-120000" {
		t.Errorf("rel_path = %q", art.RelPath)
	}
	if art.Size == 0 || art.ModTime == 0 {
		t.Errorf("size/mtime 未取到：%d / %d", art.Size, art.ModTime)
	}
	if art.ID == "" || len(art.ID) != 12 {
		t.Errorf("ID 形态不符：%q", art.ID)
	}
	if _, ok := store.Path(art.ID); !ok {
		t.Errorf("白名单未收录 ID %q", art.ID)
	}
	if idx.ByType["architecture"] != 1 || idx.Roots[0].Count != 1 {
		t.Errorf("计数不符：%+v roots=%+v", idx.ByType, idx.Roots)
	}
}

func TestScanWarningCodes(t *testing.T) {
	f := newFixture(t)
	f.put(t, "mystery-dir", "only.html", "", "")                             // DIRNAME-UNPARSED + CANDIDATE-MISSING + SIDECAR-MISSING
	f.put(t, "tree-no-meta-20261106-120000", "no-meta.html", "", "")         // CANDIDATE-MISSING
	f.put(t, "class-broken-20261106-120000", "broken.html", `{not json`, "") // CANDIDATE-INVALID
	f.put(t, "erd-two-20261106-120000", "first.html", "", "")                // 两张 HTML → HTML-AMBIGUOUS
	f.put(t, "erd-two-20261106-120000", "second.html", "", `{"status":"failed"}`)
	f.put(t, "lifecycle-bad-sidecar-20261106-120000", "life.html", "", `{"status":`)
	// 有目录、无 HTML：不是产物，也不该计警告。
	noise := filepath.Join(f.root, "not-an-artifact-dir")
	if err := os.MkdirAll(noise, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noise, "read.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, _ := f.scan(t)
	got := map[string]map[string]bool{}
	for _, art := range idx.Artifacts {
		got[art.DirName()] = map[string]bool{}
		for _, w := range art.Warnings {
			got[art.DirName()][w] = true
		}
	}

	assertWarn := func(dir string, codes ...string) {
		t.Helper()
		for _, code := range codes {
			if !got[dir][code] {
				t.Errorf("%s 缺警告码 %s（实际 %v）", dir, code, keys(got[dir]))
			}
		}
	}
	assertWarn("mystery-dir", "DIRNAME-UNPARSED", "CANDIDATE-MISSING", "SIDECAR-MISSING")
	assertWarn("tree-no-meta-20261106-120000", "CANDIDATE-MISSING")
	assertWarn("class-broken-20261106-120000", "CANDIDATE-INVALID")
	assertWarn("erd-two-20261106-120000", "HTML-AMBIGUOUS")
	assertWarn("lifecycle-bad-sidecar-20261106-120000", "SIDECAR-INVALID")

	if _, dup := got["not-an-artifact-dir"]; dup {
		t.Error("无 HTML 的目录不该被当作产物")
	}
	if idx.Artifacts[findDirIdx(t, idx, "lifecycle-bad-sidecar-20261106-120000")].Delivery != "unknown" {
		t.Error("坏 sidecar 应降级为 unknown 而不是崩溃")
	}
	if idx.ParseWarnings == 0 {
		t.Error("ParseWarnings 应汇总警告条数")
	}
}

func findDirIdx(t *testing.T, idx *Index, dirName string) int {
	t.Helper()
	for i, art := range idx.Artifacts {
		if art.DirName() == dirName {
			return i
		}
	}
	t.Fatalf("未索引到 %s", dirName)
	return -1
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestMetaOutputHonoured(t *testing.T) {
	f := newFixture(t)
	dir := f.put(t, "workflow-deploy-20261106-120000", "candidate-draft.html",
		`{"meta":{"title":"发布流","output":"deploy.html"}}`, "")
	if err := os.WriteFile(filepath.Join(dir, "deploy.html"), []byte("<!doctype html><title>final</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, _ := f.scan(t)
	if idx.Artifacts[0].HTMLName != "deploy.html" {
		t.Errorf("slug.html 不存在时应采纳 meta.output，得到 %q", idx.Artifacts[0].HTMLName)
	}
}

// 评审 QUESTION-1 裁决：文档序是 <slug>.html > meta.output，两者并存时必须由 slug 命中。
func TestSlugHTMLBeatsMetaOutput(t *testing.T) {
	f := newFixture(t)
	dir := f.put(t, "workflow-deploy-20261106-120000", "deploy.html",
		`{"meta":{"title":"发布流","output":"other.html"}}`, "")
	if err := os.WriteFile(filepath.Join(dir, "other.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 让 other.html 更新，确保不是靠 mtime 侥幸选中 deploy.html。
	newer := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "other.html"), newer, newer); err != nil {
		t.Fatal(err)
	}
	idx, _ := f.scan(t)
	art := idx.Artifacts[0]
	if art.HTMLName != "deploy.html" {
		t.Errorf("主件次序应为 <slug>.html 优先，得到 %q", art.HTMLName)
	}
	// sidecar 跟随主件名，选错主件会连带把交付态读成 unknown。
	if art.Delivery != "unknown" {
		t.Errorf("本例无 sidecar，应为 unknown，得到 %q", art.Delivery)
	}
}

// 评审 BLOCK-2：ROOT-UNREADABLE 只能出规则码，原始错误（含根绝对路径）不得进 JSON。
func TestRootErrorJSONCarriesNoAbsolutePaths(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	cfg := &config.Config{Roots: []config.Root{{Label: "bad", Path: missing}}}
	idx, _ := Scan(cfg)
	raw, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Roots[0].Err != "ROOT-UNREADABLE" {
		t.Errorf("Err 应为规则码，得到 %q", idx.Roots[0].Err)
	}
	for _, leak := range []string{missing, filepath.VolumeName(missing), t.TempDir()} {
		if leak != "" && strings.Contains(string(raw), leak) {
			t.Errorf("响应里泄出绝对路径 %q：%s", leak, raw)
		}
	}
}

func TestSlugNamedHTMLPreferredOverAmbiguity(t *testing.T) {
	f := newFixture(t)
	dir := f.put(t, "sequence-api-20261106-120000", "extra.html", `{"meta":{"title":"x"}}`, "")
	if err := os.WriteFile(filepath.Join(dir, "api.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, _ := f.scan(t)
	art := idx.Artifacts[0]
	if art.HTMLName != "api.html" {
		t.Errorf("应按 <slug>.html 命中主件，得到 %q", art.HTMLName)
	}
	for _, w := range art.Warnings {
		if w == "HTML-AMBIGUOUS" {
			t.Error("同名主件存在时不该报歧义")
		}
	}
}

func TestScanIsReadOnly(t *testing.T) {
	f := newFixture(t)
	f.put(t, "architecture-keep-20261106-120000", "keep.html", `{"meta":{"title":"保持原样"}}`, `{"status":"current"}`)
	f.put(t, "tree-keep2-20261106-120000", "keep2.html", "", "")

	before := map[string]string{}
	err := filepath.Walk(f.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		sum, serr := SHA256File(path)
		if serr != nil {
			return serr
		}
		before[path] = sum
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	idx, store := f.scan(t)
	for _, art := range idx.Artifacts {
		if _, err := os.ReadFile(art.htmlPath); err != nil {
			t.Fatal(err)
		}
		if _, ok := store.Path(art.ID); !ok {
			t.Fatalf("白名单缺 %s", art.ID)
		}
	}

	after := map[string]string{}
	if err := filepath.Walk(f.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		sum, serr := SHA256File(path)
		if serr != nil {
			return serr
		}
		after[path] = sum
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("红线 1 违例：扫描/打开后 %s 内容变了", path)
		}
	}
	if len(before) != len(after) {
		t.Errorf("文件数变化：%d → %d（不得新增或删除取证文件）", len(before), len(after))
	}
}

func TestIDSufficientlyStableAndDistinct(t *testing.T) {
	f := newFixture(t)
	// 同 slug 多版本 = 不同目录 ⇒ 不同 ID。
	f.put(t, "tree-payments-20261106-120000", "payments.html", `{"meta":{"title":"v1"}}`, "")
	f.put(t, "tree-payments-20261106-130000", "payments.html", `{"meta":{"title":"v2"}}`, "")
	idx1, store1 := f.scan(t)
	idx2, _ := f.scan(t)

	if len(idx1.Artifacts) != 2 {
		t.Fatalf("同 slug 两版本应各自成件，得到 %d", len(idx1.Artifacts))
	}
	if idx1.Artifacts[0].ID == idx1.Artifacts[1].ID {
		t.Fatal("两件产物的 ID 撞了")
	}
	byID := map[string]string{}
	for _, art := range idx1.Artifacts {
		byID[art.ID] = art.Title
	}
	for _, art := range idx2.Artifacts {
		if byID[art.ID] != art.Title {
			t.Errorf("重启后 ID 变化，深链会失效：%s", art.ID)
		}
	}
	if len(store1.byID) != 2 {
		t.Errorf("白名单条数 %d", len(store1.byID))
	}
}

func TestSortedByMtimeDesc(t *testing.T) {
	f := newFixture(t)
	f.put(t, "architecture-old-20260101-000000", "old.html", "", "")
	newDir := f.put(t, "architecture-new-20260102-000000", "new.html", "", "")
	if err := os.Chtimes(filepath.Join(newDir, "new.html"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	idx, _ := f.scan(t)
	if idx.Artifacts[0].Slug != "new" {
		t.Errorf("首件应是最近修改的，得到 %q", idx.Artifacts[0].Slug)
	}
}

func TestJSONExcludesAbsolutePaths(t *testing.T) {
	f := newFixture(t)
	f.put(t, "architecture-secret-20261106-120000", "secret.html", `{"meta":{"title":"机密图纸"}}`, "")
	idx, _ := f.scan(t)
	raw, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	// VolumeName 在 Unix 上是空串，strings.Contains(body, "") 恒真 ⇒ 不加守卫这条测试
	// 在 Linux/macOS 上必红（评审 SUGGEST；build-dist.sh 要出三平台，不能只在 Windows 绿）。
	for _, abs := range []string{f.root, filepath.VolumeName(f.root)} {
		if abs == "" {
			continue
		}
		if strings.Contains(body, strings.TrimSuffix(abs, string(os.PathSeparator))) {
			t.Errorf("绝对路径泄进 JSON 响应：%s", body)
		}
	}
	if !strings.Contains(body, `"rel_path"`) {
		t.Error("rel_path 应作为对外唯一路径口径")
	}
}

func TestRootUnreadableRecordsErrorAndContinues(t *testing.T) {
	good := t.TempDir()
	bad := filepath.Join(t.TempDir(), "gone")
	f := &fixture{root: good}
	f.put(t, "architecture-ok-20261106-120000", "ok.html", "", "")

	idx, _ := Scan(&config.Config{Roots: []config.Root{{Label: "good", Path: good}, {Label: "bad", Path: bad}}})
	if len(idx.Artifacts) != 1 {
		t.Errorf("一处根不可读不应影响另一处：%+v", idx.Artifacts)
	}
	var found bool
	for _, r := range idx.Roots {
		if r.Label == "bad" {
			found = true
			if r.Err == "" {
				t.Error("不可读的根必须记 Err")
			}
		}
	}
	if !found {
		t.Error("roots 状态里缺 bad")
	}
}

func TestFindResolution(t *testing.T) {
	f := newFixture(t)
	f.put(t, "architecture-alpha-20261106-120000", "alpha.html", `{"meta":{"title":"Alpha 系统"}}`, "")
	f.put(t, "architecture-alphabet-20261106-120001", "alphabet.html", `{"meta":{"title":"Alphabet 字母表"}}`, "")
	_, store := f.scan(t)
	all := store.All()

	if _, err := store.Find(all[0].ID); err != nil {
		t.Errorf("完整 ID 必须命中: %v", err)
	}
	if _, err := store.Find("zzz-不存在"); err != ErrNotFound {
		t.Errorf("无命中应返回 ErrNotFound，得到 %v", err)
	}
	// slug 子串命中多件 ⇒ 明确歧义错误并给出候选，绝不猜。
	_, err := store.Find("alpha")
	var amb *ErrAmbiguous
	if err == nil || !asAmbiguous(err, &amb) {
		t.Fatalf("多命中应报歧义，得到 %T %v", err, err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("候选数 %d", len(amb.Candidates))
	}
	if art, err := store.Find("Alphabet"); err != nil || art.Slug != "alphabet" {
		t.Errorf("大小写不敏感子串命中失败: %v", err)
	}
}

func asAmbiguous(err error, out **ErrAmbiguous) bool {
	amb, ok := err.(*ErrAmbiguous)
	if ok {
		*out = amb
	}
	return ok
}
