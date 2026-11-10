package tags

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 锁原语语义钉住：同进程两个独立句柄也互斥（Windows 字节锁 / POSIX flock 均如此），
// 这是「进程内双 Store 模拟双实例」全部并发用例的前提。
func TestTryLockExcludesSecondHandle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml.lock")
	f1, err := openLockFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f1.Close()
	if ok, err := tryLockFile(f1); err != nil || !ok {
		t.Fatalf("首次加锁失败: ok=%v err=%v", ok, err)
	}
	f2, err := openLockFile(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if ok, err := tryLockFile(f2); err != nil || ok {
		t.Fatalf("第二句柄不应拿到锁: ok=%v err=%v", ok, err)
	}
	unlockFile(f1)
	if ok, err := tryLockFile(f2); err != nil || !ok {
		t.Fatalf("释放后第二句柄应拿到锁: ok=%v err=%v", ok, err)
	}
	unlockFile(f2)
}

// 保存前重读合并：B 的内存视图陈旧时，Set 不得盖掉 A 后写的条目。
func TestSetMergesAcrossStores(t *testing.T) {
	cases := []struct {
		name     string
		seedKey  string
		seedTags []string
		lateKey  string // A 在 B 载入之后又写的键（B 内存已陈旧）
		lateTags []string
		key      string
		in       []string
		want     map[string][]string
	}{
		{
			name: "陈旧实例不覆盖他键", seedKey: "architecture/one", seedTags: []string{"a"},
			lateKey: "workflow/two", lateTags: []string{"b"},
			key: "erd/three", in: []string{"c"},
			want: map[string][]string{"architecture/one": {"a"}, "workflow/two": {"b"}, "erd/three": {"c"}},
		},
		{
			name: "整体替换只动本键", seedKey: "architecture/one", seedTags: []string{"a", "b"},
			key: "architecture/one", in: []string{"c"},
			want: map[string][]string{"architecture/one": {"c"}},
		},
		{
			name: "空列表删除本键不影响他键", seedKey: "architecture/one", seedTags: []string{"a"},
			lateKey: "workflow/two", lateTags: []string{"b"},
			key: "architecture/one", in: nil,
			want: map[string][]string{"workflow/two": {"b"}},
		},
		{
			name: "父子同传只留子级", seedKey: "architecture/one", seedTags: []string{"x"},
			key: "workflow/two", in: []string{"架构", "架构/平台"},
			want: map[string][]string{"architecture/one": {"x"}, "workflow/two": {"架构/平台"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "tags.yaml")
			a, err := NewStore(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.Set(c.seedKey, c.seedTags); err != nil {
				t.Fatal(err)
			}
			b, err := NewStore(p)
			if err != nil {
				t.Fatal(err)
			}
			if c.lateKey != "" {
				if _, err := a.Set(c.lateKey, c.lateTags); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := b.Set(c.key, c.in); err != nil {
				t.Fatal(err)
			}
			reload, err := NewStore(p)
			if err != nil {
				t.Fatal(err)
			}
			got := reload.Snapshot()
			if len(got) != len(c.want) {
				t.Fatalf("合并结果键数 = %d, want %d（%v）", len(got), len(c.want), got)
			}
			for k, v := range c.want {
				if strings.Join(got[k], "|") != strings.Join(v, "|") {
					t.Errorf("键 %s = %v, want %v", k, got[k], v)
				}
			}
		})
	}
}

// 双实例并发写不同键：整文件重写 + 无锁时代必丢条目，锁 + 重读合并后一条不丢。
func TestConcurrentSetsLoseNoEntries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	a, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	const n = 25
	var wg sync.WaitGroup
	errCh := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if _, err := a.Set(fmt.Sprintf("architecture/a%02d", i), []string{"甲"}); err != nil {
				errCh <- err
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if _, err := b.Set(fmt.Sprintf("workflow/b%02d", i), []string{"乙"}); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发保存失败: %v", err)
	}
	reload, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reload.Snapshot()); got != 2*n {
		t.Fatalf("并发保存丢了条目: %d, want %d", got, 2*n)
	}
}

