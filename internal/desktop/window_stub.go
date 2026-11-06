//go:build !windows

package desktop

import "fmt"

// Window 在非 Windows 平台不提供免费的原生窗口：
// webview 的跨平台绑定需要 CGO，会摧毁「CGO_ENABLED=0 也能构建与测试」这条纪律（design D-4/D-6）。
type Window struct{}

func Available() error {
	return fmt.Errorf("%w: 本平台仅支持系统浏览器形态", ErrUnsupported)
}

func New(_, _ string, _, _ int) (*Window, error) { return nil, Available() }

func (w *Window) Run() {}

func (w *Window) Terminate() {}
