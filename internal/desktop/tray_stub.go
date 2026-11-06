//go:build !windows

package desktop

import "fmt"

// EnableTray 在非 Windows 平台没有系统托盘，返回明确错误让调用方回退到普通关闭行为。
func (w *Window) EnableTray(_ TrayOptions) error {
	return fmt.Errorf("%w: 当前平台不支持系统托盘", ErrUnsupported)
}

// RemoveTray 在非 Windows 平台是 no-op。
func RemoveTray() {}
