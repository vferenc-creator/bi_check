// Package notify decides WHEN to tell the user about something:
//
//   - one notification per problem ("incident"), never repeated – also not
//     after a restart (incidents are persisted);
//   - a recovery notice when a notified problem resolves;
//   - nothing during quiet hours or while paused; when quiet hours end, one
//     summary of the problems that are still open;
//   - per-item mute, acknowledged problems are skipped.
//
// Formatting/batching of the resulting notices lives in batch.go.
package notify

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/engine"
	"bimonitor/internal/model"
	"bimonitor/internal/store"
)

// Kind of a notice.
type Kind int

const (
	KindProblem Kind = iota
	KindRecovery
	KindQuietSummary
)

// Notice is something worth telling the user.
type Notice struct {
	Kind          Kind
	Item          model.Item
	Status        model.Status
	Reason        string
	Server        string
	ServerProblem bool
	Toast         bool // show a desktop notification
	At            time.Time
	// For KindQuietSummary: the still-open problems.
	Items []engine.ItemState
	Names map[string]string
}

// IncidentStore persists open incidents.
type IncidentStore interface {
	Incidents() map[string]store.Incident
	SetIncident(itemID string, in *store.Incident) error
}

// Policy is safe for concurrent use.
type Policy struct {
	st         IncidentStore
	mu         sync.Mutex
	incidents  map[string]store.Incident
	suppressed map[string]bool
	wasQuiet   bool
}

// NewPolicy loads persisted incidents (st may be nil).
func NewPolicy(st IncidentStore) *Policy {
	p := &Policy{st: st, incidents: map[string]store.Incident{}, suppressed: map[string]bool{}}
	if st != nil {
		p.incidents = st.Incidents()
	}
	return p
}

// IsProblem reports whether a status deserves a notification.
func IsProblem(st model.Status, s model.Settings) bool {
	switch st {
	case model.StatusMissing, model.StatusUnreachable, model.StatusSuspicious:
		return true
	case model.StatusLate:
		return s.Notifications.OnLate
	}
	return false
}

// IncidentKey identifies one problem occurrence.
func IncidentKey(st engine.ItemState) string {
	switch st.Status {
	case model.StatusUnreachable:
		return "unreachable"
	case model.StatusSuspicious:
		if st.File != nil {
			return fmt.Sprintf("suspicious|%d|%d", st.File.ModTime.Unix(), st.File.Size)
		}
		return "suspicious"
	}
	return fmt.Sprintf("%s|%d", st.Status, st.Expected.Unix())
}

// InQuiet reports whether t falls into the quiet hours.
func InQuiet(q model.QuietHours, t time.Time) bool {
	if q.Weekends && (t.Weekday() == time.Saturday || t.Weekday() == time.Sunday) {
		return true
	}
	if !q.Enabled {
		return false
	}
	from, ok1 := clock(q.From)
	to, ok2 := clock(q.To)
	if !ok1 || !ok2 || from == to {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if from < to {
		return m >= from && m < to
	}
	return m >= from || m < to
}

func clock(s string) (int, bool) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// ToastsAllowed reports whether desktop notifications may be shown now.
func ToastsAllowed(s model.Settings, now time.Time) bool {
	n := s.Notifications
	return n.Enabled && !now.Before(n.PausedUntil) && !InQuiet(n.Quiet, now)
}

// OnTransition evaluates a status change. muted = personal override for
// shared items.
func (p *Policy) OnTransition(ev engine.Event, s model.Settings, muted bool, now time.Time) []Notice {
	p.mu.Lock()
	defer p.mu.Unlock()
	it, ns := ev.Item, ev.New
	id := it.ID
	inc, hasInc := p.incidents[id]

	if IsProblem(ns.Status, s) {
		key := IncidentKey(ns)
		if hasInc && hasKey(inc.Key, key) {
			return nil // already told the user about this problem
		}
		if !it.Notify || muted || ns.Acked {
			return nil
		}
		toast := ToastsAllowed(s, now)
		if !toast && s.Notifications.Enabled {
			p.suppressed[id] = true
		}
		if !toast && !p.suppressed[id] {
			return nil
		}
		keys := key
		if hasInc {
			keys = inc.Key + "\n" + key // remember every problem until recovery
		}
		p.setIncident(id, &store.Incident{Key: keys, Status: string(ns.Status), At: now})
		return []Notice{{Kind: KindProblem, Item: it, Status: ns.Status, Reason: ns.Reason, Server: ns.Server,
			ServerProblem: ns.ServerProblem, Toast: toast, At: now}}
	}

	switch ns.Status {
	case model.StatusOK, model.StatusWaiting:
		if ns.Gap {
			// Inside the allowed gap window nothing is decided: an open
			// problem stays open (no "recovered" message), none is raised.
			return nil
		}
		if !hasInc {
			delete(p.suppressed, id)
			return nil
		}
		p.setIncident(id, nil)
		wasSuppressed := p.suppressed[id]
		delete(p.suppressed, id)
		if !it.Notify || muted {
			return nil
		}
		toast := s.Notifications.OnRecovery && ToastsAllowed(s, now) && !wasSuppressed
		if !toast {
			return nil
		}
		return []Notice{{Kind: KindRecovery, Item: it, Status: ns.Status, Reason: ns.Reason, Toast: toast, At: now}}
	case model.StatusLate:
		// Late without OnLate: keep any incident (e.g. a new expectation
		// after a missing one will get its own key later).
		return nil
	default: // disabled, unknown
		if hasInc {
			p.setIncident(id, nil)
		}
		delete(p.suppressed, id)
	}
	return nil
}

func hasKey(keys, key string) bool {
	for _, k := range strings.Split(keys, "\n") {
		if k == key {
			return true
		}
	}
	return false
}

func (p *Policy) setIncident(id string, in *store.Incident) {
	if in == nil {
		delete(p.incidents, id)
	} else {
		p.incidents[id] = *in
	}
	if p.st != nil {
		_ = p.st.SetIncident(id, in)
	}
}

// Forget drops an item's bookkeeping (item deleted).
func (p *Policy) Forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.incidents[id]; ok {
		p.setIncident(id, nil)
	}
	delete(p.suppressed, id)
}

// Tick must be called periodically; when quiet hours/pause end it returns a
// summary of the problems that were suppressed and are still open.
func (p *Policy) Tick(s model.Settings, now time.Time, states []engine.ItemState, names map[string]string) []Notice {
	p.mu.Lock()
	defer p.mu.Unlock()
	quiet := !ToastsAllowed(s, now)
	defer func() { p.wasQuiet = quiet }()
	if quiet || !p.wasQuiet || !s.Notifications.Enabled {
		return nil
	}
	var open []engine.ItemState
	for _, st := range states {
		if p.suppressed[st.ID] && IsProblem(st.Status, s) && !st.Acked {
			open = append(open, st)
		}
	}
	p.suppressed = map[string]bool{}
	if len(open) == 0 {
		return nil
	}
	return []Notice{{Kind: KindQuietSummary, Toast: true, Items: open, Names: names, At: now}}
}
