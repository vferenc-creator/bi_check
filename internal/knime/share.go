package knime

// Sharing the state of the KNIME runs through the shared folder of the
// shared mode, so the other computers see what the server ran (read only).
//
// Every computer that has KNIME jobs writes one file into its own
// sub-folder of the shared folder:
//
//	<shared folder>\knime-futtatasok\<computer>__<user>.json
//
// Nothing else of the shared folder is read or written. The file holds no
// passwords, variables, credentials or logs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ShareFolder is the dedicated sub-folder of the shared folder.
const ShareFolder = "knime-futtatasok"

// ShareVersion is the format of the shared files.
const ShareVersion = 1

// ShareInterval: the state is written at least this often (heartbeat).
const ShareInterval = time.Minute

// Shared is what one computer publishes.
type Shared struct {
	Version     int         `json:"version"`
	Host        string      `json:"host"`
	User        string      `json:"user"`
	Display     string      `json:"display,omitempty"` // "Kiss Anna"
	App         string      `json:"app"`               // BI Monitor version
	Updated     time.Time   `json:"updated"`
	IntervalSec int         `json:"intervalSec"`
	KNIME       string      `json:"knime"`   // KNIME version
	NodePit     string      `json:"nodepit"` // NodePit Batch version
	Jobs        []SharedJob `json:"jobs"`
	Runs        []SharedRun `json:"runs"` // newest first
}

// SharedJob is a job without its parameters.
type SharedJob struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Workflow  string     `json:"workflow"`
	Schedule  string     `json:"schedule"`
	Scheduled bool       `json:"scheduled"`
	Enabled   bool       `json:"enabled"`
	Next      *time.Time `json:"next,omitempty"`
	Retry     *Retry     `json:"retry,omitempty"`
	Live      *SharedRun `json:"live,omitempty"`
	Last      *SharedRun `json:"last,omitempty"`
}

// SharedRun is a run without its log.
type SharedRun struct {
	ID         string      `json:"id"`
	JobID      string      `json:"jobId"`
	JobName    string      `json:"jobName"`
	Trigger    string      `json:"trigger"`
	Status     string      `json:"status"`
	Queued     time.Time   `json:"queued"`
	Started    time.Time   `json:"started,omitempty"`
	Ended      time.Time   `json:"ended,omitempty"`
	DurationMs int64       `json:"durationMs"`
	ExitCode   *int        `json:"exitCode,omitempty"`
	Message    string      `json:"message"`
	Attempt    int         `json:"attempt,omitempty"`
	Attempts   int         `json:"attempts,omitempty"`
	NextRetry  time.Time   `json:"nextRetry,omitempty"`
	FailedNode *FailedNode `json:"failedNode,omitempty"`
}

// ShareRun strips a run for sharing (message: at most 3 lines, 600 chars).
func ShareRun(r Run) SharedRun {
	msg := r.Message
	if lines := strings.Split(msg, "\n"); len(lines) > 3 {
		msg = strings.Join(lines[:3], "\n")
	}
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	return SharedRun{ID: r.ID, JobID: r.JobID, JobName: r.JobName, Trigger: r.Trigger, Status: r.Status, Queued: r.Queued,
		Started: r.Started, Ended: r.Ended, DurationMs: r.DurationMs, ExitCode: r.ExitCode, Message: msg,
		Attempt: r.Attempt, Attempts: r.Attempts, NextRetry: r.NextRetry, FailedNode: r.FailedNode}
}

// ShareFileName is the file of a computer and user: "EF-BI__d.admin.json".
func ShareFileName(host, user string) string {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		if i := strings.LastIndexAny(s, `\/`); i >= 0 { // DOMAIN\user → user
			s = s[i+1:]
		}
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		if b.Len() == 0 {
			return "ismeretlen"
		}
		return b.String()
	}
	return clean(host) + "__" + clean(user) + ".json"
}

// Stale reports whether the publisher stopped writing (missed several
// heartbeats): its BI Monitor is not running or cannot reach the folder.
func (s Shared) Stale(now time.Time) bool {
	limit := 3 * time.Duration(s.IntervalSec) * time.Second
	if limit < 10*time.Minute {
		limit = 10 * time.Minute
	}
	return now.Sub(s.Updated) > limit
}

// WriteShared writes the state of this computer (atomically: a reader
// never sees half a file).
func WriteShared(root, name string, s Shared) error {
	dir := filepath.Join(root, ShareFolder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	var rerr error
	for i := 0; i < 5; i++ { // a reader may hold the old file for a moment (Windows)
		if rerr = os.Rename(tmp, filepath.Join(dir, name)); rerr == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
	}
	os.Remove(tmp)
	return rerr
}

// RemoveShared deletes the state file of this computer (it has no jobs or
// stopped sharing: the others must not wait for its heartbeat).
func RemoveShared(root, name string) error {
	err := os.Remove(filepath.Join(root, ShareFolder, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Remote is another computer's state as read from the shared folder.
type Remote struct {
	File  string `json:"file"`
	Data  Shared `json:"data"`
	Error string `json:"error,omitempty"`
}

// ReadShared reads the other computers' states (all but exclude), sorted
// by computer name. A missing folder is no error: nobody shares yet.
func ReadShared(root, exclude string) ([]Remote, error) {
	dir := filepath.Join(root, ShareFolder)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Remote{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Remote{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(n), ".json") || strings.HasPrefix(n, ".") || strings.EqualFold(n, exclude) {
			continue
		}
		r := Remote{File: n}
		b, err := os.ReadFile(filepath.Join(dir, n))
		switch {
		case err != nil:
			r.Error = "nem olvasható: " + err.Error()
		case json.Unmarshal(b, &r.Data) != nil:
			r.Error = "sérült fájl"
		case r.Data.Version > ShareVersion:
			r.Error = fmt.Sprintf("újabb BI Monitor írta (%s) – frissítse ezt a programot", r.Data.App)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].File) < strings.ToLower(out[j].File) })
	return out, nil
}
