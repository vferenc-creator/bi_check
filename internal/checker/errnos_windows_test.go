//go:build windows

package checker

import "syscall"

var (
	eNoFile     error = syscall.Errno(2)
	eNoPath     error = syscall.Errno(3)
	eServerDown error = syscall.Errno(53)
	eDenied     error = syscall.Errno(5)
)
