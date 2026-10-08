//go:build windows

// Package desktop is the Windows shell around the app: tray icon, main
// window (WebView2), notifications and the UI-thread dispatcher.
package desktop

import (
	"encoding/json"
	"image"
	"log"
	"sync"
	"time"

	"bimonitor/branding"
	"bimonitor/internal/app"
	"bimonitor/internal/model"
	"bimonitor/internal/winapi"
)

// HostClass is the class of the hidden control window; a second instance
// finds it to ask the running one to show its window.
const HostClass = "EnergofishBIMonitorHost"

const (
	msgDispatch = winapi.WM_APP + 1
	msgTray     = winapi.WM_APP + 2
	// MsgShow asks the running instance to open its window.
	MsgShow = winapi.WM_APP + 3
)

// Tray menu command IDs.
const (
	cmdOpen = iota + 1
	cmdCheckNow
	cmdPause1h
	cmdPauseTomorrow
	cmdResume
	cmdAutostart
	cmdExit
)

// Shell owns the UI thread.
type Shell struct {
	app   *app.App
	Debug bool

	host           uintptr
	tray           *winapi.Tray
	win            *mainWindow
	taskbarCreated uint32
	base           image.Image
	smallIcon      uintptr
	bigIcon        uintptr
	balloonIcon    uintptr
	sev            model.Severity
	tooltip        string
	trayReady      bool

	qmu   sync.Mutex
	queue []func()

	// OnExit is called on the UI thread before the message loop ends.
	OnExit func()
}

// New creates the shell; call SetApp before Run.
func New() *Shell {
	return &Shell{base: branding.Icon(), sev: model.SevNone}
}

// SetApp attaches the application core.
func (sh *Shell) SetApp(a *app.App) { sh.app = a }

// Init creates the hidden host window and the tray icon. Must run on the
// locked UI thread.
func (sh *Shell) Init() {
	dpi := winapi.DPI(0)
	sh.smallIcon = winapi.LoadResourceIcon(winapi.SmallIconSize(dpi))
	sh.bigIcon = winapi.LoadResourceIcon(32 * dpi / 96)
	if sh.smallIcon == 0 {
		sh.smallIcon = winapi.IconFromImage(sh.base, winapi.SmallIconSize(dpi))
	}
	if sh.bigIcon == 0 {
		sh.bigIcon = winapi.IconFromImage(sh.base, 32*dpi/96)
	}
	sh.balloonIcon = winapi.IconFromImage(sh.base, 64)

	winapi.RegisterClass(HostClass, sh.bigIcon, sh.smallIcon)
	sh.host = winapi.CreateWindow(HostClass, branding.Current.AppName, 0x00000080 /*WS_EX_TOOLWINDOW*/, 0, 0, 0, 0, 0, 0, sh.hostProc)
	sh.taskbarCreated = winapi.RegisterWindowMessage("TaskbarCreated")

	sh.tray = winapi.NewTray(sh.host, msgTray)
	sh.tray.SetBalloonIcon(sh.balloonIcon)
	sh.tray.SetIcon(trayIcon(sh.base, model.SevNone, dpi), branding.Current.AppName)
	sh.tray.Show()
	sh.trayReady = true
}

// Run pumps messages until exit.
func (sh *Shell) Run(showWindow bool) {
	if showWindow {
		sh.openWindow()
	}
	winapi.RunMessageLoop()
}

// Do runs f on the UI thread (asynchronously).
func (sh *Shell) Do(f func()) {
	sh.qmu.Lock()
	sh.queue = append(sh.queue, f)
	sh.qmu.Unlock()
	winapi.PostMessage(sh.host, msgDispatch, 0, 0)
}

// DoWait runs f on the UI thread and waits for it.
func (sh *Shell) DoWait(f func()) {
	done := make(chan struct{})
	sh.Do(func() { defer close(done); f() })
	<-done
}

