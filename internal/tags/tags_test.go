package tags

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeDropsInvalidAndDedupes(t *testing.T) {
	got := Normalize([]string{
		" 架构/平台 ", "/架构/平台/", "b", "a", "a/b",
		"", "/", "//", "a//b",
		"a/b/c/d/e",             // 超过 MaxDepth
		strings.Repeat("x", 61), // 超过 MaxLen
		"a\x01b",                // 控制字符
	})
	want := []string{"a", "a/b", "b", "架构/平台"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Normalize = %v, want %v", got, want)
	}
}

func TestNormalizeCapsPerItem(t *testing.T) {
	in := make([]string, 0, MaxPerItem+5)
	for i := 0; i < MaxPerItem+5; i++ {
		in = append(in, fmt.Sprintf("t/t%02d", i))
	}
	if got := Normalize(in); len(got) != MaxPerItem {
		t.Fatalf("len = %d, want %d", len(got), MaxPerItem)
	}
}

func TestSetGetAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Set("architecture/login", []string{"架构/平台", "架构/平台", " 权限 "})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "权限|架构/平台" {
		t.Fatalf("Set = %v", got)
	}
	if tags := s.Get("architecture/login"); len(tags) != 2 {
		t.Fatalf("Get = %v", tags)
	}

	reload, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if tags := reload.Get("architecture/login"); len(tags) != 2 {
		t.Fatalf("落盘后重读 = %v", tags)
	}
	if runtime.GOOS != "windows" { // Windows 不保留 Unix 权限位，断言只在 POSIX 生效
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("权限过宽: %o", st.Mode().Perm())
		}
	}
}

func TestSetEmptyDeletesEntry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, _ := NewStore(p)
	if _, err := s.Set("workflow/deploy", []string{"流程"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("workflow/deploy", nil); err != nil {
		t.Fatal(err)
	}
	if tags := s.Get("workflow/deploy"); len(tags) != 0 {
		t.Fatalf("空列表应删除，实际 %v", tags)
	}
	reload, _ := NewStore(p)
	if _, ok := reload.Snapshot()["workflow/deploy"]; ok {
		t.Error("删除未持久化")
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "tags.yaml"))
	for _, k := range []string{"", "onlyone", "a/b/c", "a/\x01", "/b", "a/"} {
		if _, err := s.Set(k, []string{"x"}); err == nil {
			t.Errorf("key %q 应被拒绝", k)
		}
	}
}

func TestGCRemovesOrphans(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, _ := NewStore(p)
	s.Set("architecture/keep", []string{"a"})
	s.Set("architecture/drop", []string{"b"})
	n, err := s.GC(map[string]struct{}{"architecture/keep": {}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
	snap := s.Snapshot()
	if _, ok := snap["architecture/drop"]; ok {
		t.Error("孤儿未清理")
	}
	reload, _ := NewStore(p)
	if _, ok := reload.Snapshot()["architecture/drop"]; ok {
		t.Error("孤儿清理未持久化")
	}
}

func TestSnapshotIsolation(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "tags.yaml"))
	s.Set("erd/core", []string{"数据"})
	snap := s.Snapshot()
	snap["erd/core"][0] = "被篡改"
	snap["erd/new"] = []string{"x"}
	if got := s.Get("erd/core"); got[0] != "数据" {
		t.Errorf("快照污染了内部状态: %v", got)
	}
	if _, ok := s.Snapshot()["erd/new"]; ok {
		t.Error("快照写入泄漏进存储")
	}
}

func TestCorruptFileReportsError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	if err := os.WriteFile(p, []byte("artifacts: {a: [1, 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(p); err == nil {
		t.Fatal("损坏文件应返回错误")
	}
}

func TestNilStoreIsSafe(t *testing.T) {
	var s *Store
	if s.Get("a/b") != nil {
		t.Error("nil Get")
	}
	if s.Snapshot() != nil {
		t.Error("nil Snapshot")
	}
	if _, err := s.Set("a/b", []string{"x"}); err == nil {
		t.Error("nil Set 应报错")
	}
	if n, err := s.GC(nil); n != 0 || err != nil {
		t.Error("nil GC")
	}
	if s.Path() != "" {
		t.Error("nil Path")
	}
}

func TestKeyStableAcrossDeliveries(t *testing.T) {
	// 同一 type+slug、不同交付时间戳 → 同一个键（标签继承）。
	a := Key("architecture", "appollo-repo")
	b := Key(" architecture ", " appollo-repo ")
	if a != b || a != "architecture/appollo-repo" {
		t.Fatalf("Key = %q vs %q", a, b)
	}
}
