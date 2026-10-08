//go:build !windows

package checker

import (
	"errors"
	"syscall"
)

func classifyOS(err error) (errClass, string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, "", false
	}
	switch errno {
	case syscall.ENOENT:
		return errFileNotFound, "A fájl nem található.", true
	case syscall.ENOTDIR:
		return errPathNotFound, "A mappa nem található.", true
	case syscall.EACCES, syscall.EPERM:
		return errAccess, "Hozzáférés megtagadva (jogosultsági hiba).", true
	case syscall.EHOSTUNREACH, syscall.ENETUNREACH, syscall.ETIMEDOUT, syscall.ECONNREFUSED, syscall.EIO:
		return errServer, "A hálózat vagy a szerver nem érhető el.", true
	}
	return 0, "", false
}
