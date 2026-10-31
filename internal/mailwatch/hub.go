// Package mailwatch keeps what the Thunderbird add-on reports about e-mails
// and turns it into check results for e-mail items. The add-on does the
// searching inside Thunderbird (it already has the mailbox and the login);
// the application only receives the matching messages.
package mailwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/checker"
	"bimonitor/internal/model"
	"bimonitor/internal/pathpattern"
)

// ServerKey groups e-mail items in notifications ("Thunderbird nem érhető el").
const ServerKey = "Thunderbird"

// LookbackDays: how far back the add-on searches.
const LookbackDays = 40

// Msg is one message the add-on found for a rule.
type Msg struct {
	ID       int64     `json:"id"`                 // Thunderbird message id (valid while TB runs)
	HeaderID string    `json:"headerId,omitempty"` // Message-ID header
	Subject  string    `json:"subject"`
	Date     time.Time `json:"date"`
	Folder   string    `json:"folder"`
	Account  string    `json:"account,omitempty"`
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
}

// Rule is sent to the add-on: which messages to look for.
type Rule struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Regex  string `json:"regex"` // JavaScript regex, use with the "i" flag
	Folder string `json:"folder,omitempty"`
	Party  string `json:"party,omitempty"`
}

// Report is what the add-on posts after each scan.
type Report struct {
	ExtVersion string           `json:"extVersion"`
	TBVersion  string           `json:"tbVersion"`
	Folders    []string         `json:"folders"`
	Matches    map[string][]Msg `json:"matches"`
	Errors     []string         `json:"errors"`
}

// Command is queued for the add-on (picked up by its long poll).
type Command struct {
	Type      string `json:"type"` // "scan" | "open"
	MessageID int64  `json:"messageId,omitempty"`
	HeaderID  string `json:"headerId,omitempty"`
}

// Info describes the connection (for the settings page).
type Info struct {
	LastReport  time.Time `json:"lastReport"`
	LastContact time.Time `json:"lastContact"`
	ExtVersion  string    `json:"extVersion"`
	TBVersion   string    `json:"tbVersion"`
	Folders     []string  `json:"folders"`
	Errors      []string  `json:"errors"`
}

// Hub stores the latest report. Safe for concurrent use.
type Hub struct {
	mu      sync.Mutex
	started time.Time
	info    Info
	msgs    map[string][]Msg
	cmds    []Command
	wake    chan struct{}
	now     func() time.Time

	// recent: the newest messages of the watched folders (for picking an
	// e-mail in the editor), requested on demand.
	recent     []Msg
	recentAt   time.Time
	recentWake chan struct{}
}

// RecentDays: how far back the e-mail picker lists messages.
const RecentDays = 45

// RecentLimit caps the picker list.
const RecentLimit = 500

// SetRecent stores the picker list sent by the add-on.
func (h *Hub) SetRecent(list []Msg) {
	l := append([]Msg(nil), list...)
	sort.Slice(l, func(i, j int) bool { return l[i].Date.After(l[j].Date) })
	if len(l) > RecentLimit {
		l = l[:RecentLimit]
	}
	h.mu.Lock()
	h.recent, h.recentAt = l, h.now()
	h.info.LastContact = h.recentAt
	w := h.recentWake
	h.recentWake = nil
	h.mu.Unlock()
	if w != nil {
		close(w)
	}
}

// Recent asks the add-on for a fresh list and waits up to wait for it.
// fresh=false: the add-on did not answer in time, the last known list is
// returned (at = when it was received; zero if never).
func (h *Hub) Recent(ctx context.Context, wait time.Duration) (list []Msg, at time.Time, fresh bool) {
	h.mu.Lock()
	if h.recentWake == nil {
		h.recentWake = make(chan struct{})
	}
	w := h.recentWake
	h.mu.Unlock()
	h.Push(Command{Type: "recent"})
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-w:
		fresh = true
	case <-t.C:
	case <-ctx.Done():
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Msg(nil), h.recent...), h.recentAt, fresh
}

// New creates a hub (now may be nil).
func New(now func() time.Time) *Hub {
	if now == nil {
		now = time.Now
	}
	return &Hub{started: now(), msgs: map[string][]Msg{}, wake: make(chan struct{}, 1), now: now}
}

// Contact records that the add-on talked to us (any authenticated call).
func (h *Hub) Contact() {
	h.mu.Lock()
	h.info.LastContact = h.now()
	h.mu.Unlock()
}

// Update stores a report.
func (h *Hub) Update(r Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.info.LastReport, h.info.LastContact = now, now
	h.info.ExtVersion, h.info.TBVersion = r.ExtVersion, r.TBVersion
	h.info.Folders, h.info.Errors = r.Folders, r.Errors
	h.msgs = map[string][]Msg{}
	for id, list := range r.Matches {
		l := append([]Msg(nil), list...)
		sort.Slice(l, func(i, j int) bool { return l[i].Date.After(l[j].Date) })
		h.msgs[id] = l
	}
}

// Info returns the connection state.
func (h *Hub) Info() Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info
}

