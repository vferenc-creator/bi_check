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
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bimonitor/internal/schedule"
	"bimonitor/internal/store"
	"bimonitor/internal/teamstore"

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

func (p *devPlatform) FolderDialog(string, string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.nextDialog, p.nextDialog != ""
}

func (p *devPlatform) SetAutostart(on bool) error { p.autostart = on; return nil }
func (p *devPlatform) AutostartEnabled() bool     { return p.autostart }

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address")
	data := flag.String("data", "devdata", "data directory")
	seed := flag.Bool("seed", false, "fill the history with 30 days of fake events (demo)")
	user := flag.String("user", "", "simulated user name for the shared mode (e.g. \"Kiss Anna\")")
	host := flag.String("host", "", "simulated computer name")
	flag.Parse()
	_ = os.MkdirAll(*data, 0o755)

	p := &devPlatform{subs: map[chan []byte]bool{}}
	a, err := app.New(p, app.Options{
		SettingsPath: filepath.Join(*data, "settings.json"),
		HistoryPath:  filepath.Join(*data, "history.db"),
		LocalDir:     filepath.Join(*data, "local"),
		Identity:     teamstore.Identity{User: *user, Display: *user, Host: *host},
	})
	if err != nil {
		log.Fatal(err)
	}
	if *seed {
		seedHistory(a)
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

// seedHistory invents 30 days of arrivals so the history UI can be previewed.
func seedHistory(a *app.App) {
	if a.History == nil {
		return
	}
	now := time.Now()
	from := now.AddDate(0, 0, -30)
	_, _ = a.Settings.Update(func(s *model.Settings) error {
		for i := range s.Items {
			s.Items[i].CreatedAt = from.AddDate(0, 0, -1)
		}
		return nil
	})
	up := a.History.StartUptime(from)
	up.Beat(now)
	rnd := rand.New(rand.NewSource(42))
	for _, it := range a.Settings.Get().Items {
		sc, err := schedule.Compile(it.Schedule, a.Calendar(), a.Loc)
		if err != nil {
			continue
		}
		size := int64(1_000_000 + rnd.Intn(4_000_000))
		if r := a.Checker.Check(it.Path, now, it.Token == model.TokenAny); r.File != nil && r.File.Size > 0 {
			size = r.File.Size // make the fake history resemble the real file
		}
		base := size
		for _, t := range sc.Between(from, now.Add(-time.Hour), 2000) {
			r := rnd.Float64()
			if r < 0.04 {
				_ = a.History.RecordTransition(store.Transition{ItemID: it.ID, At: t.Add(it.Grace()), From: "late", To: "missing", Reason: "Nem érkezett meg időben.", Expected: &t})
				continue
			}
			delay := time.Duration(rnd.Intn(8)) * time.Minute
			if r > 0.9 {
				delay = it.Grace() + time.Duration(5+rnd.Intn(60))*time.Minute
				_ = a.History.RecordTransition(store.Transition{ItemID: it.ID, At: t.Add(it.Grace()), From: "late", To: "missing", Reason: "Nem érkezett meg (határidő lejárt).", Expected: &t})
				_ = a.History.RecordTransition(store.Transition{ItemID: it.ID, At: t.Add(delay), From: "missing", To: "ok", Reason: "Megérkezett késéssel.", Expected: &t})
			}
			size = base + int64(rnd.Intn(int(base/50+1))) - base/100
			d := int64(delay / time.Second)
			tt := t
			_, _ = a.History.RecordArrival(store.Arrival{ItemID: it.ID, FilePath: it.Path, ModTime: t.Add(delay), Size: size, Expected: &tt, DelaySec: &d, SeenAt: t.Add(delay)})
		}
	}
	log.Printf("history seeded")
}
