//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

// comCall invokes the method at vtable index idx of a COM object (no args).
func comCall(obj unsafe.Pointer, idx int) uintptr {
	if obj == nil {
		return 0
	}
	vtbl := *(**[64]uintptr)(obj)
	fn := vtbl[idx]
	r, _, _ := syscall.SyscallN(fn, uintptr(obj))
	return r
}
