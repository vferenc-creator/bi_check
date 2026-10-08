//go:build windows

package winapi

import (
	"log"
	u16 "unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

type openFileName struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

const (
	ofnOverwritePrompt = 0x00000002
	ofnHideReadOnly    = 0x00000004
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000
	ofnDontAddToRecent = 0x02000000
)

// FileFilter is a "name" + "*.ext;*.ext2" pair.
type FileFilter struct{ Name, Pattern string }

// filterString builds the double-NUL terminated "name\0pattern\0...\0\0"
// list GetOpenFileName expects. (windows.StringToUTF16 must not be used here:
// it panics on embedded NULs.)
func filterString(filters []FileFilter) *uint16 {
	if len(filters) == 0 {
		filters = []FileFilter{{"Minden fájl", "*.*"}}
	}
	var u []uint16
	for _, f := range filters {
		u = append(u, u16.Encode([]rune(f.Name))...)
		u = append(u, 0)
		u = append(u, u16.Encode([]rune(f.Pattern))...)
		u = append(u, 0)
	}
	u = append(u, 0)
	return &u[0]
}

func fileDialog(save bool, owner uintptr, title, initialDir, defaultName, defExt string, filters []FileFilter) (string, bool) {
	buf := make([]uint16, 4096)
	if defaultName != "" {
		copy(buf, windows.StringToUTF16(defaultName))
	}
	ofn := openFileName{
		HwndOwner:   owner,
		LpstrFilter: filterString(filters),
		LpstrFile:   &buf[0],
		NMaxFile:    uint32(len(buf)),
		LpstrTitle:  utf16(title),
		Flags:       ofnExplorer | ofnNoChangeDir | ofnHideReadOnly | ofnDontAddToRecent,
	}
	ofn.LStructSize = uint32(unsafe.Sizeof(ofn))
	if initialDir != "" {
		ofn.LpstrInitialDir = utf16(initialDir)
	}
	if defExt != "" {
		ofn.LpstrDefExt = utf16(defExt)
	}
	var r uintptr
	if save {
		ofn.Flags |= ofnOverwritePrompt | ofnPathMustExist
		r, _, _ = pGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	} else {
		ofn.Flags |= ofnFileMustExist | ofnPathMustExist
		r, _, _ = pGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	}
	if r == 0 {
		if code, _, _ := pCommDlgExtendedError.Call(); code != 0 {
			log.Printf("file dialog error: CommDlgExtendedError=0x%04x", code)
		}
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

// OpenFileDialog shows the native "Open" dialog (modern IFileOpenDialog,
// legacy GetOpenFileName as fallback).
func OpenFileDialog(owner uintptr, title, initialDir string, filters []FileFilter) (string, bool) {
	if p, ok, used := modernDialog(owner, dialogOpts{title: title, initialDir: initialDir, filters: filters}); used {
		return p, ok
	}
	return fileDialog(false, owner, title, initialDir, "", "", filters)
}

// SaveFileDialog shows the native "Save as" dialog.
func SaveFileDialog(owner uintptr, title, defaultName, defExt string, filters []FileFilter) (string, bool) {
	if p, ok, used := modernDialog(owner, dialogOpts{save: true, title: title, defaultName: defaultName, defExt: defExt, filters: filters}); used {
		return p, ok
	}
	return fileDialog(true, owner, title, "", defaultName, defExt, filters)
}
