//go:build windows

package winapi

import (
	"log"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Modern file/folder dialogs (IFileOpenDialog / IFileSaveDialog, Vista+).
// The legacy GetOpenFileName is kept as a fallback.

var (
	ole32                        = windows.NewLazySystemDLL("ole32.dll")
	pCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	pCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	pSHCreateItemFromParsingName = shell32.NewProc("SHCreateItemFromParsingName")

	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	clsidFileSaveDialog = windows.GUID{Data1: 0xC0B4E2F3, Data2: 0xBA21, Data3: 0x4773, Data4: [8]byte{0x8D, 0xBA, 0x33, 0x5E, 0xC9, 0x46, 0xEB, 0x8B}}
	iidIFileSaveDialog  = windows.GUID{Data1: 0x84BCCD23, Data2: 0x5FDE, Data3: 0x4CDB, Data4: [8]byte{0xAE, 0xA4, 0xAF, 0x64, 0xB8, 0x3D, 0x78, 0xAB}}
	iidIShellItem       = windows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

// IFileDialog vtable indices (IUnknown 0-2, IModalWindow::Show 3).
const (
	fdShow                = 3
	fdSetFileTypes        = 4
	fdSetFileTypeIndex    = 5
	fdSetOptions          = 9
	fdGetOptions          = 10
	fdSetFolder           = 12
	fdSetFileName         = 15
	fdSetTitle            = 17
	fdGetResult           = 20
	fdSetDefaultExtension = 22
	unkRelease            = 2
	siGetDisplayName      = 5

	fosOverwritePrompt = 0x00000002
	fosNoChangeDir     = 0x00000008
	fosPickFolders     = 0x00000020
	fosForceFileSystem = 0x00000040
	fosPathMustExist   = 0x00000800
	fosFileMustExist   = 0x00001000
	fosDontAddToRecent = 0x02000000
	sigdnFileSysPath   = 0x80058000
	clsctxInprocServer = 0x1
	hresultCancelled   = 0x800704C7
)

type filterSpec struct {
	name *uint16
	spec *uint16
}

// vcall invokes method idx of a COM object.
func vcall(obj uintptr, idx int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

type dialogOpts struct {
	save        bool
	folder      bool
	title       string
	initialDir  string
	defaultName string
	defExt      string
	filters     []FileFilter
}

// modernDialog shows IFileOpenDialog/IFileSaveDialog. ok=false + used=false
// means the COM dialog is unavailable and the caller should fall back.
func modernDialog(owner uintptr, o dialogOpts) (path string, ok bool, used bool) {
	clsid, iid := clsidFileOpenDialog, iidIFileOpenDialog
	if o.save {
		clsid, iid = clsidFileSaveDialog, iidIFileSaveDialog
	}
	var dlg uintptr
	hr, _, _ := pCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, clsctxInprocServer, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == 0 {
		log.Printf("IFileDialog unavailable: hr=0x%08x", uint32(hr))
		return "", false, false
	}
	defer vcall(dlg, unkRelease)

	var opts uint32
	vcall(dlg, fdGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosForceFileSystem | fosNoChangeDir | fosDontAddToRecent | fosPathMustExist
	switch {
	case o.folder:
		opts |= fosPickFolders
	case o.save:
		opts |= fosOverwritePrompt
	default:
		opts |= fosFileMustExist
	}
	vcall(dlg, fdSetOptions, uintptr(opts))
	if o.title != "" {
		vcall(dlg, fdSetTitle, uintptr(unsafe.Pointer(utf16(o.title))))
	}
	var specs []filterSpec
	if !o.folder && len(o.filters) > 0 {
		for _, f := range o.filters {
			specs = append(specs, filterSpec{utf16(f.Name), utf16(f.Pattern)})
		}
		vcall(dlg, fdSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
		vcall(dlg, fdSetFileTypeIndex, 1)
	}
	if o.defaultName != "" {
		vcall(dlg, fdSetFileName, uintptr(unsafe.Pointer(utf16(o.defaultName))))
	}
	if o.defExt != "" {
		vcall(dlg, fdSetDefaultExtension, uintptr(unsafe.Pointer(utf16(o.defExt))))
	}
	if o.initialDir != "" {
		var item uintptr
		hr, _, _ := pSHCreateItemFromParsingName.Call(uintptr(unsafe.Pointer(utf16(o.initialDir))), 0, uintptr(unsafe.Pointer(&iidIShellItem)), uintptr(unsafe.Pointer(&item)))
		if !failed(hr) && item != 0 {
			vcall(dlg, fdSetFolder, item)
			vcall(item, unkRelease)
		}
	}

	hr = vcall(dlg, fdShow, owner)
	if uint32(hr) == hresultCancelled {
		return "", false, true
	}
	if failed(hr) {
		log.Printf("IFileDialog.Show failed: hr=0x%08x", uint32(hr))
		return "", false, true
	}
	var res uintptr
	if hr := vcall(dlg, fdGetResult, uintptr(unsafe.Pointer(&res))); failed(hr) || res == 0 {
		log.Printf("IFileDialog.GetResult failed: hr=0x%08x", uint32(hr))
		return "", false, true
	}
	defer vcall(res, unkRelease)
	var name *uint16
	if hr := vcall(res, siGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&name))); failed(hr) || name == nil {
		log.Printf("IShellItem.GetDisplayName failed: hr=0x%08x", uint32(hr))
		return "", false, true
	}
	defer pCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	_ = specs // keep the filter strings alive until here
	return windows.UTF16PtrToString(name), true, true
}

// FolderDialog lets the user pick a folder.
func FolderDialog(owner uintptr, title, initialDir string) (string, bool) {
	p, ok, _ := modernDialog(owner, dialogOpts{folder: true, title: title, initialDir: initialDir})
	return p, ok
}