// 同键竞争：整体替换语义下最后写者赢，但文件不得损坏、值不得串。
func TestConcurrentSameKeyLastWriterWins(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	a, _ := NewStore(p)
	b, _ := NewStore(p)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); a.Set("erd/core", []string{"甲"}) }()
		go func() { defer wg.Done(); b.Set("erd/core", []string{"乙"}) }()
	}
	wg.Wait()
	reload, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	got := reload.Get("erd/core")
	if len(got) != 1 || (got[0] != "甲" && got[0] != "乙") {
		t.Fatalf("同键竞争结果被写坏: %v", got)
	}
}

// 锁超时：外部持锁时保存应在 lockWait 后报错退出，不无限等待、不写坏文件；释放后可保存。
func TestFileLockTimeout(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	s.lockWait = 80 * time.Millisecond

	holder, err := openLockFile(p + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := tryLockFile(holder); err != nil || !ok {
		t.Fatalf("外部占锁失败: ok=%v err=%v", ok, err)
	}
	start := time.Now()
	_, err = s.Set("architecture/x", []string{"a"})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("持锁期间保存应报超时错误: %v", err)
	}
	if elapsed < s.lockWait || elapsed > s.lockWait+2*time.Second {
		t.Errorf("超时时机不对: %v（期望约 %s）", elapsed, s.lockWait)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("超时放弃后不应写出文件: %v", err)
	}
	unlockFile(holder)
	holder.Close()
	if _, err := s.Set("architecture/x", []string{"a"}); err != nil {
		t.Fatalf("锁释放后应可保存: %v", err)
	}
}

// 锁粒度：每个 tags.yaml 各自一把锁（同目录 .lock 文件），持 A 的锁不挡 B 保存。
func TestLockGranularityPerFile(t *testing.T) {
	pa := filepath.Join(t.TempDir(), "tags.yaml")
	pb := filepath.Join(t.TempDir(), "tags.yaml")
	sa, _ := NewStore(pa)
	sb, _ := NewStore(pb)
	sa.lockWait = 80 * time.Millisecond

	holder, err := openLockFile(pa + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if ok, err := tryLockFile(holder); err != nil || !ok {
		t.Fatalf("占锁失败: ok=%v err=%v", ok, err)
	}
	defer unlockFile(holder)

	if _, err := sa.Set("architecture/x", []string{"a"}); err == nil {
		t.Error("同路径应被锁挡住")
	}
	if _, err := sb.Set("workflow/y", []string{"b"}); err != nil {
		t.Errorf("不同路径不应被挡: %v", err)
	}
	for _, p := range []string{pa + ".lock", pb + ".lock"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("锁文件应落在 tags.yaml 旁: %s（%v）", p, err)
		}
	}
}

// 写路径（Set / GC / 载入自愈）结束后锁必须已释放，下一次保存不用等超时。
func TestLockReleasedAfterWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, _ := NewStore(p)
	if _, err := s.Set("architecture/x", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	assertLockFree := func(stage string) {
		t.Helper()
		f, err := openLockFile(p + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if ok, err := tryLockFile(f); err != nil || !ok {
			t.Fatalf("%s 后锁未释放: ok=%v err=%v", stage, ok, err)
		}
		unlockFile(f)
	}
	assertLockFree("Set")
	if _, err := s.GC(map[string]struct{}{}); err != nil {
		t.Fatal(err)
	}
	assertLockFree("GC")
	raw := "version: 1\nartifacts:\n  workflow/x:\n    - taga\n    - taga/subtagb\n"
	if err := os.WriteFile(p, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(p); err != nil {
		t.Fatal(err)
	}
	assertLockFree("载入自愈")
}

// 磁盘文件损坏时保存报错而非整文件盖掉（保留现场供排查）。
func TestSetFailsOnCorruptDiskWithoutOverwrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tags.yaml")
	s, _ := NewStore(p)
	corrupt := "artifacts: {a: [1, 2\n"
	if err := os.WriteFile(p, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("architecture/x", []string{"a"}); err == nil {
		t.Fatal("损坏文件上的保存应报错")
	}
	raw, err := os.ReadFile(p)
	if err != nil || string(raw) != corrupt {
		t.Fatalf("损坏文件被覆盖: %q（%v）", raw, err)
	}
}
