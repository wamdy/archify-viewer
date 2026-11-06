//go:build windows

package desktop

import (
	"fmt"

	webview "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
)

// Window 是基于 WebView2 的原生窗口，纯 Go 实现、不依赖 CGO（design D-4）。
type Window struct {
	view webview.WebView
}

// Available 先探测运行时再决定是否建窗：未安装 WebView2 时返回可判定的 ErrUnsupported，
// 让调用方走浏览器回退，而不是在 New 里 panic。
func Available() error {
	version, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return fmt.Errorf("检测 WebView2 运行时失败: %w", err)
	}
	if version == "" {
		return fmt.Errorf("%w: 未安装 WebView2 运行时", ErrUnsupported)
	}
	return nil
}

// New 创建窗口并加载 url。必须在主操作系统线程上调用（调用方负责 LockOSThread）。
func New(title, url string, width, height int) (*Window, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	view := webview.New(false)
	view.SetTitle(title)
	view.SetSize(width, height, webview.HintNone)
	view.Navigate(url)
	return &Window{view: view}, nil
}

// Run 阻塞至窗口关闭。
func (w *Window) Run() {
	w.view.Run()
	w.view.Destroy()
}

// Terminate 可从其他 goroutine 请求关闭主循环。
func (w *Window) Terminate() { w.view.Terminate() }
