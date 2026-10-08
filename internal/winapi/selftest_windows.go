//go:build windows

package winapi

import (
	"os"

	"golang.org/x/sys/windows"
)

var findDlgCB = windows.NewCallback(func(hwnd, lparam uintptr) uintptr {
	var pid uint32
	pGetWindowThreadProcessId.Call(hwnd, uintptr(ptr(&pid)))
	if pid != uint32(os.Getpid()) || !IsWindowVisible(hwnd) {
		return 1
	}
	buf := make([]uint16, 64)
	pGetClassNameW.Call(hwnd, uintptr(ptr(&buf[0])), 64)
	if windows.UTF16ToString(buf) == "#32770" { // standard dialog class
		*(*uintptr)(unsafePointer(lparam)) = hwnd
		return 0
	}
	return 1
})

// FindOwnDialog returns a visible standard dialog window of this process (0 if none).
func FindOwnDialog() uintptr {
	var found uintptr
	pEnumWindows.Call(findDlgCB, uintptr(ptr(&found)))
	return found
}
