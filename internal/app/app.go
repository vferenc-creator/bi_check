// Package app is the platform-independent heart of the monitor: it owns the
// settings, the monitoring engine and the history, and exposes an RPC API
// that the web UI calls.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"sort"
	"sync"
	"time"

	"bimonitor/branding"
	"bimonitor/internal/calendar"
	"bimonitor/internal/checker"
	"bimonitor/internal/engine"
	"bimonitor/internal/model"
	"bimonitor/internal/notify"
	"bimonitor/internal/store"
	"bimonitor/internal/teamstore"
)

// Version is set at build time (-ldflags "-X bimonitor/internal/app.Version=1.2.3").
var Version = "dev"

// App wires everything together.
type App struct {
	P        Platform
	Settings *store.SettingsStore
	Loc      *time.Location

	Checker *checker.Checker
	Engine  *engine.Engine
	History *store.History // nil if the database could not be opened

	policy      *notify.Policy
	noticeMu    sync.Mutex
	noticeBuf   []notify.Notice
	noticeTimer *time.Timer
	stop        chan struct{}

	mu       sync.Mutex
	handlers map[string]handler
	closed   bool
	cancel   context.CancelFunc
	cal      *calendar.Calendar

	pushMu      sync.Mutex
	pushPending bool
	lastTray    string

	shared map[string]*sharedState // loaded shared team lists

	opt  Options
	team *teamStateT

	// OnCall, if set, is invoked before every RPC (used by --selftest).
	OnCall func(method string)
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
	// LocalDir is the machine-local data folder (shared-mode cache).
	LocalDir string
	// Identity of the user/PC for the shared mode (defaults from env).
	Identity teamstore.Identity
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
	a := &App{P: p, Settings: st, Loc: loc, handlers: map[string]handler{}, stop: make(chan struct{}), shared: map[string]*sharedState{}, opt: opt}
	a.opt.Identity = defaultIdentity(opt.Identity)
	if opt.HistoryPath != "" {
		h, err := store.OpenHistory(opt.HistoryPath)
		if err != nil {
			log.Printf("history: %v (előzmények nélkül fut)", err)
		} else {
			a.History = h
		}
	}
	if a.History != nil {
		a.policy = notify.NewPolicy(a.History)
	} else {
		a.policy = notify.NewPolicy(nil)
	}
	s := st.Get()
	a.cal = calendar.Default
	a.Checker = checker.New(nil, checker.Options{Timeout: time.Duration(s.TimeoutSec) * time.Second})
	a.Engine = engine.New(engine.Config{
		Checker:  a.Checker,
		Loc:      loc,
		OnEvents: a.handleEvents,
		OnChange: a.schedulePush,
		SizeHistory: func(id string) []int64 {
			if a.History == nil {
				return nil
			}
			return a.History.RecentSizes(id, 10)
		},
	})
	a.registerAll()
	a.registerItemAPI()
	a.registerHistoryAPI()
	a.registerTransferAPI()
	a.registerEmailAPI()
	a.registerTeamAPI()
	a.registerSharedModeAPI()
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
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.reconfigure()
	go func() {
		// Shared lists live on network shares: load them in the background.
		if a.refreshShared(true) {
			a.reconfigure()
		}
	}()
	go a.Engine.Run(ctx)
	go a.background(a.stop)
	if s := a.Settings.Get(); s.Team.Enabled && s.Team.Folder != "" {
		a.startTeam(s.Team)
	}
}

// Close stops background work.
func (a *App) Close() {
	a.stopTeam() // releases our edit locks on the share
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	if a.cancel != nil {
		a.cancel()
	}
	close(a.stop)
	if a.History != nil {
		time.Sleep(50 * time.Millisecond) // let the last heartbeat land
		a.History.Close()
	}
}

// reconfigure pushes the current settings into the engine.
func (a *App) reconfigure() {
	s := a.Settings.Get()
	cal := calendar.Default
	a.mu.Lock()
	a.cal = cal
	a.mu.Unlock()
	a.Checker.SetTimeout(time.Duration(s.TimeoutSec) * time.Second)
	a.Engine.Configure(a.effectiveItems(s), cal, time.Duration(s.CheckIntervalSec)*time.Second, s.Parallelism)
}

// Calendar returns the working-day calendar in use.
func (a *App) Calendar() *calendar.Calendar {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cal
}

// schedulePush coalesces state changes into one UI push every 250 ms.
func (a *App) schedulePush() {
	a.pushMu.Lock()
	defer a.pushMu.Unlock()
	if a.pushPending {
		return
	}
	a.pushPending = true
	time.AfterFunc(250*time.Millisecond, func() {
		a.pushMu.Lock()
		a.pushPending = false
		a.pushMu.Unlock()
		a.P.Push("state", a.snapshot())
		a.updateTray()
	})
}

func (a *App) updateTray() {
	counts, worst := a.Engine.Summary()
	name := branding.Current.AppName
	total := 0
	for _, n := range counts {
		total += n
	}
	var tip string
	errs := counts[model.StatusMissing] + counts[model.StatusUnreachable]
	warns := counts[model.StatusLate] + counts[model.StatusSuspicious]
	switch {
	case total == 0:
		tip = name + " – nincs figyelt elem"
	case errs == 0 && warns == 0:
		tip = fmt.Sprintf("%s – minden rendben (%d elem)", name, total)
	default:
		tip = fmt.Sprintf("%s – %d hiba, %d figyelmeztetés", name, errs, warns)
	}
	if paused := a.Settings.Get().Notifications.PausedUntil; paused.After(time.Now()) {
		tip += " · értesítések szünetelnek"
	}
	key := fmt.Sprintf("%d|%s", worst, tip)
	a.pushMu.Lock()
	changed := key != a.lastTray
	a.lastTray = key
	a.pushMu.Unlock()
	if changed {
		a.P.UpdateTray(worst, tip)
	}
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
	if a.OnCall != nil {
		a.OnCall(method)
	}
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
		a.reconfigure()
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

	a.register("pauseNotifications", func(until string) error {
		var t time.Time
		if until != "" {
			var err error
			if t, err = time.Parse(time.RFC3339, until); err != nil {
				return fmt.Errorf("érvénytelen időpont: %w", err)
			}
		}
		a.PauseNotifications(t)
		a.updateTray()
		return nil
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
