// Package model holds the persisted data types shared by all layers.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"bimonitor/internal/schedule"
)

// Status of a watched item.
type Status string

const (
	StatusUnknown     Status = "unknown"     // not checked yet
	StatusWaiting     Status = "waiting"     // no expected drop yet
	StatusOK          Status = "ok"          // arrived after the last expected time
	StatusLate        Status = "late"        // not yet arrived, still within grace
	StatusMissing     Status = "missing"     // grace period over, nothing arrived
	StatusUnreachable Status = "unreachable" // network / permission problem
	StatusSuspicious  Status = "suspicious"  // arrived, but looks wrong (0 bytes, too small…)
	StatusDisabled    Status = "disabled"    // switched off by the user
)

// GroupMuted reports whether the user muted a group.
func (s *Settings) GroupMuted(group string) bool {
	for _, g := range s.MutedGroups {
		if g == group {
			return true
		}
	}
	return false
}

// Severity groups statuses for the tray icon and notifications.
type Severity int

const (
	SevNone Severity = iota // nothing to monitor
	SevOK
	SevWarning
	SevError
)

// Severity of the status.
func (s Status) Severity() Severity {
	switch s {
	case StatusOK, StatusWaiting:
		return SevOK
	case StatusLate, StatusSuspicious:
		return SevWarning
	case StatusMissing, StatusUnreachable:
		return SevError
	}
	return SevNone
}

// Label is the Hungarian name of the status.
func (s Status) Label() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWaiting:
		return "Várakozik"
	case StatusLate:
		return "Késik"
	case StatusMissing:
		return "Hiányzik"
	case StatusUnreachable:
		return "Elérhetetlen"
	case StatusSuspicious:
		return "Gyanús"
	case StatusDisabled:
		return "Kikapcsolva"
	}
	return "Ismeretlen"
}

// TokenMode controls how {yyyyMMdd}-style tokens in a path are handled.
type TokenMode string

const (
	TokenDate TokenMode = ""    // replaced with the expected date (default)
	TokenAny  TokenMode = "any" // match any digits of the same length
)

// GapWindow is a daily time window in which the file may be missing – for
// example a script deletes it before writing the new version. Inside the
// window "missing" or "late" is shown as waiting, without a notification.
// The window crosses midnight when To is earlier than From.
type GapWindow struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from,omitempty"` // "HH:MM"
	To      string `json:"to,omitempty"`
}

func clockMinutes(s string) (int, bool) {
	var h, m int
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// Validate checks an enabled window.
func (g GapWindow) Validate() error {
	if !g.Enabled {
		return nil
	}
	f, ok1 := clockMinutes(g.From)
	t, ok2 := clockMinutes(g.To)
	if !ok1 || !ok2 {
		return errors.New("a megengedett hiány kezdete és vége ÓÓ:PP formátumú legyen")
	}
	if f == t {
		return errors.New("a megengedett hiány kezdete és vége nem lehet ugyanaz")
	}
	return nil
}

// Contains reports whether the wall-clock time of t (in its location) falls
// into the window [From, To).
func (g GapWindow) Contains(t time.Time) bool {
	if !g.Enabled {
		return false
	}
	f, ok1 := clockMinutes(g.From)
	to, ok2 := clockMinutes(g.To)
	if !ok1 || !ok2 || f == to {
		return false
	}
	m := t.Hour()*60 + t.Minute()
	if f < to {
		return m >= f && m < to
	}
	return m >= f || m < to
}

// Describe prints the window, e.g. "naponta 00:30–06:15".
func (g GapWindow) Describe() string {
	if !g.Enabled {
		return ""
	}
	return "naponta " + g.From + "–" + g.To
}

// SuspiciousRules flag files that arrived but look wrong.
type SuspiciousRules struct {
	ZeroBytes   bool  `json:"zeroBytes"`             // 0-byte file is suspicious
	MinBytes    int64 `json:"minBytes,omitempty"`    // smaller than this is suspicious
	DropPercent int   `json:"dropPercent,omitempty"` // smaller than median of recent sizes by this %
}

// Item is one watched output.
type Item struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Group    string        `json:"group,omitempty"`
	Owner    string        `json:"owner,omitempty"`
	Note     string        `json:"note,omitempty"`
	Path     string        `json:"path"`
	Token    TokenMode     `json:"tokenMode,omitempty"`
	Schedule schedule.Spec `json:"schedule"`

	GraceMinutes int `json:"graceMinutes"`
	// EarlyMinutes: a file modified this much before the expected time
	// still counts for it (but never before the previous expected time).
	EarlyMinutes int `json:"earlyMinutes"`
	// Gap: daily window in which the file may be missing (see GapWindow).
	Gap        GapWindow       `json:"gap"`
	Suspicious SuspiciousRules `json:"suspicious"`

	Enabled bool `json:"enabled"`
	Notify  bool `json:"notify"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`

	// Source is set at runtime for items coming from a shared team list
	// (never persisted in the personal list).
	Source string `json:"source,omitempty"`
	// Rev is set at runtime in shared mode: the revision of the report file
	// the UI is editing (optimistic concurrency).
	Rev int `json:"rev,omitempty"`
}

