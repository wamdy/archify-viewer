//go:build windows

package desktop

// Windows 系统托盘：隐藏消息窗口 + Shell_NotifyIconW + 右键菜单 + 主窗口 WM_CLOSE 拦截。
// 骨架借自 ssh-tunnel-ui（纯 Win32，无 CGO、无新依赖），差异：
//   - 默认直接隐藏到托盘，首次隐藏弹一次气泡说明如何恢复；
//   - 托盘菜单增加「重新扫描产物」；
//   - 退出前显式 NIM_DELETE 移除图标并恢复原窗口过程。

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmNull        = 0x0000
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmCommand     = 0x0111
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B
	wmApp         = 0x8000

	swHide = 0
	swShow = 5

	niMessage     = wmApp + 1
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004
	nifInfo       = 0x00000010
	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	niVersion4    = 4
	niifInfo      = 0x00000001

	ninSelect    = 0x0400
	ninKeySelect = 0x0401

	mfString       = 0x00000000
	mfSeparator    = 0x00000800
	tpmReturnCmd   = 0x0100
	tpmRightButton = 0x0002

	imageIcon      = 1
	lrDefaultSize  = 0x40
	idiApplication = 32512

	menuShow   = 1
	menuQuit   = 2
	menuRescan = 3

	dlgTray = 100
	dlgQuit = 101

	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4

	tdfAllowDialogCancellation  = 0x0008
	tdfPositionRelativeToWindow = 0x1000
	tdInformationIcon           = ^uintptr(2) // MAKEINTRESOURCE(-3)

	mbYesNoCancel  = 0x00000003
	mbIconQuestion = 0x00000020
	mbDefButton1   = 0x00000000
	idYes          = 6
	idNo           = 7

	errorClassAlreadyExists = syscall.Errno(1410)
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetWindowLongPtrW   = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW     = user32.NewProc("CallWindowProcW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procMessageBoxW         = user32.NewProc("MessageBoxW")

	procShellNotifyIconW   = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procTaskDialogIndirect = comctl32.NewProc("TaskDialogIndirect")
)

type point struct {
	X int32
	Y int32
}

type msgStruct struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	pt       point
	lPrivate uint32
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type notifyIconData struct {
	cbSize            uint32
	hwnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             uintptr
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          [16]byte
	hBalloonIcon      uintptr
}

type taskDialogButton struct {
	nButtonID     int32
	_             int32
	pszButtonText *uint16
}

// taskDialogConfig 对应 TASKDIALOGCONFIG（x64），字段顺序与对齐必须保持一致。
type taskDialogConfig struct {
	cbSize                  uint32
	_                       uint32
	hwndParent              uintptr
	hInstance               uintptr
	dwFlags                 uint32
	dwCommonButtons         uint32
	pszWindowTitle          *uint16
	hMainIcon               uintptr
	pszMainInstruction      *uint16
	pszContent              *uint16
	cButtons                uint32
	_                       uint32
	pButtons                uintptr
	nDefaultButton          int32
	cRadioButtons           uint32
	pRadioButtons           uintptr
	nDefaultRadioButton     int32
	_                       uint32
	pszVerificationText     *uint16
	pszExpandedInformation  *uint16
	pszExpandedControlText  *uint16
	pszCollapsedControlText *uint16
	hFooterIcon             uintptr
	pszFooter               *uint16
	pfCallback              uintptr
	lpCallbackData          uintptr
	cxWidth                 uint32
	_                       uint32
}

var (
	trayMu          sync.Mutex
	trayOpts        TrayOptions
	trayInstalled   bool
	trayHwnd        uintptr
	mainHwnd        uintptr
	origMainWndProc uintptr
	balloonShown    bool

	trayWndProcCallback = syscall.NewCallback(trayWndProc)
	mainWndProcCallback = syscall.NewCallback(mainWndProc)
)

