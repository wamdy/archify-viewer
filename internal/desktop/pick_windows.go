//go:build windows

package desktop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrCancelled 表示用户取消了目录选择。
var ErrCancelled = errors.New("用户取消")

var (
	modshell32               = windows.NewLazySystemDLL("shell32.dll")
	modole32                 = windows.NewLazySystemDLL("ole32.dll")
	procSHBrowseForFolderW   = modshell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = modshell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree        = modole32.NewProc("CoTaskMemFree")
	procCoInitialize         = modole32.NewProc("CoInitialize")
	procCoUninitialize       = modole32.NewProc("CoUninitialize")
)

// browseInfoW 对应 Win32 BROWSEINFOW。
type browseInfoW struct {
	hwndOwner      windows.Handle
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

const (
	bifReturnOnlyFSDirs = 0x00000001
	bifNewDialogStyle   = 0x00000040
	bifEditBox          = 0x00000010
)

// PickFolder 弹出系统原生目录选择框，返回绝对路径；用户取消返回 ErrCancelled。
// 由 HTTP 处理器调用，因此在调用线程上做一次 CoInitialize/CoUninitialize。
func PickFolder(title string) (string, error) {
	procCoInitialize.Call(0)
	defer procCoUninitialize.Call()

	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return "", err
	}
	display := make([]uint16, windows.MAX_PATH)
	bi := browseInfoW{
		pszDisplayName: &display[0],
		lpszTitle:      t,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle | bifEditBox,
	}
	ret, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if ret == 0 {
		return "", ErrCancelled
	}
	defer procCoTaskMemFree.Call(ret)

	buf := make([]uint16, windows.MAX_PATH)
	ok, _, _ := procSHGetPathFromIDListW.Call(ret, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "", errors.New("无法解析所选目录")
	}
	return windows.UTF16ToString(buf), nil
}
