//go:build windows

// Package winapi wraps the handful of Win32 APIs the monitor needs (tray icon,
// windows, menus, dialogs, registry, network drives) using plain syscalls –
// no cgo, so the exe can be cross-compiled from any OS.
package winapi

import (
	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	mpr      = windows.NewLazySystemDLL("mpr.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")

	pRegisterClassExW         = user32.NewProc("RegisterClassExW")
	pCreateWindowExW          = user32.NewProc("CreateWindowExW")
	pDefWindowProcW           = user32.NewProc("DefWindowProcW")
	pDestroyWindow            = user32.NewProc("DestroyWindow")
	pShowWindow               = user32.NewProc("ShowWindow")
	pUpdateWindow             = user32.NewProc("UpdateWindow")
	pSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	pBringWindowToTop         = user32.NewProc("BringWindowToTop")
	pGetMessageW              = user32.NewProc("GetMessageW")
	pTranslateMessage         = user32.NewProc("TranslateMessage")
	pDispatchMessageW         = user32.NewProc("DispatchMessageW")
	pPostMessageW             = user32.NewProc("PostMessageW")
	pPostQuitMessage          = user32.NewProc("PostQuitMessage")
	pCreatePopupMenu          = user32.NewProc("CreatePopupMenu")
	pAppendMenuW              = user32.NewProc("AppendMenuW")
	pTrackPopupMenu           = user32.NewProc("TrackPopupMenu")
	pDestroyMenu              = user32.NewProc("DestroyMenu")
	pGetCursorPos             = user32.NewProc("GetCursorPos")
	pFindWindowExW            = user32.NewProc("FindWindowExW")
	pRegisterWindowMessageW   = user32.NewProc("RegisterWindowMessageW")
	pGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	pGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
	pCreateIconIndirect       = user32.NewProc("CreateIconIndirect")
	pDestroyIcon              = user32.NewProc("DestroyIcon")
	pLoadImageW               = user32.NewProc("LoadImageW")
	pSetWindowPos             = user32.NewProc("SetWindowPos")
	pGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	pGetDpiForSystem          = user32.NewProc("GetDpiForSystem")
	pGetClientRect            = user32.NewProc("GetClientRect")
	pIsWindowVisible          = user32.NewProc("IsWindowVisible")
	pIsIconic                 = user32.NewProc("IsIconic")
	pMessageBoxW              = user32.NewProc("MessageBoxW")
	pSystemParametersInfoW    = user32.NewProc("SystemParametersInfoW")
	pSetWindowTextW           = user32.NewProc("SetWindowTextW")
	pGetWindowPlacement       = user32.NewProc("GetWindowPlacement")
	pLoadCursorW              = user32.NewProc("LoadCursorW")
	pAllowSetForegroundWindow = user32.NewProc("AllowSetForegroundWindow")

	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW    = shell32.NewProc("ShellExecuteW")

	pCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap     = gdi32.NewProc("CreateBitmap")
	pDeleteObject     = gdi32.NewProc("DeleteObject")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pAttachConsole    = kernel32.NewProc("AttachConsole")

	pWNetGetConnectionW = mpr.NewProc("WNetGetConnectionW")

	pGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
	pGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")

	pDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

// Window messages and constants.
const (
	WM_NULL            = 0x0000
	WM_CREATE          = 0x0001
	WM_DESTROY         = 0x0002
	WM_MOVE            = 0x0003
	WM_SIZE            = 0x0005
	WM_ACTIVATE        = 0x0006
	WM_SETFOCUS        = 0x0007
	WM_CLOSE           = 0x0010
	WM_QUIT            = 0x0012
	WM_ENDSESSION      = 0x0016
	WM_QUERYENDSESSION = 0x0011
	WM_GETMINMAXINFO   = 0x0024
	WM_MOVING          = 0x0216
	WM_DPICHANGED      = 0x02E0
	WM_COMMAND         = 0x0111
	WM_CONTEXTMENU     = 0x007B
	WM_LBUTTONUP       = 0x0202
	WM_LBUTTONDBLCLK   = 0x0203
	WM_RBUTTONUP       = 0x0205
	WM_USER            = 0x0400
	WM_APP             = 0x8000

	SW_HIDE       = 0
	SW_SHOWNORMAL = 1
	SW_SHOW       = 5
	SW_RESTORE    = 9

	WS_OVERLAPPEDWINDOW = 0x00CF0000
	CW_USEDEFAULT       = 0x80000000

	MF_STRING    = 0x0000
	MF_SEPARATOR = 0x0800
	MF_CHECKED   = 0x0008
	MF_GRAYED    = 0x0001
	MF_DEFAULT   = 0x1000

	TPM_RIGHTBUTTON = 0x0002
	TPM_RETURNCMD   = 0x0100
	TPM_BOTTOMALIGN = 0x0020

	SM_CXSMICON = 49
	SM_CYSMICON = 50
	SM_CXICON   = 11

	MB_OK              = 0x0000
	MB_YESNO           = 0x0004
	MB_ICONERROR       = 0x0010
	MB_ICONWARNING     = 0x0030
	MB_ICONINFORMATION = 0x0040
	MB_SETFOREGROUND   = 0x00010000
	IDYES              = 6

	SWP_NOZORDER   = 0x0004
	SWP_NOACTIVATE = 0x0010
)

// HWND_MESSAGE is the parent for message-only windows.
const HWND_MESSAGE = ^uintptr(2) // (HWND)-3

type POINT struct{ X, Y int32 }

type RECT struct{ Left, Top, Right, Bottom int32 }

type MSG struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
	_       uint32
}

type WNDCLASSEXW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type MINMAXINFO struct {
	PtReserved     POINT
	PtMaxSize      POINT
	PtMaxPosition  POINT
	PtMinTrackSize POINT
	PtMaxTrackSize POINT
}

func loWord(v uintptr) uint16 { return uint16(v & 0xffff) }
func hiWord(v uintptr) uint16 { return uint16((v >> 16) & 0xffff) }

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

// ModuleHandle returns the HINSTANCE of the exe.
func ModuleHandle() uintptr {
	h, _, _ := pGetModuleHandleW.Call(0)
	return h
}

// MessageBox shows a native message box and returns the pressed button ID.
func MessageBox(owner uintptr, title, text string, flags uint32) int {
	r, _, _ := pMessageBoxW.Call(owner, uintptr(ptr(utf16(text))), uintptr(ptr(utf16(title))), uintptr(flags|MB_SETFOREGROUND))
	return int(r)
}

// PostMessage posts a message to a window's queue.
func PostMessage(hwnd uintptr, msg uint32, wp, lp uintptr) {
	pPostMessageW.Call(hwnd, uintptr(msg), wp, lp)
}

// RunMessageLoop pumps messages until WM_QUIT.
func RunMessageLoop() {
	var m MSG
	for {
		r, _, _ := pGetMessageW.Call(uintptr(ptr(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(ptr(&m)))
		pDispatchMessageW.Call(uintptr(ptr(&m)))
	}
}

// Quit posts WM_QUIT to the calling thread's queue.
func Quit() { pPostQuitMessage.Call(0) }

// AttachParentConsole lets a GUI-subsystem exe print to the console it was
// started from (used by the --check command line mode).
func AttachParentConsole() bool {
	const ATTACH_PARENT_PROCESS = ^uintptr(0)
	r, _, _ := pAttachConsole.Call(ATTACH_PARENT_PROCESS)
	return r != 0
}
