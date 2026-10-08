//go:build windows

package winapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---- Single instance ----------------------------------------------------

// AcquireSingleInstance creates a named mutex in the user's session.
// It returns false if another instance already holds it.
func AcquireSingleInstance(name string) (bool, windows.Handle) {
	h, err := windows.CreateMutex(nil, false, utf16(`Local\`+name))
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if h != 0 {
				windows.CloseHandle(h)
			}
			return false, 0
		}
		// Could not create the mutex at all; run anyway.
		return true, 0
	}
	return true, h
}

// ---- Autostart (HKCU\...\Run) ---------------------------------------------

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// AutostartCommand is the command line written to the Run key.
func AutostartCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.Abs(exe)
	return `"` + exe + `" --minimized`, nil
}

// SetAutostart enables or disables starting with Windows for the current user.
func SetAutostart(valueName string, enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enabled {
		err := k.DeleteValue(valueName)
		if errors.Is(err, registry.ErrNotExist) || errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return nil
		}
		return err
	}
	cmd, err := AutostartCommand()
	if err != nil {
		return err
	}
	return k.SetStringValue(valueName, cmd)
}

// GetAutostart returns the registered command line ("" if not registered).
func GetAutostart(valueName string) string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(valueName)
	if err != nil {
		return ""
	}
	return v
}

// ---- Mapped drives → UNC --------------------------------------------------

// DriveToUNC converts a path on a mapped network drive (M:\x\y) to its UNC
// form (\\server\share\x\y). Local paths and UNC paths are returned as-is.
func DriveToUNC(path string) (string, bool) {
	p := filepath.Clean(path)
	if len(p) < 2 || p[1] != ':' {
		return path, false
	}
	drive := strings.ToUpper(p[:2])
	buf := make([]uint16, 1024)
	n := uint32(len(buf))
	r, _, _ := pWNetGetConnectionW.Call(uintptr(ptr(utf16(drive))), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r != 0 {
		return path, false
	}
	remote := strings.TrimRight(windows.UTF16ToString(buf), `\`)
	if remote == "" {
		return path, false
	}
	rest := strings.TrimLeft(p[2:], `\`)
	if rest == "" {
		return remote, true
	}
	return remote + `\` + rest, true
}

// ---- Shell ------------------------------------------------------------

// ShellOpen opens a file or folder with its default handler.
func ShellOpen(target string) error {
	r, _, _ := pShellExecuteW.Call(0, uintptr(ptr(utf16("open"))), uintptr(ptr(utf16(target))), 0, 0, SW_SHOWNORMAL)
	if r <= 32 {
		return errors.New("a megnyitás nem sikerült")
	}
	return nil
}

// ShowInExplorer opens Explorer with the file selected.
func ShowInExplorer(file string) error {
	args := `/select,"` + file + `"`
	r, _, _ := pShellExecuteW.Call(0, uintptr(ptr(utf16("open"))), uintptr(ptr(utf16("explorer.exe"))), uintptr(ptr(utf16(args))), 0, SW_SHOWNORMAL)
	if r <= 32 {
		return errors.New("az Intéző megnyitása nem sikerült")
	}
	return nil
}

// ---- Data protection (DPAPI) ------------------------------------------------

// Protect encrypts data for the current Windows user (DPAPI).
func Protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(plain)), Data: &plain[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// Unprotect decrypts data produced by Protect.
func Unprotect(enc []byte) ([]byte, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	in := windows.DataBlob{Size: uint32(len(enc)), Data: &enc[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

// ---- Current user ---------------------------------------------------------------

var pGetUserNameExW = windows.NewLazySystemDLL("secur32.dll").NewProc("GetUserNameExW")

// UserDisplayName returns the user's full name from the domain (e.g.
// "Kiss Anna"), or "" if not available.
func UserDisplayName() string {
	const nameDisplay = 3
	if pGetUserNameExW.Find() != nil {
		return ""
	}
	n := uint32(256)
	buf := make([]uint16, n)
	r, _, _ := pGetUserNameExW.Call(nameDisplay, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}
	return windows.UTF16ToString(buf)
}
