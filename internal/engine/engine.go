// Package engine runs the checks in the background: every item is checked
// periodically and additionally right when its expected time or deadline
// passes. All file system work happens on worker goroutines with timeouts,
// so nothing here can block the UI.
package engine

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/checker"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
	"bimonitor/internal/status"
)

// ItemState is the live state of one item (sent to the UI as JSON).
type ItemState struct {
	ID            string             `json:"id"`
	Status        model.Status       `json:"status"`
	Reason        string             `json:"reason"`
	Since         time.Time          `json:"since"`
	Expected      time.Time          `json:"expected"`
	Deadline      time.Time          `json:"deadline"`
	WindowStart   time.Time          `json:"windowStart"`
	Next          time.Time          `json:"next"`
	File          *checker.FileInfo  `json:"file,omitempty"`
	Fresh         bool               `json:"fresh"`
	Matches       int                `json:"matches"`
	Candidates    []checker.FileInfo `json:"candidates,omitempty"`
	Resolved      string             `json:"resolved"`
	Server        string             `json:"server"`
	ServerProblem bool               `json:"serverProblem"`
	LastCheck     time.Time          `json:"lastCheck"`
	TookMs        int64              `json:"tookMs"`
	Checking      bool               `json:"checking"`
	ScheduleText  string             `json:"scheduleText"`
	ScheduleError string             `json:"scheduleError,omitempty"`
	// Gap: missing, but inside the item's allowed gap window.
	Gap bool `json:"gap,omitempty"`
	// Acked: the user acknowledged the current problem (no more reminders
	// until the next expected time).
	Acked bool `json:"acked"`
	// Recheck: the last check could not reach the file; before showing
	// (and notifying) "unreachable" the engine looks once more shortly.
	Recheck       bool   `json:"recheck,omitempty"`
	RecheckReason string `json:"recheckReason,omitempty"`
}

// EventKind distinguishes engine events.
type EventKind string

const (
	EvTransition EventKind = "transition" // status changed
	EvArrival    EventKind = "arrival"    // a new file version was seen
)

// Event is emitted after checks.
type Event struct {
	Kind EventKind
	Item model.Item
	Old  ItemState
	New  ItemState
	// Arrival details.
	File     checker.FileInfo
	Expected time.Time
	Delay    time.Duration
	// Initial is true when the file was seen for the first time since start.
	Initial bool
}

// Config of the engine.
type Config struct {
	Checker *checker.Checker
	Loc     *time.Location
	Now     func() time.Time
	// OnEvents receives transitions/arrivals (called from worker goroutines,
	// never concurrently).
	OnEvents func([]Event)
	// OnChange is called whenever any visible state changed.
	OnChange func()
	// SizeHistory returns recent arrival sizes (newest first) for an item.
	SizeHistory func(id string) []int64
	// ConfirmUnreachable is the delay of the confirming re-check before an
	// item turns unreachable (0 = 20s, negative = report right away).
	ConfirmUnreachable time.Duration
}

type entry struct {
	item     model.Item
	sched    *schedule.Schedule
	schedErr string
	state    ItemState
	next     time.Time // next periodic check
	force    bool
	waiters  []chan struct{}
	// unconfirmed: the previous check was unreachable but not shown yet.
	unconfirmed bool
}

// Engine schedules and runs checks.
type Engine struct {
	cfg Config

	mu          sync.Mutex
	entries     map[string]*entry
	order       []string
	interval    time.Duration
	parallelism int
	sem         chan struct{}
	wake        chan struct{}
	evMu        sync.Mutex
}

// New creates an engine.
func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Loc == nil {
		cfg.Loc = time.Local
	}
	if cfg.OnEvents == nil {
		cfg.OnEvents = func([]Event) {}
	}
	if cfg.OnChange == nil {
		cfg.OnChange = func() {}
	}
	if cfg.SizeHistory == nil {
		cfg.SizeHistory = func(string) []int64 { return nil }
	}
	if cfg.ConfirmUnreachable == 0 {
		cfg.ConfirmUnreachable = 20 * time.Second
	}
	return &Engine{
		cfg:         cfg,
		entries:     map[string]*entry{},
		interval:    time.Minute,
		parallelism: 6,
		sem:         make(chan struct{}, 6),
		wake:        make(chan struct{}, 1),
	}
}

// checkRelevant are the item fields that change what a check finds.
type checkRelevant struct {
	Path     string
	Token    model.TokenMode
	Schedule schedule.Spec
	Grace    int
	Early    int
	Rules    model.SuspiciousRules
	Gap      model.GapWindow
	Enabled  bool
}

func relevant(it model.Item) checkRelevant {
	return checkRelevant{it.Path, it.Token, it.Schedule, it.GraceMinutes, it.EarlyMinutes, it.Suspicious, it.Gap, it.Enabled}
}

