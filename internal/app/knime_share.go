package app

// The state of the KNIME runs in the shared folder: the computer that runs
// KNIME jobs (the BI server) writes its state every minute and on every
// change into <shared folder>\knime-futtatasok\<computer>__<user>.json; the
// other computers show it read-only on their KNIME page and notify when a
// run there finally failed or the server stopped reporting. Nothing else
// of the shared folder is touched.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/knime"
)

type knimeShareState struct {
	mu        sync.Mutex
	remote    []knime.Remote
	published time.Time // last successful write of our file
	ownFile   bool      // our file exists in the shared folder
	pubErr    string
	readErr   string
	seen      map[string]map[string]bool // remote file → run IDs already known
	stale     map[string]bool            // remote file → "not reporting" notified
	kick      chan struct{}
}

// knimeShareView is the sharing part of the KNIME page.
type knimeShareView struct {
	Team      bool       `json:"team"`      // shared mode is on (a shared folder exists)
	Folder    string     `json:"folder"`    // the dedicated sub-folder
	File      string     `json:"file"`      // our file name
	Sharing   bool       `json:"sharing"`   // we publish (jobs exist and not switched off)
	Published *time.Time `json:"published"` // last successful write
	Error     string     `json:"error,omitempty"`
}

type knimeRemoteView struct {
	File    string            `json:"file"`
	Host    string            `json:"host"`
	User    string            `json:"user"`
	App     string            `json:"app"`
	Updated time.Time         `json:"updated"`
	Stale   bool              `json:"stale"`
	KNIME   string            `json:"knime"`
	NodePit string            `json:"nodepit"`
	Jobs    []knime.SharedJob `json:"jobs"`
	Runs    []knime.SharedRun `json:"runs"`
	Error   string            `json:"error,omitempty"`
}

func (a *App) knimeShareFile() string {
	id := a.opt.Identity
	return knime.ShareFileName(id.Host, id.User)
}

// knimeShareKick asks for a write soon (a run changed).
func (a *App) knimeShareKick() {
	k := &a.kn.share
	k.mu.Lock()
	ch := k.kick
	k.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// knimeShareLoop writes and reads the shared state every minute and a few
// seconds after every change of a run.
func (a *App) knimeShareLoop(ctx context.Context) {
	k := &a.kn.share
	k.mu.Lock()
	k.kick = make(chan struct{}, 1)
	k.mu.Unlock()
	t := time.NewTicker(knime.ShareInterval)
	defer t.Stop()
	a.knimeShareStep()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.knimeShareStep()
		case <-k.kick:
			select { // gather the changes of a few seconds into one write
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			a.knimeShareStep()
		}
	}
}

// knimeSnapshot is what this computer publishes.
func (a *App) knimeSnapshot(st *knime.Store, r *knime.Runner) knime.Shared {
	id := a.opt.Identity
	cfg := st.Config()
	sh := knime.Shared{Version: knime.ShareVersion, Host: id.Host, User: id.User, Display: id.Display, App: Version,
		Updated: time.Now(), IntervalSec: int(knime.ShareInterval / time.Second), Jobs: []knime.SharedJob{}, Runs: []knime.SharedRun{}}
	if cfg.Exe != "" {
		in := knime.Inspect(cfg.Exe)
		sh.KNIME, sh.NodePit = in.Version, in.NodePit
	}
	live := map[string]knime.Run{}
	for _, x := range r.Live() {
		live[x.JobID] = x
	}
	for _, j := range st.Jobs() {
		sj := knime.SharedJob{ID: j.ID, Name: j.Name, Workflow: j.Workflow, Schedule: "csak kézi indítás", Scheduled: j.Scheduled, Enabled: j.Enabled}
		if j.Scheduled {
			sj.Schedule = j.Schedule.Describe()
		}
		if t := r.Next(j); !t.IsZero() {
			sj.Next = &t
		}
		if rt, ok := st.PendingRetry(j.ID); ok {
			sj.Retry = &rt
		}
		if x, ok := live[j.ID]; ok {
			s := knime.ShareRun(x)
			sj.Live = &s
		}
		if x, ok := st.LastRun(j.ID); ok {
			s := knime.ShareRun(x)
			sj.Last = &s
		}
		sh.Jobs = append(sh.Jobs, sj)
	}
	for _, x := range st.Runs("", 30) {
		sh.Runs = append(sh.Runs, knime.ShareRun(x))
	}
	return sh
}

// knimeShareStep publishes our state (if we have jobs) and reads the others.
func (a *App) knimeShareStep() {
	k := &a.kn.share
	s := a.Settings.Get()
	folder := strings.TrimSpace(s.Team.Folder)
	if !s.Team.Enabled || folder == "" {
		k.mu.Lock()
		changed := len(k.remote) > 0
		k.remote, k.pubErr, k.readErr, k.ownFile = nil, "", "", false
		k.mu.Unlock()
		if changed {
			a.P.Push("knime", nil)
		}
		return
	}
	st, r, err := a.knimeInit()
	if err != nil {
		return
	}
	cfg := st.Config()
	name := a.knimeShareFile()
	timeout := time.Duration(s.TimeoutSec+10) * time.Second

	// our state
	var pubErr string
	jobs := st.Jobs()
	k.mu.Lock()
	own := k.ownFile
	k.mu.Unlock()
	switch {
	case !cfg.NoShare && len(jobs) > 0:
		sh := a.knimeSnapshot(st, r)
		if err := shareTimeout(timeout, func() error { return knime.WriteShared(folder, name, sh) }); err != nil {
			pubErr = "Az állapot nem írható a közös mappába: " + err.Error()
		} else {
			k.mu.Lock()
			k.published, k.ownFile = time.Now(), true
			k.mu.Unlock()
		}
	case own || cfg.NoShare || len(jobs) == 0:
		// nothing to share (any more): the others must not wait for us
		if err := shareTimeout(timeout, func() error { return knime.RemoveShared(folder, name) }); err == nil {
			k.mu.Lock()
			k.ownFile = false
			k.mu.Unlock()
		}
	}

	// the others
	var remote []knime.Remote
	var readErr string
	if err := shareTimeout(timeout, func() error {
		var e error
		remote, e = knime.ReadShared(folder, name)
		return e
	}); err != nil {
		readErr = "A közös mappa nem olvasható: " + err.Error()
	}
	k.mu.Lock()
	k.pubErr, k.readErr = pubErr, readErr
	if readErr == "" {
		k.remote = remote
	}
	k.mu.Unlock()
	if readErr == "" {
		a.knimeRemoteNotify(remote, cfg.MuteRemote)
	}
	a.P.Push("knime", nil)
}

