// Package app is the platform-independent heart of the monitor: it owns the
// settings, the monitoring engine and the history, and exposes an RPC API
// that the web UI calls.
package app

import (
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"sort"
	"sync"
	"time"

	"bimonitor/branding"
	"bimonitor/internal/model"
	"bimonitor/internal/store"
)

// Version is set at build time (-ldflags "-X bimonitor/internal/app.Version=1.2.3").
var Version = "dev"

// App wires everything together.
type App struct {
	P        Platform
	Settings *store.SettingsStore
	Loc      *time.Location

	mu       sync.Mutex
	handlers map[string]handler
	closed   bool
}

type handler struct {
	fn     reflect.Value
	params reflect.Type // nil = no params
}

// Options for New.
type Options struct {
	SettingsPath string
	HistoryPath  string
	Location     *time.Location
}

// New loads the settings and prepares the app (call Start to begin monitoring).
func New(p Platform, opt Options) (*App, error) {
	st, err := store.OpenSettings(opt.SettingsPath)
	if err != nil {
		return nil, fmt.Errorf("beállítások betöltése: %w", err)
	}
	loc := opt.Location
	if loc == nil {
		loc = loadBudapest()
	}
	a := &App{P: p, Settings: st, Loc: loc, handlers: map[string]handler{}}
	a.registerAll()
	return a, nil
}

func loadBudapest() *time.Location {
	if l, err := time.LoadLocation("Europe/Budapest"); err == nil {
		return l
	}
	return time.Local
}

// Start begins background work.
func (a *App) Start() {
	a.syncAutostart()
}

// Close stops background work.
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
}

// syncAutostart makes the registry match the setting (also fixes the path
// if the exe was moved).
func (a *App) syncAutostart() {
	s := a.Settings.Get()
	if err := a.P.SetAutostart(s.Autostart); err != nil {
		log.Printf("autostart: %v", err)
	}
}

// register adds an RPC method. fn must be func() (R, error) or func(P) (R, error)
// (or return only error).
func (a *App) register(name string, fn any) {
	v := reflect.ValueOf(fn)
	t := v.Type()
	h := handler{fn: v}
	if t.NumIn() == 1 {
		h.params = t.In(0)
	} else if t.NumIn() > 1 {
		panic("rpc " + name + ": at most one parameter")
	}
	a.handlers[name] = h
}

// Methods lists the registered RPC names.
func (a *App) Methods() []string {
	out := make([]string, 0, len(a.handlers))
	for k := range a.handlers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Call invokes an RPC method with JSON params.
func (a *App) Call(method string, params json.RawMessage) (result any, err error) {
	h, ok := a.handlers[method]
	if !ok {
		return nil, fmt.Errorf("ismeretlen művelet: %s", method)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("rpc %s panic: %v", method, r)
			err = fmt.Errorf("belső hiba: %v", r)
		}
	}()
	var args []reflect.Value
	if h.params != nil {
		pv := reflect.New(h.params)
		if len(params) > 0 && string(params) != "null" {
			if err := json.Unmarshal(params, pv.Interface()); err != nil {
				return nil, fmt.Errorf("hibás paraméter: %w", err)
			}
		}
		args = append(args, pv.Elem())
	}
	out := h.fn.Call(args)
	switch len(out) {
	case 1:
		if e, _ := out[0].Interface().(error); e != nil {
			return nil, e
		}
		if out[0].Type().Implements(reflect.TypeOf((*error)(nil)).Elem()) {
			return nil, nil
		}
		return out[0].Interface(), nil
	case 2:
		if e, _ := out[1].Interface().(error); e != nil {
			return nil, e
		}
		return out[0].Interface(), nil
	}
	return nil, nil
}

// ---- RPC methods ---------------------------------------------------------

// Bootstrap is the first call of the UI.
type Bootstrap struct {
	AppName      string         `json:"appName"`
	Company      string         `json:"company"`
	Version      string         `json:"version"`
	Settings     model.Settings `json:"settings"`
	SettingsPath string         `json:"settingsPath"`
	Warning      string         `json:"warning,omitempty"`
}

// SettingsPatch is what the settings page saves (everything except items).
type SettingsPatch struct {
	CheckIntervalSec int                        `json:"checkIntervalSec"`
	TimeoutSec       int                        `json:"timeoutSec"`
	Parallelism      int                        `json:"parallelism"`
	Autostart        bool                       `json:"autostart"`
	HistoryDays      int                        `json:"historyDays"`
	Theme            string                     `json:"theme"`
	Notifications    model.NotificationSettings `json:"notifications"`
}

func (a *App) registerAll() {
	a.register("bootstrap", func() (Bootstrap, error) {
		s := a.Settings.Get()
		s.Autostart = a.P.AutostartEnabled()
		return Bootstrap{
			AppName:      branding.Current.AppName,
			Company:      branding.Current.Company,
			Version:      Version,
			Settings:     redact(s),
			SettingsPath: a.Settings.Path(),
			Warning:      a.Settings.LoadWarning,
		}, nil
	})

	a.register("saveSettings", func(p SettingsPatch) (model.Settings, error) {
		s, err := a.Settings.Update(func(s *model.Settings) error {
			if p.CheckIntervalSec < 10 || p.CheckIntervalSec > 3600 {
				return fmt.Errorf("az ellenőrzési gyakoriság 10 és 3600 másodperc között lehet")
			}
			if p.TimeoutSec < 1 || p.TimeoutSec > 120 {
				return fmt.Errorf("az időkorlát 1 és 120 másodperc között lehet")
			}
			s.CheckIntervalSec = p.CheckIntervalSec
			s.TimeoutSec = p.TimeoutSec
			if p.Parallelism > 0 {
				s.Parallelism = p.Parallelism
			}
			if p.HistoryDays > 0 {
				s.HistoryDays = p.HistoryDays
			}
			s.Autostart = p.Autostart
			s.Theme = p.Theme
			paused := s.Notifications.PausedUntil
			s.Notifications = p.Notifications
			s.Notifications.PausedUntil = paused
			return nil
		})
		if err != nil {
			return redact(s), err
		}
		if err := a.P.SetAutostart(s.Autostart); err != nil {
			return redact(s), fmt.Errorf("az automatikus indítás beállítása nem sikerült: %w", err)
		}
		return redact(s), nil
	})

	a.register("setAutostart", func(on bool) error {
		if _, err := a.Settings.Update(func(s *model.Settings) error { s.Autostart = on; return nil }); err != nil {
			return err
		}
		return a.P.SetAutostart(on)
	})

	a.register("openDataFolder", func() error {
		return a.P.ShellOpen(dirOf(a.Settings.Path()))
	})
}

// redact removes secrets before sending settings to the UI.
func redact(s model.Settings) model.Settings {
	if s.Email.PasswordEnc != "" {
		s.Email.PasswordEnc = "********"
	}
	return s
}

func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '\\' || p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// PauseNotifications mutes toasts until t (zero time = resume).
func (a *App) PauseNotifications(t time.Time) {
	if _, err := a.Settings.Update(func(s *model.Settings) error { s.Notifications.PausedUntil = t; return nil }); err != nil {
		log.Printf("pause notifications: %v", err)
	}
	a.P.Push("settings", redact(a.Settings.Get()))
}
