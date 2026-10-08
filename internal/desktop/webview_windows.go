//go:build windows

package desktop

import (
	"encoding/json"
	"log"
	"strconv"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"

	"bimonitor/branding"
	"bimonitor/internal/paths"
	"bimonitor/internal/winapi"
	"bimonitor/web"
)

const mainClass = "EnergofishBIMonitorMain"

// mainWindow is the UI window hosting WebView2. It is created on demand and
// fully destroyed when closed, so the browser processes do not use memory
// while the app sits in the tray.
type mainWindow struct {
	sh       *Shell
	hwnd     uintptr
	chromium *edge.Chromium
	ready    bool
}

type rpcRequest struct {
	ID     int             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// WebView2Available reports whether the WebView2 runtime is installed.
func WebView2Available() (string, bool) {
	v, err := webviewloader.GetInstalledVersion()
	return v, err == nil && v != ""
}

func (sh *Shell) openWindow() {
	if sh.win != nil && sh.win.hwnd != 0 {
		winapi.BringToFront(sh.win.hwnd)
		return
	}
	if _, ok := WebView2Available(); !ok {
		if winapi.MessageBox(0, branding.Current.AppName,
			"A felülethez a Microsoft Edge WebView2 Runtime szükséges, de nem található ezen a gépen.\n\n"+
				"A figyelés a háttérben ettől függetlenül működik, és az értesítések is megjelennek.\n\n"+
				"Megnyissam a letöltési oldalt?", winapi.MB_YESNO|winapi.MB_ICONWARNING) == winapi.IDYES {
			_ = winapi.ShellOpen("https://developer.microsoft.com/microsoft-edge/webview2/")
		}
		return
	}

	w := &mainWindow{sh: sh}
	dpi := winapi.DPI(0)
	width, height := int32(1280*dpi/96), int32(800*dpi/96)
	wa := winapi.WorkArea()
	if ww := wa.Right - wa.Left; width > ww-40 {
		width = ww - 40
	}
	if wh := wa.Bottom - wa.Top; height > wh-40 {
		height = wh - 40
	}
	x := wa.Left + (wa.Right-wa.Left-width)/2
	y := wa.Top + (wa.Bottom-wa.Top-height)/2

	winapi.RegisterClass(mainClass, sh.bigIcon, sh.smallIcon)
	w.hwnd = winapi.CreateWindow(mainClass, branding.Current.AppName, 0, winapi.WS_OVERLAPPEDWINDOW,
		x, y, width, height, 0, w.wndProc)
	if w.hwnd == 0 {
		log.Printf("CreateWindow failed")
		return
	}
	sh.win = w
	if c, ok := branding.ParseHex(branding.Current.Colors["primary"]); ok {
		winapi.SetCaptionColor(w.hwnd, winapi.RGB(c.R, c.G, c.B), winapi.RGB(255, 255, 255))
	}
	winapi.ShowWindow(w.hwnd, winapi.SW_SHOWNORMAL)
	winapi.SetForeground(w.hwnd)

	ch := edge.NewChromium()
	ch.DataPath = paths.WebViewDir()
	ch.MessageCallback = w.onMessage
	w.chromium = ch
	if !ch.Embed(w.hwnd) {
		log.Printf("WebView2 embed failed")
		winapi.MessageBox(w.hwnd, branding.Current.AppName, "A felület (WebView2) nem indítható el. A figyelés a háttérben tovább fut.", winapi.MB_ICONERROR)
		w.destroy()
		return
	}
	if settings, err := ch.GetSettings(); err == nil {
		debug := sh.Debug
		_ = settings.PutAreDevToolsEnabled(debug)
		_ = settings.PutAreDefaultContextMenusEnabled(debug)
		_ = settings.PutIsStatusBarEnabled(false)
		_ = settings.PutIsZoomControlEnabled(true)
		_ = settings.PutAreBrowserAcceleratorKeysEnabled(debug)
	}
	if c2 := ch.GetController().GetICoreWebView2Controller2(); c2 != nil {
		if c, ok := branding.ParseHex(branding.Current.Colors["background"]); ok {
			_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: c.R, G: c.G, B: c.B})
		}
	}
	ch.Resize()
	ch.NavigateToString(web.Page())
	w.ready = true
	ch.Focus()
}

