//go:build windows

// BI Output Monitor – Windows tray application.
//
// Command line:
//
//	BIMonitor.exe               start (or bring the running instance to front)
//	BIMonitor.exe --minimized   start in the tray only (used by autostart)
//	BIMonitor.exe --debug       enable WebView2 dev tools
//	BIMonitor.exe --data-dir X  use X instead of %APPDATA% / %LOCALAPPDATA%
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"

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
	sh.OnExit = a.Close
	sh.Init()
	a.Start()
	sh.Run(!*minimized)
	log.Printf("kilépés")
}
