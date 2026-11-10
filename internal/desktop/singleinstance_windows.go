//go:build windows

package desktop

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// guiMutexName 用 Local\ 前缀把单实例范围限定在当前登录会话，避免跨会话/服务误判。
const guiMutexName = `Local\archify-viewer-gui`

// errorAlreadyExists 是 CreateMutexW 命中已有对象时的 GetLastError（ERROR_ALREADY_EXISTS）。
// x/sys 的 CreateMutex 包装只在句柄为 0 时返回 err，不暴露该码，故直接用原始 Call 取第三返回值。
const errorAlreadyExists = syscall.Errno(183)

var (
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procCloseHandle  = kernel32.NewProc("CloseHandle")
)

// AcquireGUISingleInstance 抢占 gui 单实例命名 mutex。
// 返回 (release, acquired, err)：acquired=false 表示已有实例在跑；err!=nil 表示系统调用异常，
// 调用方应降级放行（宁可多开也不因守卫故障而完全打不开）。release 关闭句柄即释放，进程退出时 OS 也会自动回收。
func AcquireGUISingleInstance() (func(), bool, error) {
	return acquireNamedMutex(guiMutexName)
}

func acquireNamedMutex(name string) (func(), bool, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, err
	}
	h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return nil, false, fmt.Errorf("创建单实例 mutex 失败: %w", callErr)
	}
	if callErr == errorAlreadyExists {
		procCloseHandle.Call(h)
		return nil, false, nil
	}
	return func() { procCloseHandle.Call(h) }, true, nil
}
