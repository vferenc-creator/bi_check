//go:build windows

package winapi

import "unsafe"

func ptr[T any](p *T) unsafe.Pointer { return unsafe.Pointer(p) }

func unsafeSizeof[T any](v T) uintptr { return unsafe.Sizeof(v) }
