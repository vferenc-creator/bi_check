//go:build windows

package winapi

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10
	nifShowTip = 0x80

	niifInfo             = 0x01
	niifWarning          = 0x02
	niifError            = 0x03
	niifUser             = 0x04
	niifLargeIcon        = 0x20
	niifRespectQuietTime = 0x80

	notifyIconVersion4 = 4

	ninSelect           = WM_USER + 0
	ninKeySelect        = WM_USER + 1
	ninBalloonUserClick = WM_USER + 5
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

// BalloonKind selects the system icon of a notification.
type BalloonKind int

const (
	BalloonInfo BalloonKind = iota
	BalloonWarning
	BalloonError
	BalloonApp // uses the application icon (large)
)

// Tray is a notification-area icon bound to a host window.
type Tray struct {
	mu       sync.Mutex
	hwnd     uintptr
	callback uint32
	icon     uintptr
	tip      string
	appIcon  uintptr // large icon for balloons
	added    bool
}

// NewTray prepares a tray icon; hwnd receives callback messages.
func NewTray(hwnd uintptr, callbackMsg uint32) *Tray {
	return &Tray{hwnd: hwnd, callback: callbackMsg}
}

func (t *Tray) base() notifyIconData {
	var d notifyIconData
	d.CbSize = uint32(unsafe.Sizeof(d))
	d.HWnd = t.hwnd
	d.UID = 1
	return d
}

func copyUTF16(dst []uint16, s string) {
	u, _ := windows.UTF16FromString(s)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

// Show adds (or re-adds after Explorer restart) the icon.
func (t *Tray) Show() {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.base()
	d.UFlags = nifMessage | nifIcon | nifTip | nifShowTip
	d.UCallbackMessage = t.callback
	d.HIcon = t.icon
	copyUTF16(d.SzTip[:], t.tip)
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
	r, _, _ := pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&d)))
	t.added = r != 0
	d.UVersion = notifyIconVersion4
	pShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&d)))
}

// SetIcon changes the icon; the previous HICON is destroyed.
func (t *Tray) SetIcon(icon uintptr, tip string) {
	t.mu.Lock()
	old := t.icon
	t.icon = icon
	t.tip = tip
	d := t.base()
	d.UFlags = nifIcon | nifTip | nifShowTip
	d.HIcon = icon
	copyUTF16(d.SzTip[:], tip)
	added := t.added
	t.mu.Unlock()
	if added {
		pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d)))
	}
	if old != 0 && old != icon {
		DestroyIcon(old)
	}
}

// SetBalloonIcon sets the large icon used by BalloonApp notifications.
func (t *Tray) SetBalloonIcon(icon uintptr) {
	t.mu.Lock()
	t.appIcon = icon
	t.mu.Unlock()
}

// Notify shows a notification. On Windows 10/11 these appear as native toast
// notifications and are kept in the Action Center.
func (t *Tray) Notify(title, text string, kind BalloonKind) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.base()
	d.UFlags = nifInfo
	copyUTF16(d.SzInfoTitle[:], title)
	copyUTF16(d.SzInfo[:], text)
	switch kind {
	case BalloonWarning:
		d.DwInfoFlags = niifWarning
	case BalloonError:
		d.DwInfoFlags = niifError
	case BalloonApp:
		if t.appIcon != 0 {
			d.DwInfoFlags = niifUser | niifLargeIcon
			d.HBalloonIcon = t.appIcon
		} else {
			d.DwInfoFlags = niifInfo
		}
	default:
		d.DwInfoFlags = niifInfo
	}
	d.DwInfoFlags |= niifRespectQuietTime
	pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&d)))
}

// Remove deletes the icon from the notification area.
func (t *Tray) Remove() {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := t.base()
	pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
	t.added = false
}

// TrayEvent is a decoded tray callback.
type TrayEvent int

const (
	TrayNone         TrayEvent = iota
	TrayActivate               // left click / Enter
	TrayContextMenu            // right click / menu key
	TrayBalloonClick           // the user clicked a notification
)

// DecodeTrayCallback decodes the lParam of a NOTIFYICON_VERSION_4 callback.
func DecodeTrayCallback(lp uintptr) TrayEvent {
	switch uint32(loWord(lp)) {
	case ninSelect, ninKeySelect, WM_LBUTTONDBLCLK:
		return TrayActivate
	case WM_CONTEXTMENU:
		return TrayContextMenu
	case ninBalloonUserClick:
		return TrayBalloonClick
	}
	return TrayNone
}

// MenuItem describes a popup menu entry; ID 0 with empty Text is a separator.
type MenuItem struct {
	ID       uint32
	Text     string
	Checked  bool
	Disabled bool
	Default  bool
}

// PopupMenu shows a context menu at the cursor and returns the chosen ID (0 = none).
func PopupMenu(hwnd uintptr, items []MenuItem) uint32 {
	m, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(m)
	for _, it := range items {
		if it.Text == "" {
			pAppendMenuW.Call(m, MF_SEPARATOR, 0, 0)
			continue
		}
		flags := uintptr(MF_STRING)
		if it.Checked {
			flags |= MF_CHECKED
		}
		if it.Disabled {
			flags |= MF_GRAYED
		}
		if it.Default {
			flags |= MF_DEFAULT
		}
		pAppendMenuW.Call(m, flags, uintptr(it.ID), uintptr(ptr(utf16(it.Text))))
	}
	var pt POINT
	pGetCursorPos.Call(uintptr(ptr(&pt)))
	// Required so the menu closes when the user clicks elsewhere.
	pSetForegroundWindow.Call(hwnd)
	r, _, _ := pTrackPopupMenu.Call(m, TPM_RIGHTBUTTON|TPM_RETURNCMD|TPM_BOTTOMALIGN, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	pPostMessageW.Call(hwnd, WM_NULL, 0, 0)
	return uint32(r)
}