func (sh *Shell) runQueue() {
	sh.qmu.Lock()
	q := sh.queue
	sh.queue = nil
	sh.qmu.Unlock()
	for _, f := range q {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("ui task panic: %v", r)
				}
			}()
			f()
		}()
	}
}

func (sh *Shell) hostProc(hwnd uintptr, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case msgDispatch:
		sh.runQueue()
		return 0, true
	case MsgShow:
		sh.openWindow()
		return 0, true
	case msgTray:
		switch winapi.DecodeTrayCallback(lp) {
		case winapi.TrayActivate, winapi.TrayBalloonClick:
			sh.openWindow()
		case winapi.TrayContextMenu:
			sh.showTrayMenu()
		}
		return 0, true
	case winapi.WM_QUERYENDSESSION:
		return 1, true
	case winapi.WM_ENDSESSION:
		if wp != 0 {
			sh.shutdown()
		}
		return 0, true
	}
	if sh.taskbarCreated != 0 && msg == sh.taskbarCreated && sh.tray != nil {
		sh.tray.Show() // Explorer restarted
		return 0, true
	}
	return 0, false
}

func (sh *Shell) showTrayMenu() {
	s := sh.app.Settings.Get()
	paused := s.Notifications.PausedUntil.After(time.Now())
	items := []winapi.MenuItem{
		{ID: cmdOpen, Text: "Megnyitás", Default: true},
		{ID: cmdCheckNow, Text: "Ellenőrzés most"},
		{},
	}
	if paused {
		items = append(items, winapi.MenuItem{ID: cmdResume, Text: "Értesítések visszakapcsolása (szüneteltetve " + s.Notifications.PausedUntil.Format("15:04") + "-ig)"})
	} else {
		items = append(items,
			winapi.MenuItem{ID: cmdPause1h, Text: "Értesítések szüneteltetése 1 órára"},
			winapi.MenuItem{ID: cmdPauseTomorrow, Text: "Értesítések szüneteltetése holnap reggelig"})
	}
	items = append(items,
		winapi.MenuItem{},
		winapi.MenuItem{ID: cmdAutostart, Text: "Indítás a Windows-zal", Checked: sh.AutostartEnabled()},
		winapi.MenuItem{},
		winapi.MenuItem{ID: cmdExit, Text: "Kilépés"},
	)
	switch winapi.PopupMenu(sh.host, items) {
	case cmdOpen:
		sh.openWindow()
	case cmdCheckNow:
		go sh.app.Call("checkNow", json.RawMessage(`""`))
	case cmdPause1h:
		go sh.app.PauseNotifications(time.Now().Add(time.Hour))
	case cmdPauseTomorrow:
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 7, 0, 0, 0, now.Location())
		go sh.app.PauseNotifications(next)
	case cmdResume:
		go sh.app.PauseNotifications(time.Time{})
	case cmdAutostart:
		on := !sh.AutostartEnabled()
		go func() {
			if _, err := sh.app.Call("setAutostart", mustJSON(on)); err != nil {
				sh.Notify(branding.Current.AppName, "Az automatikus indítás beállítása nem sikerült: "+err.Error(), app.NotifyError)
			}
		}()
	case cmdExit:
		sh.shutdown()
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// onWindowClosed shows the one-time "still running" hint.
func (sh *Shell) onWindowClosed() {
	s := sh.app.Settings.Get()
	if s.CloseHintShown {
		return
	}
	sh.tray.Notify(branding.Current.AppName,
		"Az alkalmazás a háttérben tovább figyeli az outputokat. A tálcaikonra kattintva bármikor megnyithatja, kilépni a jobb gombos menüből lehet.",
		winapi.BalloonApp)
	go sh.app.Settings.Update(func(s *model.Settings) error { s.CloseHintShown = true; return nil })
}

func (sh *Shell) shutdown() {
	if sh.win != nil {
		sh.win.destroy()
	}
	if sh.OnExit != nil {
		sh.OnExit()
	}
	if sh.tray != nil {
		sh.tray.Remove()
	}
	winapi.Quit()
}

// ---- app.Platform implementation -------------------------------------------

// Notify shows a toast via the tray icon.
func (sh *Shell) Notify(title, text string, kind app.NotifyKind) {
	sh.Do(func() {
		if sh.tray == nil {
			return
		}
		k := winapi.BalloonApp
		switch kind {
		case app.NotifyWarning:
			k = winapi.BalloonWarning
		case app.NotifyError:
			k = winapi.BalloonError
		case app.NotifyInfo:
			k = winapi.BalloonInfo
		}
		sh.tray.Notify(title, text, k)
	})
}

// UpdateTray recolors the tray icon.
func (sh *Shell) UpdateTray(sev model.Severity, tooltip string) {
	sh.Do(func() {
		if sh.tray == nil || (sev == sh.sev && tooltip == sh.tooltip) {
			return
		}
		iconChanged := sev != sh.sev
		sh.sev, sh.tooltip = sev, tooltip
		if iconChanged {
			sh.tray.SetIcon(trayIcon(sh.base, sev, winapi.DPI(0)), tooltip)
		} else {
			sh.tray.SetIcon(sh.currentTrayIcon(), tooltip)
		}
	})
}

func (sh *Shell) currentTrayIcon() uintptr {
	return trayIcon(sh.base, sh.sev, winapi.DPI(0))
}

// Push forwards an event to the UI.
func (sh *Shell) Push(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	ev, _ := json.Marshal(event)
	js := "window.__push(" + string(ev) + "," + string(b) + ")"
	sh.Do(func() {
		if sh.win != nil {
			sh.win.eval(js)
		}
	})
}

func (sh *Shell) owner() uintptr {
	if sh.win != nil {
		return sh.win.hwnd
	}
	return sh.host
}

func toWinFilters(f []app.FileFilter) []winapi.FileFilter {
	out := make([]winapi.FileFilter, len(f))
	for i, x := range f {
		out[i] = winapi.FileFilter{Name: x.Name, Pattern: x.Pattern}
	}
	return out
}

// OpenFileDialog shows the native open dialog (blocks the caller, not the UI).
func (sh *Shell) OpenFileDialog(title, initialDir string, filters []app.FileFilter) (path string, ok bool) {
	sh.DoWait(func() { path, ok = winapi.OpenFileDialog(sh.owner(), title, initialDir, toWinFilters(filters)) })
	return
}

// SaveFileDialog shows the native save dialog.
func (sh *Shell) SaveFileDialog(title, defaultName, defExt string, filters []app.FileFilter) (path string, ok bool) {
	sh.DoWait(func() {
		path, ok = winapi.SaveFileDialog(sh.owner(), title, defaultName, defExt, toWinFilters(filters))
	})
	return
}

// ShellOpen opens a file/folder with its default program.
func (sh *Shell) ShellOpen(p string) error { return winapi.ShellOpen(p) }

// ShowInFolder opens Explorer with the file selected.
func (sh *Shell) ShowInFolder(p string) error { return winapi.ShowInExplorer(p) }

// ToUNC converts mapped drive paths to UNC.
func (sh *Shell) ToUNC(p string) (string, bool) { return winapi.DriveToUNC(p) }

// AutostartValueName is the registry value under HKCU\...\Run.
const AutostartValueName = "EnergofishBIMonitor"

// SetAutostart writes/removes the Run registry value.
func (sh *Shell) SetAutostart(on bool) error { return winapi.SetAutostart(AutostartValueName, on) }

// AutostartEnabled reports whether the Run value exists.
func (sh *Shell) AutostartEnabled() bool { return winapi.GetAutostart(AutostartValueName) != "" }

// Protect encrypts with DPAPI.
func (sh *Shell) Protect(b []byte) ([]byte, error) { return winapi.Protect(b) }

// Unprotect decrypts with DPAPI.
func (sh *Shell) Unprotect(b []byte) ([]byte, error) { return winapi.Unprotect(b) }
