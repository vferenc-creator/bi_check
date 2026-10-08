// devserver runs the real application core with the web UI in a normal
// browser – handy for UI development and screenshots on any OS.
//
//	go run ./cmd/devserver -addr :8787 -data ./devdata
//
// File dialogs are replaced by a prompt, notifications are logged.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"bimonitor/internal/app"
	"bimonitor/internal/model"
	"bimonitor/web"
)

type devPlatform struct {
	app.NullPlatform
	mu        sync.Mutex
	subs      map[chan []byte]bool
	autostart bool
	// NextDialogPath is returned by the next file dialog (set via /dev/dialog).
	nextDialog string
}

func (p *devPlatform) Notify(title, text string, kind app.NotifyKind) {
	log.Printf("[toast:%d] %s – %s", kind, title, text)
	p.Push("devToast", map[string]any{"title": title, "text": text})
}

func (p *devPlatform) UpdateTray(sev model.Severity, tooltip string) {
	log.Printf("[tray] sev=%d %s", sev, tooltip)
}

func (p *devPlatform) Push(event string, data any) {
	b, err := json.Marshal(map[string]any{"event": event, "data": data})
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.subs {
		select {
		case c <- b:
		default:
		}
	}
}

func (p *devPlatform) OpenFileDialog(string, string, []app.FileFilter) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nextDialog, p.nextDialog != ""
}

func (p *devPlatform) SaveFileDialog(_ string, name, _ string, _ []app.FileFilter) (string, bool) {
	return filepath.Join(os.TempDir(), name), true
}

func (p *devPlatform) SetAutostart(on bool) error { p.autostart = on; return nil }
func (p *devPlatform) AutostartEnabled() bool     { return p.autostart }

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	data := flag.String("data", "devdata", "data directory")
	flag.Parse()
	_ = os.MkdirAll(*data, 0o755)

	p := &devPlatform{subs: map[chan []byte]bool{}}
	a, err := app.New(p, app.Options{
		SettingsPath: filepath.Join(*data, "settings.json"),
		HistoryPath:  filepath.Join(*data, "history.db"),
	})
	if err != nil {
		log.Fatal(err)
	}
	a.Start()
	defer a.Close()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, web.Page())
	})
	http.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		res, err := a.Call(req.Method, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": res})
	})
	http.HandleFunc("/dev/dialog", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.nextDialog = r.URL.Query().Get("path")
		p.mu.Unlock()
	})
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		c := make(chan []byte, 64)
		p.mu.Lock()
		p.subs[c] = true
		p.mu.Unlock()
		defer func() { p.mu.Lock(); delete(p.subs, c); p.mu.Unlock() }()
		fl.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case b := <-c:
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			}
		}
	})
	log.Printf("UI: http://%s/", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