// knimeRemoteNotify notifies the final failures of the other computers'
// runs and a computer that stopped reporting (each only once).
func (a *App) knimeRemoteNotify(remote []knime.Remote, mute bool) {
	k := &a.kn.share
	now := time.Now()
	type note struct {
		title, text string
		kind        NotifyKind
	}
	var notes []note
	k.mu.Lock()
	if k.seen == nil {
		k.seen, k.stale = map[string]map[string]bool{}, map[string]bool{}
	}
	for _, rm := range remote {
		if rm.Error != "" {
			continue
		}
		d := rm.Data
		who := d.Host
		if who == "" {
			who = rm.File
		}
		switch stale := d.Stale(now); {
		case stale && !k.stale[rm.File]:
			k.stale[rm.File] = true
			notes = append(notes, note{"KNIME: " + who + " nem jelentkezik",
				fmt.Sprintf("A(z) %s gép BI Monitorja %s óta nem írt a közös mappába – nem fut, vagy nem éri el a mappát. Az ütemezett KNIME-futtatások ott most nem biztos, hogy elindulnak.", who, d.Updated.Local().Format("01.02. 15:04")), NotifyWarning})
		case !stale && k.stale[rm.File]:
			delete(k.stale, rm.File)
			notes = append(notes, note{"KNIME: " + who + " ismét jelentkezik", "A BI Monitor újra fut azon a gépen.", NotifyInfo})
		}
		seen, known := k.seen[rm.File]
		if !known {
			seen = map[string]bool{}
			k.seen[rm.File] = seen
		}
		for _, run := range d.Runs {
			if seen[run.ID] {
				continue
			}
			final := run.NextRetry.IsZero() && (run.Status == knime.StatusError || run.Status == knime.StatusTimeout || run.Status == knime.StatusMissed)
			if !final && run.Status != knime.StatusOK && run.Status != knime.StatusCanceled && run.Status != knime.StatusSkipped {
				continue // still going (or a retry follows): look again later
			}
			seen[run.ID] = true
			if known && final { // not for the history found at the first look
				msg := run.Message
				if i := strings.IndexByte(msg, '\n'); i >= 0 {
					msg = msg[:i]
				}
				notes = append(notes, note{"KNIME (" + who + "): " + run.JobName, msg, NotifyError})
			}
		}
	}
	k.mu.Unlock()
	for _, n := range notes {
		log.Printf("knime közös: %s – %s", n.title, n.text)
		if !mute {
			a.P.Notify(n.title, n.text, n.kind)
		}
	}
}

// knimeShareView is the sharing state for the KNIME page.
func (a *App) knimeShareViews() (knimeShareView, []knimeRemoteView) {
	s := a.Settings.Get()
	k := &a.kn.share
	v := knimeShareView{Team: s.Team.Enabled && strings.TrimSpace(s.Team.Folder) != "", File: a.knimeShareFile()}
	if v.Team {
		v.Folder = strings.TrimRight(s.Team.Folder, `\/`) + `\` + knime.ShareFolder
	}
	now := time.Now()
	k.mu.Lock()
	defer k.mu.Unlock()
	v.Sharing = k.ownFile
	if !k.published.IsZero() {
		t := k.published
		v.Published = &t
	}
	v.Error = k.pubErr
	if k.readErr != "" {
		v.Error = strings.TrimSpace(v.Error + " " + k.readErr)
	}
	out := []knimeRemoteView{}
	if !v.Team {
		return v, out
	}
	for _, rm := range k.remote {
		d := rm.Data
		rv := knimeRemoteView{File: rm.File, Host: d.Host, User: d.Display, App: d.App, Updated: d.Updated, Stale: rm.Error == "" && d.Stale(now),
			KNIME: d.KNIME, NodePit: d.NodePit, Jobs: d.Jobs, Runs: d.Runs, Error: rm.Error}
		if rv.User == "" {
			rv.User = d.User
		}
		if rv.Jobs == nil {
			rv.Jobs = []knime.SharedJob{}
		}
		if len(rv.Runs) > 15 {
			rv.Runs = rv.Runs[:15]
		}
		if rv.Runs == nil {
			rv.Runs = []knime.SharedRun{}
		}
		out = append(out, rv)
	}
	return v, out
}

var errShareTimeout = errors.New("időtúllépés (a közös mappa nem válaszol)")

// shareTimeout: a network share that hangs must not block the program.
func shareTimeout(d time.Duration, op func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- op() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return errShareTimeout
	}
}