// EnableTray 创建系统托盘图标并接管主窗口关闭行为。必须在主线程调用。
func (w *Window) EnableTray(opts TrayOptions) error {
	hwnd := uintptr(w.view.Window())
	if hwnd == 0 {
		return fmt.Errorf("原生窗口句柄不可用，无法启用托盘")
	}

	trayMu.Lock()
	if trayInstalled {
		trayMu.Unlock()
		return fmt.Errorf("托盘已启用")
	}
	trayOpts = opts
	mainHwnd = hwnd
	trayMu.Unlock()

	orig, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlpWndProc)
	if orig == 0 {
		return fmt.Errorf("读取窗口过程失败")
	}
	origMainWndProc = orig
	procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, mainWndProcCallback)

	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := createTrayWindow(); err != nil {
			ready <- err
			return
		}
		addTrayIcon()
		ready <- nil
		runMessageLoop()
	}()

	if err := <-ready; err != nil {
		procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, origMainWndProc)
		return err
	}

	trayMu.Lock()
	trayInstalled = true
	trayMu.Unlock()
	return nil
}

// RemoveTray 在进程退出前移除图标，避免 Windows 通知区域留下「幽灵图标」直到鼠标划过。
func RemoveTray() {
	trayMu.Lock()
	if !trayInstalled || trayHwnd == 0 {
		trayMu.Unlock()
		return
	}
	hwnd := trayHwnd
	trayInstalled = false
	trayMu.Unlock()

	nid := notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{})), hwnd: hwnd, uID: 1}
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
	if mainHwnd != 0 && origMainWndProc != 0 {
		procSetWindowLongPtrW.Call(mainHwnd, gwlpWndProc, origMainWndProc)
	}
}

func createTrayWindow() error {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className, err := windows.UTF16PtrFromString("ArchifyTrayWindow")
	if err != nil {
		return err
	}
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   trayWndProcCallback,
		hInstance:     hInstance,
		lpszClassName: className,
	}
	atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && callErr != errorClassAlreadyExists {
		return fmt.Errorf("注册托盘窗口类失败: %v", callErr)
	}
	hwnd, _, callErr := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, 0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("创建托盘窗口失败: %v", callErr)
	}
	trayMu.Lock()
	trayHwnd = hwnd
	trayMu.Unlock()
	return nil
}

func baseNotifyIcon() notifyIconData {
	trayMu.Lock()
	hwnd := trayHwnd
	trayMu.Unlock()
	return notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{})), hwnd: hwnd, uID: 1}
}

func addTrayIcon() {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	// 资源 ID 2 是 .syso 写入的图标组；自定义构建没有资源时回退系统默认图标。
	hIcon, _, _ := procLoadImageW.Call(hInstance, 2, imageIcon, 0, 0, lrDefaultSize)
	if hIcon == 0 {
		hIcon, _, _ = procLoadIconW.Call(0, idiApplication)
	}

	nid := baseNotifyIcon()
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = niMessage
	nid.hIcon = hIcon
	copy(nid.szTip[:], windows.StringToUTF16("Archify 产物库"))
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	nid.uTimeoutOrVersion = niVersion4
	procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid)))
}