// IsPattern reports whether the path contains wildcards or date tokens.
func (it *Item) IsPattern() bool { return strings.ContainsAny(it.Path, "*?{") }

// Grace returns the grace period.
func (it *Item) Grace() time.Duration { return time.Duration(it.GraceMinutes) * time.Minute }

// Early returns the early tolerance.
func (it *Item) Early() time.Duration { return time.Duration(it.EarlyMinutes) * time.Minute }

// NewID returns a random identifier.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewItem returns an item with sensible defaults.
func NewItem() Item {
	now := time.Now()
	return Item{
		ID:           NewID(),
		Schedule:     schedule.Spec{Type: schedule.Workdays, Times: []string{"06:00"}, UseHolidays: true},
		GraceMinutes: 30,
		EarlyMinutes: 30,
		Suspicious:   SuspiciousRules{ZeroBytes: true, DropPercent: 70},
		Enabled:      true,
		Notify:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// QuietHours suppress toasts in a daily window (may wrap midnight).
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Weekends: quiet all day on Saturday and Sunday.
	Weekends bool `json:"weekends"`
}

// NotificationSettings configure toasts.
type NotificationSettings struct {
	Enabled    bool       `json:"enabled"`
	OnLate     bool       `json:"onLate"`     // also notify while within grace
	OnRecovery bool       `json:"onRecovery"` // notify when a problem resolves
	Quiet      QuietHours `json:"quiet"`
	// PausedUntil temporarily mutes all toasts (tray menu).
	PausedUntil time.Time `json:"pausedUntil,omitempty"`
}

// SharedList is a team-maintained JSON list on a network share.
type SharedList struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

// TeamSettings configure the shared ("közös") mode.
type TeamSettings struct {
	Enabled bool   `json:"enabled"`
	Folder  string `json:"folder"`  // UNC path of the shared folder
	PollSec int    `json:"pollSec"` // how often the folder is re-read
	// LockTTLMin: an edit lock without heartbeat for this long is orphaned.
	LockTTLMin int `json:"lockTtlMin"`
	// ToastOnChanges: Windows notification when someone else changes a report
	// (the in-app notice is always shown).
	ToastOnChanges bool `json:"toastOnChanges"`
}

// ItemOverride stores personal settings for items of shared lists.
type ItemOverride struct {
	Disabled bool `json:"disabled,omitempty"`
	Mute     bool `json:"mute,omitempty"`
}

// Settings is the whole persisted configuration (settings.json).
type Settings struct {
	SchemaVersion int `json:"schemaVersion"`

	CheckIntervalSec int    `json:"checkIntervalSec"`
	TimeoutSec       int    `json:"timeoutSec"`
	Parallelism      int    `json:"parallelism"`
	Autostart        bool   `json:"autostart"`
	CloseHintShown   bool   `json:"closeHintShown"`
	HistoryDays      int    `json:"historyDays"`
	Theme            string `json:"theme"` // "system" | "light" | "dark"

	Notifications NotificationSettings    `json:"notifications"`
	SharedLists   []SharedList            `json:"sharedLists,omitempty"`
	Overrides     map[string]ItemOverride `json:"overrides,omitempty"`
	// MutedGroups: personal – no notifications for reports of these groups.
	MutedGroups []string     `json:"mutedGroups,omitempty"`
	Team        TeamSettings `json:"team"`

	Items []Item `json:"items"`
}

// CurrentSchema is the settings schema version written by this build.
const CurrentSchema = 1

// DefaultSettings returns the factory defaults.
func DefaultSettings() Settings {
	return Settings{
		SchemaVersion:    CurrentSchema,
		CheckIntervalSec: 60,
		TimeoutSec:       10,
		Parallelism:      6,
		Autostart:        true,
		HistoryDays:      180,
		Theme:            "system",
		Notifications: NotificationSettings{
			Enabled:    true,
			OnRecovery: true,
			Quiet:      QuietHours{From: "22:00", To: "06:00"},
		},
		Items: []Item{},
	}
}

// Normalize fills zero values with defaults (for files from older versions).
func (s *Settings) Normalize() {
	d := DefaultSettings()
	if s.CheckIntervalSec < 10 {
		s.CheckIntervalSec = d.CheckIntervalSec
	}
	if s.TimeoutSec < 1 {
		s.TimeoutSec = d.TimeoutSec
	}
	if s.Parallelism < 1 {
		s.Parallelism = d.Parallelism
	}
	if s.HistoryDays < 1 {
		s.HistoryDays = d.HistoryDays
	}
	if s.Theme == "" {
		s.Theme = d.Theme
	}
	if s.Notifications.Quiet.From == "" {
		s.Notifications.Quiet.From = d.Notifications.Quiet.From
	}
	if s.Notifications.Quiet.To == "" {
		s.Notifications.Quiet.To = d.Notifications.Quiet.To
	}
	if s.Items == nil {
		s.Items = []Item{}
	}
	if s.Team.PollSec < 5 {
		s.Team.PollSec = 30
	}
	if s.Team.LockTTLMin < 1 {
		s.Team.LockTTLMin = 10
	}
	for i := range s.Items {
		if s.Items[i].ID == "" {
			s.Items[i].ID = NewID()
		}
	}
	s.SchemaVersion = CurrentSchema
}
