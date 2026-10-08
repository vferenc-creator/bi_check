//go:build windows

// BI Output Monitor – Windows tray application.
//
// Command line:
//
//	BIMonitor.exe               start (or bring the running instance to front)
//	BIMonitor.exe --minimized   start in the tray only (used by autostart)
//	BIMonitor.exe --debug       enable WebView2 dev tools
//	BIMonitor.exe --data-dir X  use X instead of %APPDATA% / %LOCALAPPDATA%
//	BIMonitor.exe --selftest F  open the UI, write "ok" to F once it talks to Go, exit
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"bimonitor/branding"
	"bimonitor/internal/app"
	"bimonitor/internal/desktop"
	"bimonitor/internal/logging"
	"bimonitor/internal/paths"
	"bimonitor/internal/winapi"
)

func init() {
	// Win32 windows and COM (WebView2) must stay on one OS thread.
	runtime.LockOSThread()
}

func main() {
	minimized := flag.Bool("minimized", false, "indítás csak a tálcán")
	debug := flag.Bool("debug", false, "fejlesztői eszközök engedélyezése")
	dataDir := flag.String("data-dir", "", "adatkönyvtár felülbírálása")
	selftest := flag.String("selftest", "", "automata teszt: megnyitja a felületet, az eredményt a megadott fájlba írja és kilép")
	selftestDialog := flag.Bool("selftest-dialog", false, "a --selftest a fájlválasztót is megnyitja és bezárja")
	flag.Parse()
	if *dataDir != "" {
		paths.Override = *dataDir
	}

	ok, mutex := winapi.AcquireSingleInstance("EnergofishBIMonitor")
	if !ok {
		// Already running: ask that instance to show its window.
		if h := winapi.FindWindow(desktop.HostClass); h != 0 {
			winapi.AllowAnyForeground()
			winapi.PostMessage(h, desktop.MsgShow, 0, 0)
		}
		return
	}
	defer windows.CloseHandle(mutex)

	logging.Setup(paths.LogDir(), nil)
	log.Printf("%s %s indul", branding.Current.AppName, app.Version)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC: %v", r)
			winapi.MessageBox(0, branding.Current.AppName, fmt.Sprintf("Váratlan hiba történt:\n%v\n\nRészletek a naplóban: %s", r, paths.LogDir()), winapi.MB_ICONERROR)
			os.Exit(1)
		}
	}()

	// S_FALSE (1) just means COM was already initialized on this thread.
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil && err != syscall.Errno(1) {
		log.Printf("CoInitializeEx: %v", err)
	}

	sh := desktop.New()
	sh.Debug = *debug
	a, err := app.New(sh, app.Options{SettingsPath: paths.SettingsFile(), HistoryPath: paths.HistoryFile()})
	if err != nil {
		winapi.MessageBox(0, branding.Current.AppName, "Az alkalmazás nem indítható:\n"+err.Error(), winapi.MB_ICONERROR)
		os.Exit(1)
	}
	sh.SetApp(a)
	if *selftest != "" {
		// The UI calls listItems right after it loaded: that proves the
		// window, WebView2 and the JS↔Go bridge all work.
		var once sync.Once
		a.OnCall = func(method string) {
			if method == "listItems" {
				once.Do(func() {
					if !*selftestDialog {
						_ = os.WriteFile(*selftest, []byte("ok"), 0o644)
						sh.Do(sh.Shutdown)
						return
					}
					go selftestFileDialog(a, sh, *selftest)
				})
			}
		}
		go func() {
			time.Sleep(90 * time.Second)
			_ = os.WriteFile(*selftest, []byte("timeout"), 0o644)
			os.Exit(2)
		}()
	}
	sh.OnExit = a.Close
	sh.Init()
	a.Start()
	sh.Run(!*minimized)
	log.Printf("kilépés")
}

// selftestFileDialog opens the "Tallózás" dialog through the same RPC the UI
// uses, checks that a dialog window really appears, then cancels it.
func selftestFileDialog(a *app.App, sh *desktop.Shell, out string) {
	done := make(chan error, 1)
	go func() {
		_, err := a.Call("browseFile", []byte(`""`))
		done <- err
	}()
	result := "dialog-missing"
	for i := 0; i < 100; i++ {
		time.Sleep(200 * time.Millisecond)
		if dlg := winapi.FindOwnDialog(); dlg != 0 {
			result = "ok"
			winapi.PostMessage(dlg, winapi.WM_COMMAND, 2 /* IDCANCEL */, 0)
			break
		}
	}
	select {
	case err := <-done:
		if err != nil && result == "ok" {
			result = "dialog-error: " + err.Error()
		}
	case <-time.After(10 * time.Second):
		if result == "ok" {
			result = "dialog-did-not-close"
		}
	}
	log.Printf("selftest dialog: %s", result)
	_ = os.WriteFile(out, []byte(result), 0o644)
	sh.Do(sh.Shutdown)
}
