//go:build windows

package main

import (
	"log"
	"os"
	"syscall"
	"unsafe"
)

// 只用 stdlib syscall 做控制台附着：引入 golang.org/x/sys 会违反 design D-6 的依赖面约束，
// 而这里需要的四个 kernel32 入口 stdlib 完全够用。
var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
	procAllocConsole  = kernel32.NewProc("AllocConsole")
	procGetStdHandle  = kernel32.NewProc("GetStdHandle")
	procSetStdHandle  = kernel32.NewProc("SetStdHandle")
	user32            = syscall.NewLazyDLL("user32.dll")
	procMessageBox    = user32.NewProc("MessageBoxW")
)

const (
	attachParentProcess = ^uint32(0) // ATTACH_PARENT_PROCESS
	// Go 官方定义（syscall/types_windows.go:132-134）：
	// STD_INPUT_HANDLE=-10、STD_OUTPUT_HANDLE=-11、STD_ERROR_HANDLE=-12。
	// 转成 uintptr 是 fff6/fff5/fff4 —— 输入与输出只差一位，写反过一次（评审 BLOCK-1），
	// 反直觉到必须在代码里钉住来历。
	stdInputHandle  = uintptr(0xfffffff6) // -10
	stdOutputHandle = uintptr(0xfffffff5) // -11
	stdErrorHandle  = uintptr(0xfffffff4) // -12
	mbOK            = 0x0
	mbIconError     = 0x10
)

var consoleReady bool

// ensureConsole 让 windowsgui 子系统产物在命令行调用时仍有输入输出：
// 标准句柄已经有效（管道/重定向）就完全不碰控制台，否则附着父控制台，没有父控制台时新建一个。
func ensureConsole() {
	inOK := stdHandleOK(stdInputHandle)
	outOK := stdHandleOK(stdOutputHandle)
	errOK := stdHandleOK(stdErrorHandle)
	if inOK && outOK {
		consoleReady = true
		return
	}
	if r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); r == 0 {
		if r, _, _ = procAllocConsole.Call(); r == 0 {
			return
		}
	}
	consoleReady = true
	if !inOK {
		reopenStd(stdInputHandle, "CONIN$", os.O_RDONLY)
	}
	if !outOK {
		reopenStd(stdOutputHandle, "CONOUT$", os.O_WRONLY)
	}
	if !errOK {
		reopenStd(stdErrorHandle, "CONOUT$", os.O_WRONLY)
	}
	log.SetOutput(os.Stderr)
}

func stdHandleOK(which uintptr) bool {
	ret, _, _ := procGetStdHandle.Call(which)
	h := syscall.Handle(ret)
	return h != 0 && h != syscall.InvalidHandle
}

func reopenStd(which uintptr, name string, flags int) {
	f, err := os.OpenFile(name, flags, 0)
	if err != nil {
		return
	}
	procSetStdHandle.Call(which, uintptr(f.Fd()))
	switch which {
	case stdInputHandle:
		os.Stdin = f
	case stdOutputHandle:
		os.Stdout = f
	default:
		os.Stderr = f
	}
}

// reportFatal 用于启动阶段错误：双击启动（无控制台）时弹窗，避免「双击后静默退出」无处可查。
func reportFatal(err error) {
	if err == nil {
		return
	}
	if consoleReady {
		log.Fatal(err)
	}
	text, _ := syscall.UTF16PtrFromString(err.Error())
	title, _ := syscall.UTF16PtrFromString("Archify Viewer")
	procMessageBox.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbOK|mbIconError)
	os.Exit(1)
}
