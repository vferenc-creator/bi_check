//go:build !windows

package checker

import "syscall"

var (
	eNoFile     error = syscall.ENOENT
	eNoPath     error = syscall.ENOENT
	eServerDown error = syscall.EHOSTUNREACH
	eDenied     error = syscall.EACCES
)