// notifyBalloonOnce 只在首次隐藏时提示，避免每次关闭都打扰用户。
func notifyBalloonOnce() {
	trayMu.Lock()
	if balloonShown {
		trayMu.Unlock()
		return
	}
	balloonShown = true
	trayMu.Unlock()

	nid := baseNotifyIcon()
	nid.uFlags = nifInfo
	copy(nid.szInfoTitle[:], windows.StringToUTF16("Archify 产物库"))
	copy(nid.szInfo[:], windows.StringToUTF16("已最小化到系统托盘，双击图标或右键菜单可恢复窗口。"))
	nid.dwInfoFlags = niifInfo
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func runMessageLoop() {
	var msg msgStruct
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func trayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case niMessage:
		// NOTIFYICON_VERSION_4：事件在 lParam 低字，坐标在 wParam。
		switch uint32(lParam) & 0xffff {
		case wmLButtonUp, ninSelect, ninKeySelect:
			showMainWindow()
		case wmRButtonUp, wmContextMenu:
			showTrayMenu()
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return ret
}

// mainWndProc 拦截关闭：按当前设置询问、隐藏到托盘或放行退出。
// 最小化（WM_SYSCOMMAND/SC_MINIMIZE）不处理，保持系统默认行为。
func mainWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if msg == wmClose {
		trayMu.Lock()
		opts := trayOpts
		trayMu.Unlock()

		allowed := false
		hide := false
		switch opts.behavior() {
		case CloseQuit:
			allowed = true
		case CloseTray:
			hide = true
		default:
			switch askCloseAction(hwnd) {
			case dlgTray:
				hide = true
			case dlgQuit:
				allowed = true
			default:
				return 0 // 取消关闭
			}
		}
		if hide {
			procShowWindow.Call(hwnd, swHide)
			notifyBalloonOnce()
		}
		if !allowed {
			return 0
		}
	}
	ret, _, _ := procCallWindowProcW.Call(origMainWndProc, hwnd, msg, wParam, lParam)
	return ret
}

func showMainWindow() {
	if mainHwnd == 0 {
		return
	}
	procShowWindow.Call(mainHwnd, swShow)
	procSetForegroundWindow.Call(mainHwnd)
}

func showTrayMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	showText, _ := windows.UTF16PtrFromString("显示主窗口")
	rescanText, _ := windows.UTF16PtrFromString("重新扫描产物")
	quitText, _ := windows.UTF16PtrFromString("退出")
	procAppendMenuW.Call(hMenu, mfString, menuShow, uintptr(unsafe.Pointer(showText)))
	procAppendMenuW.Call(hMenu, mfString, menuRescan, uintptr(unsafe.Pointer(rescanText)))
	procAppendMenuW.Call(hMenu, mfSeparator, 0, 0)
	procAppendMenuW.Call(hMenu, mfString, menuQuit, uintptr(unsafe.Pointer(quitText)))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 先把隐藏窗口设为前台，否则点击别处时菜单不会消失。
	trayMu.Lock()
	hidden := trayHwnd
	trayMu.Unlock()
	procSetForegroundWindow.Call(hidden)
	cmd, _, _ := procTrackPopupMenu.Call(
		hMenu, tpmReturnCmd|tpmRightButton,
		uintptr(pt.X), uintptr(pt.Y), 0, hidden, 0,
	)
	procPostMessageW.Call(hidden, wmNull, 0, 0)

	trayMu.Lock()
	opts := trayOpts
	trayMu.Unlock()
	switch cmd {
	case menuShow:
		showMainWindow()
	case menuRescan:
		go opts.rescan()
	case menuQuit:
		go opts.quit()
	}
}

// askCloseAction 仅在 close_action=ask 时使用：页面内不弹窗，这里用系统任务对话框。
func askCloseAction(parent uintptr) int {
	hInstance, _, _ := procGetModuleHandleW.Call(0)

	title, _ := windows.UTF16PtrFromString("Archify 产物库")
	instruction, _ := windows.UTF16PtrFromString("关闭主窗口？")
	content, _ := windows.UTF16PtrFromString("关闭后查看器仍在后台运行，可从系统托盘图标重新打开。")
	trayText, _ := windows.UTF16PtrFromString("最小化到托盘")
	quitText, _ := windows.UTF16PtrFromString("退出程序")

	buttons := [2]taskDialogButton{
		{nButtonID: dlgTray, pszButtonText: trayText},
		{nButtonID: dlgQuit, pszButtonText: quitText},
	}
	cfg := taskDialogConfig{
		hwndParent:         parent,
		hInstance:          hInstance,
		dwFlags:            tdfAllowDialogCancellation | tdfPositionRelativeToWindow,
		pszWindowTitle:     title,
		hMainIcon:          tdInformationIcon,
		pszMainInstruction: instruction,
		pszContent:         content,
		cButtons:           2,
		pButtons:           uintptr(unsafe.Pointer(&buttons[0])),
		nDefaultButton:     dlgTray,
	}
	cfg.cbSize = uint32(unsafe.Sizeof(cfg))

	var pressed int32
	hr, _, _ := procTaskDialogIndirect.Call(
		uintptr(unsafe.Pointer(&cfg)),
		uintptr(unsafe.Pointer(&pressed)),
		0, 0,
	)
	if int32(hr) >= 0 {
		return int(pressed)
	}

	// TaskDialog 不可用时退回系统消息框，保证关闭流程仍可选择。
	text, _ := windows.UTF16PtrFromString("是：最小化到托盘\n否：退出程序\n取消：保持窗口")
	ret, _, _ := procMessageBoxW.Call(
		parent,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		mbYesNoCancel|mbIconQuestion|mbDefButton1,
	)
	switch ret {
	case idYes:
		return dlgTray
	case idNo:
		return dlgQuit
	default:
		return 0
	}
}
