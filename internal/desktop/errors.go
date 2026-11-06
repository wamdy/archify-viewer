// Package desktop 承担「桌面打开」这一侧：原生 WebView2 窗口（仅 Windows，免 CGO）
// 与系统默认浏览器/文件关联两种回退。
package desktop

import "errors"

// ErrUnsupported 表示当前平台/环境没有原生窗口能力，调用方据此回退系统浏览器。
// 回退语义与 ssh-tunnel-ui 一致（缺 CGO / 缺 WebView2 时不硬失败）。
var ErrUnsupported = errors.New("原生 WebView 窗口不可用")
