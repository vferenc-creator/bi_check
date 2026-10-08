// Package teamstore keeps the shared ("közös") report list on an SMB share
// so several BI Monitor instances can edit it together.
//
// Layout of the shared folder:
//
//	bicheck.json            marker + schema version of the folder
//	reports\<id>.json       one file per report (no two people ever write the same file
//	                        unless they edit the same report – and that needs the lock)
//	locks\<id>.lock         edit lock, created with CREATE_NEW semantics
//
// Rules that make it safe on a network share (no database files!):
//   - every write goes to a temp file first and is then renamed over the
//     target, so readers never see half-written JSON;
//   - a lock is acquired by creating the lock file exclusively (O_EXCL) –
//     the file server decides, so only one instance can win;
//   - the lock's heartbeat is the lock file's modification time; a lock whose
//     heartbeat is older than the TTL counts as orphaned and may be taken
//     over (rename-away first, which again only one instance can do);
//   - saving re-checks both the lock owner and the revision (optimistic
//     concurrency), so nothing is overwritten silently;
//   - a folder or report written by a newer schema is read-only for us.
package teamstore

import (
	"fmt"
	"time"

	"bimonitor/internal/model"
)

// SchemaVersion is the shared-folder format written by this build.
const SchemaVersion = 1

// FolderFormat identifies our shared folders.
const FolderFormat = "energofish-bicheck-shared"

// Identity of a running instance.
type Identity struct {
	User     string `json:"user"`     // DOMAIN\user
	Display  string `json:"display"`  // "Kiss Anna"
	Host     string `json:"host"`     // computer name
	Instance string `json:"instance"` // random per process
}

// Name is the human readable name.
func (i Identity) Name() string {
	if i.Display != "" {
		return i.Display
	}
	return i.User
}

// Stamp records who did something and when.
type Stamp struct {
	User    string    `json:"user"`
	Display string    `json:"display,omitempty"`
	Host    string    `json:"host"`
	At      time.Time `json:"at"`
}

// Name of the person.
func (s Stamp) Name() string {
	if s.Display != "" {
		return s.Display
	}
	return s.User
}

// Change is one entry of a report's change log.
type Change struct {
	Rev    int      `json:"rev"`
	Stamp  Stamp    `json:"stamp"`
	Action string   `json:"action"` // created | modified | deleted | restored | unlocked | imported
	Fields []string `json:"fields,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// MaxChanges is the length of the per-report change log.
const MaxChanges = 100

// Report is the content of reports\<id>.json.
type Report struct {
	Schema     int        `json:"schema"`
	ID         string     `json:"id"`
	Revision   int        `json:"revision"`
	Created    Stamp      `json:"created"`
	Modified   Stamp      `json:"modified"`
	Deleted    *Stamp     `json:"deleted,omitempty"`
	Definition model.Item `json:"definition"`
	Changes    []Change   `json:"changes,omitempty"`
}

// Manifest is bicheck.json.
type Manifest struct {
	Format string `json:"format"`
	Schema int    `json:"schema"`
	// MinWriterSchema: builds with an older SchemaVersion must not write.
	MinWriterSchema int   `json:"minWriterSchema"`
	Created         Stamp `json:"created"`
}

// Lock is the content of locks\<id>.lock.
type Lock struct {
	ID    string    `json:"id"`
	Owner Identity  `json:"owner"`
	Since time.Time `json:"since"`
	// Heartbeat is the lock file's modification time (filled in when read).
	Heartbeat time.Time `json:"-"`
	// Expired: the heartbeat is older than the TTL.
	Expired bool `json:"-"`
	// Mine: held by this instance.
	Mine bool `json:"-"`
}

// Holder describes who holds a lock, e.g. "Kiss Anna (EF-PC12), 10:42 óta".
func (l Lock) Holder() string {
	n := l.Owner.Display
	if n == "" {
		n = l.Owner.User
	}
	if n == "" {
		n = "ismeretlen"
	}
	return fmt.Sprintf("%s (%s), %s óta", n, l.Owner.Host, l.Since.Local().Format("15:04"))
}

// ---- Errors ------------------------------------------------------------------

// LockedError: somebody else holds the lock.
type LockedError struct{ Lock Lock }

func (e *LockedError) Error() string { return "A riportot éppen szerkeszti: " + e.Lock.Holder() }

// ConflictError: the report changed since it was opened.
type ConflictError struct {
	Current Report
	Base    int
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("A riportot közben módosította %s (%s) – a %d. verzió helyett már a %d. van a közös mappában.",
		e.Current.Modified.Name(), e.Current.Modified.At.Local().Format("15:04"), e.Base, e.Current.Revision)
}

// ErrNotLocked: saving requires holding the lock.
var ErrNotLocked = fmt.Errorf("a mentéshez a riport zárolása szükséges (a zárat időközben elvesztette vagy feloldották)")

// ErrOffline: the shared folder is not reachable.
var ErrOffline = fmt.Errorf("a közös mappa jelenleg nem érhető el – offline módban a szerkesztés nem lehetséges")

// ReadOnlyError: the folder/report uses a newer format.
type ReadOnlyError struct{ Reason string }

func (e *ReadOnlyError) Error() string { return e.Reason }
