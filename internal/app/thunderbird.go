package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/checker"
	"bimonitor/internal/mailwatch"
	"bimonitor/internal/model"
)

// The Thunderbird add-on talks to the application over HTTP on 127.0.0.1
// only (never reachable from the network, no firewall prompt). Every call
// carries the pairing key shown on the settings page.

// tbState is the running local API.
type tbState struct {
	mu        sync.Mutex
	srv       *http.Server
	addr      string
	key       string
	err       string
	rulesHash string
	draft     *MailDraft
}

// MailDraft is a message the user picked in Thunderbird ("watch e-mails like
// this"); the editor opens prefilled with it.
type MailDraft struct {
	Subject string    `json:"subject"`
	Folder  string    `json:"folder"`
	Account string    `json:"account"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	Date    time.Time `json:"date"`
}

func newKey() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// mailCheck is the engine's checker for e-mail items.
func (a *App) mailCheck(it model.Item, ref time.Time, anyDate bool) checker.Result {
	tb := a.Settings.Get().Thunderbird
	return a.Mail.Check(it, ref, anyDate, tb.Enabled, time.Duration(tb.StaleMin)*time.Minute)
}

func (a *App) emailItemIDs() []string {
	var ids []string
	for _, it := range a.effectiveItems(a.Settings.Get()) {
		if it.IsEmail() && it.Enabled {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// syncThunderbird starts, restarts or stops the local API to match the
// settings, and asks the add-on to rescan when the e-mail rules changed.
func (a *App) syncThunderbird() {
	s := a.Settings.Get()
	tb := s.Thunderbird
	if tb.Enabled && tb.Key == "" {
		s2, err := a.Settings.Update(func(s *model.Settings) error {
			s.Thunderbird.Key, s.Thunderbird.KeyIssued = newKey(), time.Now().UTC().Format(time.RFC3339Nano)
			return nil
		})
		if err == nil {
			tb = s2.Thunderbird
		}
	}
	st := &a.tb
	st.mu.Lock()
	addr := fmt.Sprintf("127.0.0.1:%d", tb.Port)
	running := st.srv != nil
	if running && (!tb.Enabled || st.addr != addr) {
		_ = st.srv.Close()
		st.srv, st.addr = nil, ""
		running = false
	}
	st.key = tb.Key
	if tb.Enabled && !running {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			st.err = fmt.Sprintf("A %d-es helyi port nem nyitható meg (%v) – adjon meg másik portot.", tb.Port, err)
			log.Printf("thunderbird: %s", st.err)
		} else {
			st.err = ""
			srv := &http.Server{Handler: a.tbHandler(), ReadHeaderTimeout: 10 * time.Second}
			st.srv, st.addr = srv, addr
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Printf("thunderbird API: %v", err)
				}
			}()
			log.Printf("thunderbird: helyi API: http://%s", addr)
		}
	}
	if !tb.Enabled {
		st.err = ""
	}
	// Rules changed → the add-on should search again right away.
	rb, _ := json.Marshal(mailwatch.Rules(a.effectiveItems(s)))
	changed := string(rb) != st.rulesHash
	st.rulesHash = string(rb)
	st.mu.Unlock()
	if changed && tb.Enabled {
		a.Mail.Push(mailwatch.Command{Type: "scan"})
	}
}

func (a *App) stopThunderbird() {
	a.tb.mu.Lock()
	defer a.tb.mu.Unlock()
	if a.tb.srv != nil {
		_ = a.tb.srv.Close()
		a.tb.srv = nil
	}
}

func (a *App) tbHandler() http.Handler {
	mux := http.NewServeMux()
	auth := func(method string, h func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Only the add-on: web pages in a browser send their own origin.
			if o := r.Header.Get("Origin"); o != "" && !strings.HasPrefix(o, "moz-extension://") {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			if r.Method != method {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			a.tb.mu.Lock()
			key := a.tb.key
			a.tb.mu.Unlock()
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Hibás párosító kulcs – másolja be újra a BI Monitor Beállítások → Thunderbird kártyájáról."})
				return
			}
			a.Mail.Contact()
			r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
			h(w, r)
		}
	}
	mux.HandleFunc("/api/tb/v1/hello", auth("GET", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"app": "BI Output Monitor", "version": Version, "user": a.opt.Identity.Display, "host": a.opt.Identity.Host})
	}))
	mux.HandleFunc("/api/tb/v1/rules", auth("GET", func(w http.ResponseWriter, r *http.Request) {
		rules := mailwatch.Rules(a.effectiveItems(a.Settings.Get()))
		if rules == nil {
			rules = []mailwatch.Rule{}
		}
		writeJSON(w, 200, map[string]any{"rules": rules, "lookbackDays": mailwatch.LookbackDays, "scanIntervalSec": 120})
	}))
	mux.HandleFunc("/api/tb/v1/report", auth("POST", func(w http.ResponseWriter, r *http.Request) {
		var rep mailwatch.Report
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error()})
			return
		}
		a.Mail.Update(rep)
		if ids := a.emailItemIDs(); len(ids) > 0 {
			// answer after the re-check, so the add-on's next status call is current
			select {
			case <-a.Engine.CheckNow(ids...):
			case <-time.After(5 * time.Second):
			}
		}
		a.P.Push("thunderbird", a.tbStatus())
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/tb/v1/status", auth("GET", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, a.mailStatus())
	}))
	mux.HandleFunc("/api/tb/v1/poll", auth("GET", func(w http.ResponseWriter, r *http.Request) {
		wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
		if wait <= 0 || wait > 55 {
			wait = 25
		}
		cmds := a.Mail.Poll(r.Context(), time.Duration(wait)*time.Second)
		if cmds == nil {
			cmds = []mailwatch.Command{}
		}
		writeJSON(w, 200, map[string]any{"commands": cmds})
	}))
	mux.HandleFunc("/api/tb/v1/draft", auth("POST", func(w http.ResponseWriter, r *http.Request) {
		var d MailDraft
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil || strings.TrimSpace(d.Subject) == "" {
			writeJSON(w, 400, map[string]any{"error": "hiányzó tárgy"})
			return
		}
		a.tb.mu.Lock()
		a.tb.draft = &d
		a.tb.mu.Unlock()
		a.P.Push("mailDraft", d)
		a.P.ShowWindow()
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	mux.HandleFunc("/api/tb/v1/show", auth("POST", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.ID != "" {
			a.P.Push("showItem", p.ID)
		}
		a.P.ShowWindow()
		writeJSON(w, 200, map[string]any{"ok": true})
	}))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// MailItemStatus is one e-mail item as the add-on's toolbar popup shows it.
type MailItemStatus struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Group    string       `json:"group,omitempty"`
	Subject  string       `json:"subject"`
	Status   model.Status `json:"status"`
	Label    string       `json:"label"`
	Severity int          `json:"severity"`
	Reason   string       `json:"reason"`
	Schedule string       `json:"schedule"`
	Next     time.Time    `json:"next"`
	Last     *LastMail    `json:"last,omitempty"`
}

// LastMail is the newest matching message.
type LastMail struct {
	Subject   string    `json:"subject"`
	Date      time.Time `json:"date"`
	Folder    string    `json:"folder"`
	MessageID int64     `json:"messageId,omitempty"`
	HeaderID  string    `json:"headerId,omitempty"`
}

func (a *App) mailStatus() map[string]any {
	snap := a.snapshot()
	var out []MailItemStatus
	problems := 0
	for _, v := range snap.Items {
		if !v.Item.IsEmail() {
			continue
		}
		st := v.State
		ms := MailItemStatus{ID: v.Item.ID, Name: v.Item.Name, Group: v.Item.Group, Subject: v.Item.Mail.Subject, Status: st.Status,
			Label: st.Status.Label(), Severity: int(st.Status.Severity()), Reason: st.Reason, Schedule: st.ScheduleText, Next: st.Next}
		if st.File != nil {
			lm := &LastMail{Subject: st.File.Name, Date: st.File.ModTime, Folder: st.File.Path}
			if m, ok := a.Mail.Find(v.Item.ID, st.File.Name, st.File.ModTime, st.File.Path); ok {
				lm.MessageID, lm.HeaderID = m.ID, m.HeaderID
			}
			ms.Last = lm
		}
		if st.Status.Severity() >= model.SevWarning {
			problems++
		}
		out = append(out, ms)
	}
	if out == nil {
		out = []MailItemStatus{}
	}
	return map[string]any{"items": out, "problems": problems, "at": time.Now()}
}

// TBStatus is shown on the settings page.
type TBStatus struct {
	Enabled   bool           `json:"enabled"`
	Port      int            `json:"port"`
	Key       string         `json:"key"`
	StaleMin  int            `json:"staleMin"`
	Listening bool           `json:"listening"`
	Error     string         `json:"error,omitempty"`
	Info      mailwatch.Info `json:"info"`
	Rules     int            `json:"rules"`
	Profiles  []TBProfile    `json:"profiles"`
	// ProfileError: no Thunderbird profile found (one-click install impossible).
	ProfileError string `json:"profileError,omitempty"`
}

func (a *App) tbStatus() TBStatus {
	s := a.Settings.Get()
	a.tb.mu.Lock()
	st := TBStatus{Enabled: s.Thunderbird.Enabled, Port: s.Thunderbird.Port, Key: s.Thunderbird.Key, StaleMin: s.Thunderbird.StaleMin,
		Listening: a.tb.srv != nil, Error: a.tb.err}
	a.tb.mu.Unlock()
	st.Info = a.Mail.Info()
	st.Rules = len(mailwatch.Rules(a.effectiveItems(s)))
	if p, err := a.tbProfiles(); err != nil {
		st.ProfileError = err.Error()
	} else {
		st.Profiles = p
	}
	if st.Profiles == nil {
		st.Profiles = []TBProfile{}
	}
	return st
}

func (a *App) registerThunderbirdAPI() {
	a.register("tbStatus", func() (TBStatus, error) { return a.tbStatus(), nil })
	a.register("tbInstall", func() (TBInstallResult, error) { return a.installThunderbird() })

	type tbSave struct {
		Enabled  bool `json:"enabled"`
		Port     int  `json:"port"`
		StaleMin int  `json:"staleMin"`
	}
	a.register("tbSave", func(p tbSave) (TBStatus, error) {
		if p.Port < 1024 || p.Port > 65535 {
			return a.tbStatus(), errors.New("a port 1024 és 65535 között lehet")
		}
		if p.StaleMin < 2 || p.StaleMin > 24*60 {
			return a.tbStatus(), errors.New("a riasztási idő 2 perc és 24 óra között lehet")
		}
		_, err := a.Settings.Update(func(s *model.Settings) error {
			s.Thunderbird.Enabled, s.Thunderbird.Port, s.Thunderbird.StaleMin = p.Enabled, p.Port, p.StaleMin
			return nil
		})
		if err != nil {
			return a.tbStatus(), err
		}
		a.syncThunderbird()
		a.reconfigure()
		return a.tbStatus(), nil
	})
	a.register("tbNewKey", func() (TBStatus, error) {
		if _, err := a.Settings.Update(func(s *model.Settings) error {
			s.Thunderbird.Key, s.Thunderbird.KeyIssued = newKey(), time.Now().UTC().Format(time.RFC3339Nano)
			return nil
		}); err != nil {
			return a.tbStatus(), err
		}
		a.syncThunderbird()
		a.refreshInstalledXPI(true) // the installed add-on gets the new key on the next Thunderbird start
		return a.tbStatus(), nil
	})
	type openMail struct {
		ID      string    `json:"id"`
		Subject string    `json:"subject"`
		Date    time.Time `json:"date"`
		Path    string    `json:"path"`
	}
	a.register("openMail", func(p openMail) error {
		m, ok := a.Mail.Find(p.ID, p.Subject, p.Date, p.Path)
		if !ok {
			return errors.New("ez az e-mail már nincs a Thunderbird legutóbbi jelentésében")
		}
		if a.Mail.Info().LastContact.Before(time.Now().Add(-2 * time.Minute)) {
			return errors.New("a Thunderbird-bővítmény most nem kapcsolódik – fut a Thunderbird?")
		}
		a.Mail.Push(mailwatch.Command{Type: "open", MessageID: m.ID, HeaderID: m.HeaderID})
		return nil
	})
	// openURL opens a web page (the add-on download) in the default browser.
	a.register("openURL", func(u string) error {
		if !strings.HasPrefix(u, "https://") {
			return errors.New("csak https:// cím nyitható meg")
		}
		return a.P.ShellOpen(u)
	})
	a.register("takeMailDraft", func() (*MailDraft, error) {
		a.tb.mu.Lock()
		defer a.tb.mu.Unlock()
		d := a.tb.draft
		a.tb.draft = nil
		return d, nil
	})
}