func (w *mainWindow) wndProc(hwnd uintptr, msg uint32, wp, lp uintptr) (uintptr, bool) {
	switch msg {
	case winapi.WM_SIZE:
		if w.chromium != nil && w.ready {
			w.chromium.Resize()
		}
		return 0, true
	case winapi.WM_MOVE, winapi.WM_MOVING:
		if w.chromium != nil && w.ready {
			_ = w.chromium.NotifyParentWindowPositionChanged()
		}
	case winapi.WM_ACTIVATE:
		if w.chromium != nil && w.ready && loWord(wp) != 0 {
			w.chromium.Focus()
		}
	case winapi.WM_GETMINMAXINFO:
		mmi := (*winapi.MINMAXINFO)(unsafe.Pointer(lp))
		dpi := int32(winapi.DPI(hwnd))
		mmi.PtMinTrackSize = winapi.POINT{X: 900 * dpi / 96, Y: 560 * dpi / 96}
		return 0, true
	case winapi.WM_DPICHANGED:
		r := (*winapi.RECT)(unsafe.Pointer(lp))
		winapi.SetWindowPos(hwnd, r.Left, r.Top, r.Right-r.Left, r.Bottom-r.Top, winapi.SWP_NOZORDER|winapi.SWP_NOACTIVATE)
		return 0, true
	case winapi.WM_CLOSE:
		// Closing the window only hides the UI; monitoring keeps running.
		w.sh.onWindowClosed()
		w.destroy()
		return 0, true
	case winapi.WM_DESTROY:
		if w.sh.win == w {
			w.sh.win = nil
		}
		return 0, true
	}
	return 0, false
}

func loWord(v uintptr) uint16 { return uint16(v & 0xffff) }

// destroy closes the WebView2 controller (freeing the browser processes)
// and the window.
func (w *mainWindow) destroy() {
	w.ready = false
	if w.chromium != nil {
		if ctrl := w.chromium.GetController(); ctrl != nil {
			comCall(unsafe.Pointer(ctrl), 24) // ICoreWebView2Controller::Close
			comCall(unsafe.Pointer(ctrl), 2)  // Release
		}
		if env := w.chromium.Environment(); env != nil {
			comCall(unsafe.Pointer(env), 2) // Release
		}
		w.chromium = nil
	}
	if w.hwnd != 0 {
		h := w.hwnd
		w.hwnd = 0
		winapi.DestroyWindow(h)
	}
	if w.sh.win == w {
		w.sh.win = nil
	}
}

// onMessage receives RPC requests from JavaScript (UI thread). The work runs
// on a goroutine so slow network checks never block the window.
func (w *mainWindow) onMessage(msg string) {
	var req rpcRequest
	if err := json.Unmarshal([]byte(msg), &req); err != nil || req.Method == "" {
		return
	}
	go func() {
		res, err := w.sh.app.Call(req.Method, req.Params)
		var js string
		id := strconv.Itoa(req.ID)
		if err != nil {
			b, _ := json.Marshal(err.Error())
			js = "window.__rpc(" + id + ",false," + string(b) + ")"
		} else {
			b, jerr := json.Marshal(res)
			if jerr != nil {
				eb, _ := json.Marshal(jerr.Error())
				js = "window.__rpc(" + id + ",false," + string(eb) + ")"
			} else {
				js = "window.__rpc(" + id + ",true," + string(b) + ")"
			}
		}
		w.sh.Do(func() { w.eval(js) })
	}()
}

func (w *mainWindow) eval(js string) {
	if w.ready && w.chromium != nil && w.sh.win == w {
		w.chromium.Eval(js)
	}
}
