//go:build windows

package winapi

import (
	"sync"

	"golang.org/x/sys/windows"
)

// WndProcFunc handles a window message; return handled=false to fall back
// to DefWindowProc.
type WndProcFunc func(hwnd uintptr, msg uint32, wp, lp uintptr) (result uintptr, handled bool)

var (
	wndMu       sync.RWMutex
	wndHandlers = map[uintptr]WndProcFunc{}
	pending     WndProcFunc // handler for the window currently being created
	wndProcCB   = windows.NewCallback(globalWndProc)
	classesMu   sync.Mutex
	classes     = map[string]bool{}
)

func globalWndProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	wndMu.RLock()
	h := wndHandlers[hwnd]
	wndMu.RUnlock()
	if h == nil && pending != nil {
		// Messages sent during CreateWindowEx (WM_NCCREATE, WM_CREATE...).
		h = pending
		wndMu.Lock()
		wndHandlers[hwnd] = h
		wndMu.Unlock()
	}
	if h != nil {
		if r, ok := h(hwnd, msg, wp, lp); ok {
			return r
		}
	}
	if msg == WM_DESTROY {
		wndMu.Lock()
		delete(wndHandlers, hwnd)
		wndMu.Unlock()
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, uintptr(msg), wp, lp)
	return r
}

// RegisterClass registers a window class using the shared window procedure.
func RegisterClass(name string, icon, iconSm uintptr) {
	classesMu.Lock()
	defer classesMu.Unlock()
	if classes[name] {
		return
	}
	cursor, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := WNDCLASSEXW{
		LpfnWndProc:   wndProcCB,
		HInstance:     ModuleHandle(),
		HIcon:         icon,
		HIconSm:       iconSm,
		HCursor:       cursor,
		LpszClassName: utf16(name),
	}
	wc.CbSize = uint32(unsafeSizeof(wc))
	pRegisterClassExW.Call(uintptr(ptr(&wc)))
	classes[name] = true
}

// CreateWindow creates a window of a class registered with RegisterClass.
// Must be called on the UI thread.
func CreateWindow(class, title string, exStyle, style uint32, x, y, w, h int32, parent uintptr, proc WndProcFunc) uintptr {
	pending = proc
	hwnd, _, _ := pCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(ptr(utf16(class))),
		uintptr(ptr(utf16(title))),
		uintptr(style),
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, 0, ModuleHandle(), 0)
	pending = nil
	if hwnd != 0 {
		wndMu.Lock()
		wndHandlers[hwnd] = proc
		wndMu.Unlock()
	}
	return hwnd
}

// DestroyWindow destroys a window.
func DestroyWindow(hwnd uintptr) { pDestroyWindow.Call(hwnd) }

// ShowWindow wraps ShowWindow.
func ShowWindow(hwnd uintptr, cmd int) { pShowWindow.Call(hwnd, uintptr(cmd)) }

// IsWindowVisible reports whether the window is visible.
func IsWindowVisible(hwnd uintptr) bool {
	r, _, _ := pIsWindowVisible.Call(hwnd)
	return r != 0
}

// IsIconic reports whether the window is minimized.
func IsIconic(hwnd uintptr) bool {
	r, _, _ := pIsIconic.Call(hwnd)
	return r != 0
}

// BringToFront restores and focuses a window.
func BringToFront(hwnd uintptr) {
	if IsIconic(hwnd) {
		pShowWindow.Call(hwnd, SW_RESTORE)
	} else {
		pShowWindow.Call(hwnd, SW_SHOW)
	}
	pBringWindowToTop.Call(hwnd)
	pSetForegroundWindow.Call(hwnd)
}

// SetForeground calls SetForegroundWindow.
func SetForeground(hwnd uintptr) { pSetForegroundWindow.Call(hwnd) }

// AllowAnyForeground lets another process (the running instance) take the
// foreground when we signal it.
func AllowAnyForeground() {
	const ASFW_ANY = ^uintptr(0)
	pAllowSetForegroundWindow.Call(ASFW_ANY)
}

// SetTitle sets the window caption.
func SetTitle(hwnd uintptr, title string) { pSetWindowTextW.Call(hwnd, uintptr(ptr(utf16(title)))) }

// ClientRect returns the client rectangle.
func ClientRect(hwnd uintptr) RECT {
	var r RECT
	pGetClientRect.Call(hwnd, uintptr(ptr(&r)))
	return r
}

// DPI returns the DPI of a window (96 = 100%).
func DPI(hwnd uintptr) int {
	if pGetDpiForWindow.Find() == nil && hwnd != 0 {
		if r, _, _ := pGetDpiForWindow.Call(hwnd); r != 0 {
			return int(r)
		}
	}
	if pGetDpiForSystem.Find() == nil {
		if r, _, _ := pGetDpiForSystem.Call(); r != 0 {
			return int(r)
		}
	}
	return 96
}

// SetWindowPos moves/resizes a window.
func SetWindowPos(hwnd uintptr, x, y, w, h int32, flags uint32) {
	pSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), uintptr(flags))
}

// WorkArea returns the primary monitor work area.
func WorkArea() RECT {
	const SPI_GETWORKAREA = 0x0030
	var r RECT
	pSystemParametersInfoW.Call(SPI_GETWORKAREA, 0, uintptr(ptr(&r)), 0)
	return r
}

// FindWindow finds a top-level window by class name.
func FindWindow(class string) uintptr {
	r, _, _ := pFindWindowExW.Call(0, 0, uintptr(ptr(utf16(class))), 0)
	return r
}

// RegisterWindowMessage wraps RegisterWindowMessageW.
func RegisterWindowMessage(name string) uint32 {
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(ptr(utf16(name))))
	return uint32(r)
}

// SetCaptionColor colors the title bar on Windows 11 (ignored on Windows 10).
func SetCaptionColor(hwnd uintptr, caption, text uint32) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	const DWMWA_CAPTION_COLOR = 35
	const DWMWA_TEXT_COLOR = 36
	pDwmSetWindowAttribute.Call(hwnd, DWMWA_CAPTION_COLOR, uintptr(ptr(&caption)), 4)
	pDwmSetWindowAttribute.Call(hwnd, DWMWA_TEXT_COLOR, uintptr(ptr(&text)), 4)
}

// RGB builds a COLORREF.
func RGB(r, g, b uint8) uint32 { return uint32(r) | uint32(g)<<8 | uint32(b)<<16 }