// Configure replaces the item list and timing settings. States of unchanged
// items are kept; changed and new items are checked right away.
func (e *Engine) Configure(items []model.Item, cal *calendar.Calendar, interval time.Duration, parallelism int) {
	e.mu.Lock()
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	e.interval = interval
	if parallelism < 1 {
		parallelism = 1
	}
	if parallelism != e.parallelism {
		e.parallelism = parallelism
		e.sem = make(chan struct{}, parallelism)
	}
	now := e.cfg.Now()
	seen := map[string]bool{}
	e.order = e.order[:0]
	for _, it := range items {
		seen[it.ID] = true
		e.order = append(e.order, it.ID)
		old := e.entries[it.ID]
		var sc *schedule.Schedule
		schedErr := ""
		if c, err := schedule.Compile(it.Schedule, cal, e.cfg.Loc); err != nil {
			schedErr = err.Error()
		} else {
			sc = c
		}
		var en *entry
		if old != nil && reflect.DeepEqual(relevant(old.item), relevant(it)) && old.schedErr == schedErr {
			// Same checks as before: keep the entry (and any running check).
			en = old
			en.item, en.sched = it, sc
		} else {
			en = &entry{item: it, sched: sc, schedErr: schedErr, force: true}
			en.state = ItemState{ID: it.ID, Status: model.StatusUnknown, Since: now}
			if old != nil {
				en.waiters = old.waiters
				old.waiters = nil
				// keep the last seen file to avoid a bogus "arrival"
				en.state.File = old.state.File
			}
		}
		en.state.ScheduleText = it.Schedule.Describe()
		en.state.ScheduleError = en.schedErr
		if !it.Enabled {
			if en.state.Status != model.StatusDisabled {
				en.state.Since = now
			}
			en.state.Status = model.StatusDisabled
			en.state.Reason = "Az elem ki van kapcsolva."
			en.state.Checking = false
			en.force = false
		} else if en.schedErr != "" {
			en.state.Status = model.StatusUnknown
			en.state.Reason = "Hibás ütemezés: " + en.schedErr
			en.force = false
		}
		if en.sched != nil {
			if t, ok := en.sched.Next(now); ok {
				en.state.Next = t
			}
		}
		e.entries[it.ID] = en
	}
	for id, en := range e.entries {
		if !seen[id] {
			for _, w := range en.waiters {
				close(w)
			}
			delete(e.entries, id)
		}
	}
	e.mu.Unlock()
	e.kick()
	e.cfg.OnChange()
}

func (e *Engine) kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// CheckNow forces a check of the given items (all if empty) and returns a
// channel that is closed when they are done.
func (e *Engine) CheckNow(ids ...string) <-chan struct{} {
	e.mu.Lock()
	if len(ids) == 0 {
		ids = append([]string(nil), e.order...)
	}
	var chans []chan struct{}
	for _, id := range ids {
		en := e.entries[id]
		if en == nil || !en.item.Enabled || en.sched == nil {
			continue
		}
		en.force = true
		c := make(chan struct{})
		en.waiters = append(en.waiters, c)
		chans = append(chans, c)
	}
	e.mu.Unlock()
	e.kick()
	done := make(chan struct{})
	go func() {
		for _, c := range chans {
			<-c
		}
		close(done)
	}()
	return done
}

// SetAcked marks/unmarks the current problem of an item as acknowledged.
func (e *Engine) SetAcked(id string, acked bool) {
	e.mu.Lock()
	if en := e.entries[id]; en != nil {
		en.state.Acked = acked
	}
	e.mu.Unlock()
	e.cfg.OnChange()
}

// States returns all states in item order.
func (e *Engine) States() []ItemState {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]ItemState, 0, len(e.order))
	for _, id := range e.order {
		if en := e.entries[id]; en != nil {
			out = append(out, en.state)
		}
	}
	return out
}

// State returns one item's state.
func (e *Engine) State(id string) (ItemState, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if en := e.entries[id]; en != nil {
		return en.state, true
	}
	return ItemState{}, false
}

// Summary counts states and returns the worst severity.
func (e *Engine) Summary() (map[model.Status]int, model.Severity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[model.Status]int{}
	worst := model.SevNone
	for _, en := range e.entries {
		counts[en.state.Status]++
		if s := en.state.Status.Severity(); s > worst {
			worst = s
		}
	}
	return counts, worst
}

// Run loops until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		e.dispatchDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-e.wake:
		}
	}
}

// dispatchDue starts checks for every item that needs one.
func (e *Engine) dispatchDue(ctx context.Context) {
	now := e.cfg.Now()
	var due []*entry
	e.mu.Lock()
	for _, id := range e.order {
		en := e.entries[id]
		if en == nil || en.state.Checking || !en.item.Enabled || en.sched == nil {
			continue
		}
		need := en.force || !now.Before(en.next)
		if !need {
			if exp, ok := en.sched.Prev(now); ok && !exp.Equal(en.state.Expected) {
				need = true // a new expected time has come
			}
		}
		if !need && en.state.Status == model.StatusLate && !now.Before(en.state.Deadline) {
			need = true // grace period just ended
		}
		if need {
			en.state.Checking = true
			en.force = false
			due = append(due, en)
		}
	}
	sem := e.sem
	e.mu.Unlock()
	if len(due) == 0 {
		return
	}
	e.cfg.OnChange()
	for _, en := range due {
		en := en
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		go func() {
			defer func() { <-sem }()
			e.checkOne(en)
		}()
	}
}

