package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSettingsConcurrencyLogicalSnapshot 是 `go test -race` 在本机无 gcc / CGO_ENABLED=0
// 时的**逻辑层替代**：并发写设置 × 并发读运行时快照，验证不 panic、快照自洽、值域收敛。
//
// 口径边界（必须如实登记）：本用例不启用 race detector，**不能证明不存在数据竞争**；
// 它只证明「多写多读交错下不崩、不产生越界/空路径/非法关闭行为」。
// 真正的 race 门需在有 gcc 的机器上以 CGO_ENABLED=1 go test -race ./... 复跑。
func TestSettingsConcurrencyLogicalSnapshot(t *testing.T) {
	dir := t.TempDir()
	roots := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		p := filepath.Join(dir, fmt.Sprintf("root-%d", i))
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, p)
	}

	cfg := &Config{
		GUI:        GUI{Addr: DefaultGUIAddr, CloseAction: DefaultCloseAction},
		RootSource: "none",
	}
	editor, err := NewEditor(cfg, filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	const (
		workers = 8
		rounds  = 12
	)
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds*4)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				p := roots[(w+r)%len(roots)]
				switch (w + r) % 3 {
				case 0:
					// 重复路径、路径不存在属预期校验失败，不计入并发缺陷。
					_, _ = editor.AddRoot(p)
				case 1:
					_, _ = editor.RemoveRoot(p)
				case 2:
					values := []string{"ask", "tray", "quit", "bogus"}
					if _, err := editor.SetCloseAction(values[(w+r)%len(values)]); err != nil {
						errs <- fmt.Errorf("SetCloseAction 失败: %w", err)
					}
				}

				snap := cfg.RootSnapshot()
				if len(snap) > maxRoots {
					errs <- fmt.Errorf("快照超出上限: %d", len(snap))
				}
				for _, root := range snap {
					if root.Path == "" {
						errs <- fmt.Errorf("空路径进入快照")
					}
				}
				switch cfg.CloseActionValue() {
				case "ask", "tray", "quit":
				default:
					errs <- fmt.Errorf("非法关闭行为进入运行时: %q", cfg.CloseActionValue())
				}
				if view := editor.Settings(); len(view.Roots) > maxRoots {
					errs <- fmt.Errorf("设置快照超出上限: %d", len(view.Roots))
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
