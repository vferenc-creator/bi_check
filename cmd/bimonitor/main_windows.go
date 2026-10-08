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

	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
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
					_ = os.WriteFile(*selftest, []byte("ok"), 0o644)
					sh.Do(sh.Shutdown)
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
