//go:build windows

package desktop

import (
	"fmt"

	webview "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
)

const (
	minWindowWidth  = 960
	minWindowHeight = 600
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
// IconId=2 指向 .syso 里的图标组：窗口标题栏、任务栏与托盘共用同一枚应用图标。
func New(title, url string, width, height int) (*Window, error) {
	if err := Available(); err != nil {
		return nil, err
	}
	view := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  title,
			Width:  uint(width),
			Height: uint(height),
			Center: true,
			IconId: 2,
		},
	})
	if view == nil {
		return nil, fmt.Errorf("创建原生窗口失败")
	}
	view.SetSize(width, height, webview.HintNone)
	view.SetSize(minWindowWidth, minWindowHeight, webview.HintMin)
	view.Navigate(url)
	return &Window{view: view}, nil
}

// Run 阻塞至窗口关闭。
func (w *Window) Run() {
	w.view.Run()
	w.view.Destroy()
}

// Terminate 从其他 goroutine 请求关闭主循环。
// 必须经 Dispatch 投递到 UI 线程：Terminate 内部是 PostQuitMessage，只对调用者所在线程生效，
// 托盘菜单等后台 goroutine 直接调用会静默丢失（ssh-tunnel-ui 实证，评审常踩）。
func (w *Window) Terminate() { w.view.Dispatch(w.view.Terminate) }

// Close 是托盘「退出」使用的语义化入口。
func (w *Window) Close() { w.Terminate() }