// Push queues a command for the add-on.
func (h *Hub) Push(c Command) {
	h.mu.Lock()
	for _, x := range h.cmds {
		if x == c {
			h.mu.Unlock()
			return
		}
	}
	h.cmds = append(h.cmds, c)
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Poll waits up to wait for commands (long poll).
func (h *Hub) Poll(ctx context.Context, wait time.Duration) []Command {
	h.Contact()
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		h.mu.Lock()
		if len(h.cmds) > 0 {
			c := h.cmds
			h.cmds = nil
			h.mu.Unlock()
			return c
		}
		h.mu.Unlock()
		select {
		case <-h.wake:
		case <-t.C:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// Rules builds the add-on's search list from the enabled e-mail items.
func Rules(items []model.Item) []Rule {
	var out []Rule
	for _, it := range items {
		if !it.IsEmail() || !it.Enabled {
			continue
		}
		x, err := pathpattern.ParseText(it.Mail.Subject)
		if err != nil || strings.TrimSpace(it.Mail.Subject) == "" {
			continue
		}
		out = append(out, Rule{ID: it.ID, Name: it.Name, Regex: x.LooseRegex(), Folder: it.Mail.Folder, Party: it.Mail.Party})
	}
	return out
}

// Check evaluates an e-mail item at reference time ref. enabled: the
// Thunderbird connection is switched on on this computer; stale: after this
// long without a report the item is unreachable.
func (h *Hub) Check(it model.Item, ref time.Time, anyDate, enabled bool, stale time.Duration) checker.Result {
	res := checker.Result{Server: ServerKey, Resolved: "✉ " + it.Mail.Subject, CheckedAt: h.now()}
	if !enabled {
		res.Kind = checker.NoData
		res.Reason = "Ezen a gépen nincs bekapcsolva a Thunderbird-kapcsolat (Beállítások → Thunderbird). Az e-maileket az a gép figyeli, ahol a Thunderbird és a BI Monitor bővítmény fut."
		return res
	}
	x, err := pathpattern.ParseText(it.Mail.Subject)
	if err != nil {
		res.Kind, res.Reason = checker.Unreachable, "Hibás tárgyminta: "+err.Error()
		return res
	}
	h.mu.Lock()
	info := h.info
	msgs := h.msgs[it.ID]
	started := h.started
	h.mu.Unlock()
	now := h.now()
	if info.LastReport.IsZero() {
		if now.Sub(started) < 3*time.Minute {
			res.Kind = checker.NoData
			res.Reason = "Várakozás a Thunderbird-bővítmény első jelentésére…"
			return res
		}
		res.Kind, res.ServerProblem = checker.Unreachable, true
		res.Reason = "A Thunderbird-bővítmény még nem jelentkezett. Fut a Thunderbird, telepítve van a BI Monitor bővítmény, és jó a párosító kulcs?"
		return res
	}
	if age := now.Sub(info.LastReport); age > stale {
		res.Kind, res.ServerProblem = checker.Unreachable, true
		res.Reason = fmt.Sprintf("A Thunderbird-bővítmény %d perce nem jelentkezett – fut a Thunderbird?", int(age.Minutes()))
		return res
	}
	match := x.Matcher(ref, anyDate)
	folder := strings.ToLower(strings.TrimSpace(it.Mail.Folder))
	party := strings.ToLower(strings.TrimSpace(it.Mail.Party))
	// Shared mode: every computer with Thunderbird checks the e-mail items.
	// A Thunderbird without the sending account must not report "missing".
	if len(info.Folders) == 0 {
		res.Kind = checker.NoData
		res.Reason = "Ezen a gépen a Thunderbird-bővítmény nem figyel egyetlen mappát sem (bővítmény beállításai → Figyelt mappák)."
		return res
	}
	if folder != "" && !anyContains(info.Folders, folder) {
		res.Kind = checker.NoData
		res.Reason = fmt.Sprintf("Ezen a gépen a Thunderbird nem figyel „%s” mappát – itt nem ellenőrizhető (az e-mailt az a gép figyeli, ahol ez a fiók megvan). Figyelt mappák: %s.", it.Mail.Folder, strings.Join(info.Folders, ", "))
		return res
	}
	var found []checker.FileInfo
	for _, m := range msgs {
		if !match(m.Subject) {
			continue
		}
		if folder != "" && !strings.Contains(strings.ToLower(m.Account+" / "+m.Folder), folder) {
			continue
		}
		if party != "" && !strings.Contains(strings.ToLower(m.From+" "+m.To), party) {
			continue
		}
		found = append(found, checker.FileInfo{Path: mailPath(m), Name: m.Subject, ModTime: m.Date})
	}
	res.Matches = len(found)
	if len(found) == 0 {
		res.Kind = checker.NotFound
		res.Reason = fmt.Sprintf("Nincs ilyen tárgyú e-mail a figyelt mappákban (az elmúlt %d napban): „%s”.", LookbackDays, it.Mail.Subject)
		return res
	}
	newest := found[0]
	res.Kind, res.File = checker.Found, &newest
	if len(found) > 5 {
		found = found[:5]
	}
	res.Candidates = found
	return res
}

// Find returns the reported message of an item with this subject, date and
// "account / folder" (as in the check result), to open it in Thunderbird.
func (h *Hub) Find(id, subject string, date time.Time, path string) (Msg, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs[id] {
		if m.Subject == subject && m.Date.Equal(date) && (path == "" || mailPath(m) == path) {
			return m, true
		}
	}
	return Msg{}, false
}

func anyContains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(strings.ToLower(s), sub) {
			return true
		}
	}
	return false
}

func mailPath(m Msg) string {
	if m.Account != "" {
		return m.Account + " / " + m.Folder
	}
	return m.Folder
}