func (e *Engine) checkOne(en *entry) {
	e.mu.Lock()
	item := en.item
	sc := en.sched
	old := en.state
	e.mu.Unlock()

	now := e.cfg.Now()
	exp, hasExp := sc.Prev(now)
	var prev time.Time
	if hasExp {
		prev, _ = sc.PrevBefore(exp)
	}
	ref := now
	if hasExp {
		ref = exp
	}
	res := e.cfg.Checker.Check(item.Path, ref.In(e.cfg.Loc), item.Token == model.TokenAny)
	out := status.Evaluate(status.Input{
		Now: e.cfg.Now(), Expected: exp, HasExpected: hasExp, PrevExpected: prev,
		Grace: item.Grace(), Early: item.Early(), Result: res, Rules: item.Suspicious, Gap: item.Gap,
		RecentSizes: e.cfg.SizeHistory(item.ID), Loc: e.cfg.Loc,
	})

	ns := old
	ns.Checking = false
	ns.Recheck, ns.RecheckReason = false, ""
	ns.Status = out.Status
	ns.Reason = out.Reason
	ns.Expected = exp
	ns.Deadline = out.Deadline
	ns.WindowStart = out.WindowStart
	ns.Fresh = out.Fresh
	ns.Gap = out.Gap
	ns.Matches = res.Matches
	ns.Candidates = res.Candidates
	ns.Resolved = res.Resolved
	ns.Server = res.Server
	ns.ServerProblem = res.ServerProblem
	ns.LastCheck = res.CheckedAt
	ns.TookMs = res.TookMs
	if res.Kind != checker.Unreachable {
		ns.File = res.File // keep the last known file while unreachable
	}
	if t, ok := sc.Next(now); ok {
		ns.Next = t
	}
	if ns.Status != old.Status || old.Status == model.StatusUnknown {
		ns.Since = now
	}
	if !exp.Equal(old.Expected) || ns.Status.Severity() < model.SevWarning {
		ns.Acked = false // a new expectation or recovery clears the acknowledgement
	}

	var events []Event
	if old.Status != ns.Status {
		events = append(events, Event{Kind: EvTransition, Item: item, Old: old, New: ns})
	}
	if ns.File != nil && res.Kind == checker.Found {
		changed := old.File == nil || !old.File.ModTime.Equal(ns.File.ModTime) || old.File.Path != ns.File.Path || old.File.Size != ns.File.Size
		if changed {
			ev := Event{Kind: EvArrival, Item: item, Old: old, New: ns, File: *ns.File, Initial: old.File == nil}
			if out.Fresh {
				ev.Expected = exp
				ev.Delay = ns.File.ModTime.Sub(exp)
			}
			events = append(events, ev)
		}
	}

	e.mu.Lock()
	var waiters []chan struct{}
	if cur := e.entries[item.ID]; cur == en {
		if ns.Status == model.StatusUnreachable && old.Status != model.StatusUnreachable &&
			!en.unconfirmed && e.cfg.ConfirmUnreachable > 0 {
			// Network shares hiccup (dropped SMB session, busy server):
			// keep the previous status and look again soon; only a second
			// failure in a row is shown and notified.
			en.unconfirmed = true
			en.state.Checking = false
			en.state.Recheck, en.state.RecheckReason = true, ns.Reason
			en.next = now.Add(e.cfg.ConfirmUnreachable)
			events = nil
		} else {
			en.unconfirmed = false
			en.state = ns
			en.next = now.Add(e.interval)
		}
		waiters = en.waiters
		en.waiters = nil
	} else {
		events = nil // item was reconfigured meanwhile; drop stale results
		if cur != nil {
			cur.state.Checking = false
		}
		waiters = en.waiters
		en.waiters = nil
	}
	e.mu.Unlock()

	if len(events) > 0 {
		e.evMu.Lock()
		e.cfg.OnEvents(events)
		e.evMu.Unlock()
	}
	e.cfg.OnChange()
	for _, w := range waiters {
		close(w)
	}
}

// Servers exposes the checker's breaker states.
func (e *Engine) Servers() []checker.ServerStatus { return e.cfg.Checker.Servers() }

// SortStates orders states by severity (worst first), then by name.
func SortStates(states []ItemState, name func(id string) string) {
	rank := map[model.Status]int{
		model.StatusMissing: 0, model.StatusUnreachable: 1, model.StatusSuspicious: 2, model.StatusLate: 3,
		model.StatusUnknown: 4, model.StatusWaiting: 5, model.StatusOK: 6, model.StatusDisabled: 7,
	}
	sort.SliceStable(states, func(i, j int) bool {
		ri, rj := rank[states[i].Status], rank[states[j].Status]
		if ri != rj {
			return ri < rj
		}
		return name(states[i].ID) < name(states[j].ID)
	})
}
