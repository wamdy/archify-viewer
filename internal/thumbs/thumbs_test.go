package thumbs

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// pngBytes 生成一段结构合法的 PNG 头（魔数 + IHDR + 尺寸），足够通过 isValidPNG。
// 不追求可解码：本包只做结构自检，不解码像素。
func pngBytes(w, h uint32) []byte {
	b := make([]byte, 33)
	copy(b, pngMagic)
	b[8], b[9], b[10], b[11] = 0, 0, 0, 13
	copy(b[12:16], "IHDR")
	binary.BigEndian.PutUint32(b[16:20], w)
	binary.BigEndian.PutUint32(b[20:24], h)
	return b
}

type fakeCapturer struct {
	calls int32
	gate  chan struct{}
	fn    func(ctx context.Context, url string) ([]byte, error)
}

func (f *fakeCapturer) Capture(ctx context.Context, url string) ([]byte, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fn != nil {
		return f.fn(ctx, url)
	}
	return pngBytes(1280, 800), nil
}

func TestHitDoesNotCaptureAgain(t *testing.T) {
	dir := t.TempDir()
	f := &fakeCapturer{}
	s := NewStore(dir, f, nil)

	first, err := s.GetOrCreate(context.Background(), "abc-1-2", "http://127.0.0.1:1/a/abc")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.GetOrCreate(context.Background(), "abc-1-2", "http://127.0.0.1:1/a/abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&f.calls); got != 1 {
		t.Errorf("命中后仍采集：calls=%d", got)
	}
	if string(first) != string(second) {
		t.Error("两次返回的字节不一致")
	}
	if _, err := os.Stat(filepath.Join(dir, "abc-1-2.png")); err != nil {
		t.Errorf("缓存未落盘: %v", err)
	}
}

func TestConcurrentSameKeySingleflight(t *testing.T) {
	dir := t.TempDir()
	gate := make(chan struct{})
	f := &fakeCapturer{gate: gate}
	s := NewStore(dir, f, nil)

	const n = 10
	var wg sync.WaitGroup
	results := make([][]byte, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.GetOrCreate(context.Background(), "same-7-8", "http://127.0.0.1:1/a/same")
		}(i)
	}
	// 等到第一个采集真正开始，再放行；期间其余请求必须挂起而不是各采一次。
	for atomic.LoadInt32(&f.calls) == 0 {
		// 自旋等待，避免引入额外同步原语
	}
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt32(&f.calls); got != 1 {
		t.Errorf("同 key 并发应只采集一次，实际 %d", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d err = %v", i, errs[i])
		}
		if string(results[i]) != string(results[0]) {
			t.Errorf("goroutine %d 结果与其他不同", i)
		}
	}
}

func TestDifferentKeysSerialize(t *testing.T) {
	dir := t.TempDir()
	var active, maxActive int32
	f := &fakeCapturer{fn: func(ctx context.Context, url string) ([]byte, error) {
		cur := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}
		// 拉长窗口，制造重叠机会
		for i := 0; i < 2000; i++ {
			_ = i
		}
		atomic.AddInt32(&active, -1)
		return pngBytes(1280, 800), nil
	}}
	s := NewStore(dir, f, nil)

	var wg sync.WaitGroup
	for _, key := range []string{"a-1-1", "b-2-2", "c-3-3"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			if _, err := s.GetOrCreate(context.Background(), k, "http://127.0.0.1:1/a/x"); err != nil {
				t.Errorf("%s: %v", k, err)
			}
		}(key)
	}
	wg.Wait()
	if got := atomic.LoadInt32(&maxActive); got > 1 {
		t.Errorf("跨 key 并发采集（max=%d），应为 1", got)
	}
}

func TestCorruptCacheIsRebuilt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad-1-1.png")
	if err := os.WriteFile(path, []byte("not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeCapturer{}
	s := NewStore(dir, f, nil)
	b, err := s.GetOrCreate(context.Background(), "bad-1-1", "http://127.0.0.1:1/a/bad")
	if err != nil {
		t.Fatal(err)
	}
	if !isValidPNG(b) {
		t.Error("重建结果不是有效 PNG")
	}
	if got := atomic.LoadInt32(&f.calls); got != 1 {
		t.Errorf("坏缓存应触发一次重建，calls=%d", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !isValidPNG(raw) {
		t.Error("坏缓存未被覆盖为有效 PNG")
	}
}

func TestGCOnlyDropsUnknownAndLeftovers(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("keep-1-1.png", pngBytes(10, 10))
	write("stale-2-2.png", pngBytes(10, 10))
	write(".tmp-abc.png", []byte("half"))
	write("notes.txt", []byte("user file"))
	if err := os.MkdirAll(filepath.Join(dir, ".profile-xyz"), 0o700); err != nil {
		t.Fatal(err)
	}

	NewStore(dir, nil, nil).GC(map[string]struct{}{"keep-1-1": {}})

	for _, gone := range []string{"stale-2-2.png", ".tmp-abc.png", ".profile-xyz"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s 应被清理", gone)
		}
	}
	for _, kept := range []string{"keep-1-1.png", "notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, kept)); err != nil {
			t.Errorf("%s 应保留: %v", kept, err)
		}
	}
}

func TestNilAndUnsafeInputs(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.GetOrCreate(context.Background(), "k-1-1", "u"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil store err = %v", err)
	}
	if got := nilStore.Dir(); got != "" {
		t.Errorf("nil store Dir = %q", got)
	}
	nilStore.GC(nil) // 不 panic

	s := NewStore(t.TempDir(), &fakeCapturer{}, nil)
	for _, bad := range []string{"", "../escape", "a/b", "A-1-1", "x-1-1.png"} {
		if _, err := s.GetOrCreate(context.Background(), bad, "u"); !errors.Is(err, ErrFailed) {
			t.Errorf("key %q err = %v, want ErrFailed", bad, err)
		}
	}
	if _, err := NewStore("", nil, nil).GetOrCreate(context.Background(), "k-1-1", "u"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("空目录 err = %v", err)
	}
}

func TestCaptureErrorPropagatesWithoutCache(t *testing.T) {
	dir := t.TempDir()
	f := &fakeCapturer{fn: func(ctx context.Context, url string) ([]byte, error) {
		return nil, ErrUnavailable
	}}
	s := NewStore(dir, f, nil)
	if _, err := s.GetOrCreate(context.Background(), "e-1-1", "u"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "e-1-1.png")); !os.IsNotExist(err) {
		t.Error("失败不应落盘")
	}
}
