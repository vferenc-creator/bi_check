package store

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// History is the SQLite-backed event store (arrivals, status changes,
// notification bookkeeping).
type History struct {
	db *sql.DB
	mu sync.Mutex
}

// Arrival is a newly seen file version.
type Arrival struct {
	ItemID   string     `json:"itemId"`
	FilePath string     `json:"filePath"`
	ModTime  time.Time  `json:"modTime"`
	Size     int64      `json:"size"`
	Expected *time.Time `json:"expected,omitempty"`
	DelaySec *int64     `json:"delaySec,omitempty"`
	SeenAt   time.Time  `json:"seenAt"`
}

// Transition is a status change.
type Transition struct {
	ItemID   string     `json:"itemId"`
	At       time.Time  `json:"at"`
	From     string     `json:"from"`
	To       string     `json:"to"`
	Reason   string     `json:"reason"`
	Expected *time.Time `json:"expected,omitempty"`
}

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS arrivals(
	id INTEGER PRIMARY KEY,
	item_id TEXT NOT NULL,
	file_path TEXT NOT NULL,
	mod_time INTEGER NOT NULL,
	size INTEGER NOT NULL,
	expected INTEGER,
	delay_sec INTEGER,
	seen_at INTEGER NOT NULL,
	UNIQUE(item_id, file_path, mod_time)
);
CREATE INDEX IF NOT EXISTS arrivals_item ON arrivals(item_id, mod_time);
CREATE TABLE IF NOT EXISTS transitions(
	id INTEGER PRIMARY KEY,
	item_id TEXT NOT NULL,
	at INTEGER NOT NULL,
	from_status TEXT NOT NULL,
	to_status TEXT NOT NULL,
	reason TEXT NOT NULL,
	expected INTEGER
);
CREATE INDEX IF NOT EXISTS transitions_item ON transitions(item_id, at);
CREATE INDEX IF NOT EXISTS transitions_at ON transitions(at);
CREATE TABLE IF NOT EXISTS incidents(
	item_id TEXT PRIMARY KEY,
	key TEXT NOT NULL,
	status TEXT NOT NULL,
	at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS uptime(
	id INTEGER PRIMARY KEY,
	start INTEGER NOT NULL,
	last INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS meta(
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// OpenHistory opens (creating if needed) the history database.
func OpenHistory(path string) (*History, error) {
	dsn := "file:" + strings.ReplaceAll(path, `\`, "/")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("előzmény adatbázis: %w", err)
	}
	return &History{db: db}, nil
}

// Close closes the database.
func (h *History) Close() error { return h.db.Close() }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMs(v int64) time.Time { return time.UnixMilli(v) }

func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return ms(*t)
}

// RecordArrival stores an arrival (duplicates are ignored). Returns true if new.
func (h *History) RecordArrival(a Arrival) (bool, error) {
	var delay any
	if a.DelaySec != nil {
		delay = *a.DelaySec
	}
	res, err := h.db.Exec(`INSERT OR IGNORE INTO arrivals(item_id,file_path,mod_time,size,expected,delay_sec,seen_at) VALUES(?,?,?,?,?,?,?)`,
		a.ItemID, a.FilePath, ms(a.ModTime), a.Size, nullTime(a.Expected), delay, ms(a.SeenAt))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// RecentSizes returns the sizes of the last n arrivals (newest first).
func (h *History) RecentSizes(itemID string, n int) []int64 {
	rows, err := h.db.Query(`SELECT size FROM arrivals WHERE item_id=? ORDER BY mod_time DESC LIMIT ?`, itemID, n)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var s int64
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// Arrivals lists arrivals of an item since t (newest first).
func (h *History) Arrivals(itemID string, since time.Time, limit int) ([]Arrival, error) {
	rows, err := h.db.Query(`SELECT item_id,file_path,mod_time,size,expected,delay_sec,seen_at FROM arrivals
		WHERE item_id=? AND mod_time>=? ORDER BY mod_time DESC LIMIT ?`, itemID, ms(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Arrival
	for rows.Next() {
		var a Arrival
		var mod, seen int64
		var exp, delay sql.NullInt64
		if err := rows.Scan(&a.ItemID, &a.FilePath, &mod, &a.Size, &exp, &delay, &seen); err != nil {
			return nil, err
		}
		a.ModTime, a.SeenAt = fromMs(mod), fromMs(seen)
		if exp.Valid {
			t := fromMs(exp.Int64)
			a.Expected = &t
		}
		if delay.Valid {
			d := delay.Int64
			a.DelaySec = &d
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordTransition stores a status change.
func (h *History) RecordTransition(t Transition) error {
	_, err := h.db.Exec(`INSERT INTO transitions(item_id,at,from_status,to_status,reason,expected) VALUES(?,?,?,?,?,?)`,
		t.ItemID, ms(t.At), t.From, t.To, t.Reason, nullTime(t.Expected))
	return err
}

// Transitions lists status changes (itemID "" = all items), newest first.
func (h *History) Transitions(itemID string, since time.Time, limit int) ([]Transition, error) {
	q := `SELECT item_id,at,from_status,to_status,reason,expected FROM transitions WHERE at>=?`
	args := []any{ms(since)}
	if itemID != "" {
		q += ` AND item_id=?`
		args = append(args, itemID)
	}
	q += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Transition
	for rows.Next() {
		var t Transition
		var at int64
		var exp sql.NullInt64
		if err := rows.Scan(&t.ItemID, &at, &t.From, &t.To, &t.Reason, &exp); err != nil {
			return nil, err
		}
		t.At = fromMs(at)
		if exp.Valid {
			x := fromMs(exp.Int64)
			t.Expected = &x
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Incident is the last problem we notified about for an item.
type Incident struct {
	Key    string
	Status string
	At     time.Time
}

// Incidents loads all open incidents.
func (h *History) Incidents() map[string]Incident {
	out := map[string]Incident{}
	rows, err := h.db.Query(`SELECT item_id,key,status,at FROM incidents`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var in Incident
		var at int64
		if rows.Scan(&id, &in.Key, &in.Status, &at) == nil {
			in.At = fromMs(at)
			out[id] = in
		}
	}
	return out
}

// SetIncident stores (or with key "" deletes) an item's open incident.
func (h *History) SetIncident(itemID string, in *Incident) error {
	if in == nil {
		_, err := h.db.Exec(`DELETE FROM incidents WHERE item_id=?`, itemID)
		return err
	}
	_, err := h.db.Exec(`INSERT INTO incidents(item_id,key,status,at) VALUES(?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET key=excluded.key, status=excluded.status, at=excluded.at`, itemID, in.Key, in.Status, ms(in.At))
	return err
}

// Meta reads a key/value.
func (h *History) Meta(key string) string {
	var v string
	_ = h.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	return v
}

// SetMeta writes a key/value.
func (h *History) SetMeta(key, value string) error {
	_, err := h.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// Prune deletes events older than the retention period and of deleted items.
func (h *History) Prune(olderThan time.Time, liveItems []string) error {
	if _, err := h.db.Exec(`DELETE FROM arrivals WHERE mod_time<?`, ms(olderThan)); err != nil {
		return err
	}
	if _, err := h.db.Exec(`DELETE FROM transitions WHERE at<?`, ms(olderThan)); err != nil {
		return err
	}
	if _, err := h.db.Exec(`DELETE FROM uptime WHERE last<?`, ms(olderThan)); err != nil {
		return err
	}
	if liveItems != nil {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(liveItems)), ",")
		args := make([]any, len(liveItems))
		for i, s := range liveItems {
			args[i] = s
		}
		if len(liveItems) == 0 {
			_, err := h.db.Exec(`DELETE FROM incidents`)
			return err
		}
		if _, err := h.db.Exec(`DELETE FROM incidents WHERE item_id NOT IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	return nil
}

// Uptime tracks when the monitor was running, so statistics do not count
// expectations as "missed" while the PC was switched off.
type Uptime struct {
	h  *History
	id int64
}

// StartUptime opens a new running period.
func (h *History) StartUptime(now time.Time) *Uptime {
	res, err := h.db.Exec(`INSERT INTO uptime(start,last) VALUES(?,?)`, ms(now), ms(now))
	if err != nil {
		return &Uptime{h: h}
	}
	id, _ := res.LastInsertId()
	return &Uptime{h: h, id: id}
}

// Beat extends the current running period.
func (u *Uptime) Beat(now time.Time) {
	if u == nil || u.id == 0 {
		return
	}
	_, _ = u.h.db.Exec(`UPDATE uptime SET last=? WHERE id=?`, ms(now), u.id)
}

// Period is a time range when the monitor ran.
type Period struct{ Start, End time.Time }

// UptimeSince returns running periods overlapping [since, now].
func (h *History) UptimeSince(since time.Time) []Period {
	rows, err := h.db.Query(`SELECT start,last FROM uptime WHERE last>=? ORDER BY start`, ms(since))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Period
	for rows.Next() {
		var a, b int64
		if rows.Scan(&a, &b) == nil {
			out = append(out, Period{fromMs(a), fromMs(b)})
		}
	}
	return out
}
