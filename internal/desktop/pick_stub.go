//go:build !windows

package desktop

import "errors"

// ErrCancelled 表示用户取消了目录选择。
var ErrCancelled = errors.New("用户取消")

// PickFolder 在非 Windows 平台不可用。
func PickFolder(string) (string, error) {
	return "", errors.New("当前平台不支持原生目录选择")
}
