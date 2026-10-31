//go:build windows

package app

import (
	"path/filepath"
	"syscall"
	"testing"
)

// holdProfileLock simulates a running Thunderbird: it keeps parent.lock open
// without sharing, as Thunderbird does on Windows.
func holdProfileLock(t *testing.T, dir string) {
	p, _ := syscall.UTF16PtrFromString(filepath.Join(dir, "parent.lock"))
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.CloseHandle(h) })
}
