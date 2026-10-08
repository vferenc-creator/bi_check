package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/model"
	"bimonitor/internal/teamstore"
)

// Shared ("közös") mode: the report definitions live in a folder on the
// network share (see internal/teamstore); everything personal – which
// reports notify me, quiet hours, autostart, SMTP – stays in settings.json.

type teamStateT struct {
	store  *teamstore.Store
	stop   chan struct{}
	done   chan struct{}
	mu     sync.Mutex
	recent map[string]RecentChange // id → last change by someone else
	wasOn  bool
}

// RecentChange marks a report somebody else changed recently.
type RecentChange struct {
	Kind string    `json:"kind"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// TeamItemInfo is attached to every item in shared mode.
type TeamItemInfo struct {
	Rev      int             `json:"rev"`
	Created  teamstore.Stamp `json:"created"`
	Modified teamstore.Stamp `json:"modified"`
	Lock     *LockView       `json:"lock,omitempty"`
	Recent   *RecentChange   `json:"recent,omitempty"`
	Muted    bool            `json:"muted"`
}

// LockView is a lock as shown in the UI.
type LockView struct {
	Holder  string    `json:"holder"` // "Kiss Anna (EF-PC12), 10:42 óta"
	Name    string    `json:"name"`
	Host    string    `json:"host"`
	Since   time.Time `json:"since"`
	Mine    bool      `json:"mine"`
	Expired bool      `json:"expired"`
}

// TeamView is the shared-mode part of the UI snapshot.
type TeamView struct {
	Enabled bool             `json:"enabled"`
	Folder  string           `json:"folder"`
	Status  teamstore.Status `json:"status"`
	Me      string           `json:"me"`
	Trash   int              `json:"trash"`
	LockTTL int              `json:"lockTtlMin"`
}

func defaultIdentity(id teamstore.Identity) teamstore.Identity {
	if id.User == "" {
		u := os.Getenv("USERNAME")
		if u == "" {
			u = os.Getenv("USER")
		}
		if d := os.Getenv("USERDOMAIN"); d != "" && u != "" {
			u = d + `\` + u
		}
		id.User = u
	}
	if id.Host == "" {
		id.Host, _ = os.Hostname()
	}
	if id.Instance == "" {
		id.Instance = model.NewID()
	}
	return id
}

func (a *App) teamStore() *teamstore.Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.team == nil {
		return nil
	}
	return a.team.store
}

func (a *App) teamState() *teamStateT {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.team
}

// teamItems converts the shared reports into items for the engine and UI.
func (a *App) teamItems(s model.Settings, ts *teamstore.Store) []model.Item {
	reps := ts.Reports(false)
	out := make([]model.Item, 0, len(reps))
	for _, r := range reps {
		it := r.Definition
		it.ID = r.ID
		it.Rev = r.Revision
		it.Source = ""
		it.Notify = !s.Overrides[r.ID].Mute // personal
		out = append(out, it)
	}
	return out
}

func (a *App) teamInfo(s model.Settings, ts *teamstore.Store, st *teamStateT, id string, locks map[string]teamstore.Lock) *TeamItemInfo {
	r, ok := ts.Get(id)
	if !ok {
		return nil
	}
	info := &TeamItemInfo{Rev: r.Revision, Created: r.Created, Modified: r.Modified,
		Muted: s.Overrides[id].Mute || s.GroupMuted(r.Definition.Group)}
	if l, ok := locks[id]; ok {
		info.Lock = lockView(l)
	}
	st.mu.Lock()
	if rc, ok := st.recent[id]; ok {
		cp := rc
		info.Recent = &cp
	}
	st.mu.Unlock()
	return info
}

func lockView(l teamstore.Lock) *LockView {
	n := l.Owner.Display
	if n == "" {
		n = l.Owner.User
	}
	return &LockView{Holder: l.Holder(), Name: n, Host: l.Owner.Host, Since: l.Since, Mine: l.Mine, Expired: l.Expired}
}

func (a *App) teamView(s model.Settings) *TeamView {
	ts := a.teamStore()
	tv := &TeamView{Enabled: s.Team.Enabled, Folder: s.Team.Folder, Me: a.opt.Identity.Display, LockTTL: s.Team.LockTTLMin}
	if tv.Me == "" {
		tv.Me = a.opt.Identity.User
	}
	if ts == nil {
		return tv
	}
	tv.Status = ts.Status()
	for _, r := range ts.Reports(true) {
		if r.Deleted != nil {
			tv.Trash++
		}
	}
	return tv
}

// ---- Lifecycle ------------------------------------------------------------------

func (a *App) cacheDir() string {
	if a.opt.LocalDir != "" {
		return a.opt.LocalDir
	}
	return filepath.Join(filepath.Dir(a.Settings.Path()), "local")
}

func (a *App) newTeamStore(t model.TeamSettings) *teamstore.Store {
	s := a.Settings.Get()
	return teamstore.Open(teamstore.Options{
		Root:     t.Folder,
		CacheDir: a.cacheDir(),
		Identity: a.opt.Identity,
		LockTTL:  time.Duration(t.LockTTLMin) * time.Minute,
		Timeout:  time.Duration(s.TimeoutSec) * time.Second,
	})
}

// startTeam switches to shared mode: the cached list is used immediately,
// the network is read in the background loop.
func (a *App) startTeam(t model.TeamSettings) { a.startTeamWith(t, nil) }

// startTeamWith reuses an already synced store (when the user just enabled
// shared mode) so editing works immediately.
func (a *App) startTeamWith(t model.TeamSettings, ts *teamstore.Store) {
	a.stopTeam()
	if ts == nil {
		ts = a.newTeamStore(t)
	}
	st := &teamStateT{store: ts, stop: make(chan struct{}), done: make(chan struct{}), recent: map[string]RecentChange{}, wasOn: ts.Online()}
	a.mu.Lock()
	a.team = st
	a.mu.Unlock()
	a.reconfigure()
	go a.teamLoop(st, t)
}

// stopTeam leaves shared mode (releasing our locks).
func (a *App) stopTeam() {
	a.mu.Lock()
	st := a.team
	a.team = nil
	a.mu.Unlock()
	if st == nil {
		return
	}
	close(st.stop)
	select {
	case <-st.done:
	case <-time.After(3 * time.Second):
	}
	st.store.ReleaseAll()
}

// UIClosed is called when the main window closes: open editors are gone,
// so their locks must be released.
func (a *App) UIClosed() {
	if ts := a.teamStore(); ts != nil {
		go ts.ReleaseAll()
	}
}

func (a *App) teamLoop(st *teamStateT, t model.TeamSettings) {
	defer close(st.done)
	poll := time.Duration(t.PollSec) * time.Second
	beat := time.NewTicker(time.Minute)
	defer beat.Stop()
	a.teamSync(st, true)
	timer := time.NewTimer(poll)
	defer timer.Stop()
	for {
		select {
		case <-st.stop:
			return
		case <-beat.C:
			if lost := st.store.Heartbeat(); len(lost) > 0 {
				for _, id := range lost {
					log.Printf("közös mód: elveszett zár: %s", id)
				}
				a.P.Push("lockLost", lost)
			}
		case <-timer.C:
			a.teamSync(st, false)
			timer.Reset(poll)
		}
	}
}

// TeamSyncNow re-reads the shared folder immediately.
func (a *App) teamSync(st *teamStateT, first bool) {
	events, err := st.store.Sync()
	online := err == nil
	st.mu.Lock()
	wasOn := st.wasOn
	st.wasOn = online
	st.mu.Unlock()
	if err != nil {
		if wasOn || first {
			log.Printf("közös mód: offline – %v", err)
			a.schedulePush()
		}
		return
	}
	if first || !wasOn {
		if n := st.store.CleanOwnStaleLocks(); n > 0 {
			log.Printf("közös mód: %d saját, korábbi munkamenetből maradt zár feloldva", n)
		}
	}
	if !wasOn && !first {
		log.Printf("közös mód: újra elérhető a közös mappa")
		a.P.Push("toast", map[string]any{"text": "A közös mappa újra elérhető – a lista frissült."})
		if ts := a.teamStore(); ts != nil {
			if lost := ts.Heartbeat(); len(lost) > 0 {
				a.P.Push("lockLost", lost)
			}
		}
	}
	s := a.Settings.Get()
	var notices []string
	for _, ev := range events {
		if ev.ByMe || first {
			continue
		}
		r := ev.Report
		var by string
		var text string
		switch ev.Kind {
		case "added":
			by = r.Created.Name()
			text = fmt.Sprintf("Új riport: %s – felvette: %s", r.Definition.Name, by)
		case "modified":
			by = r.Modified.Name()
			text = fmt.Sprintf("Módosított riport: %s – %s", r.Definition.Name, by)
		case "deleted":
			by = r.Deleted.Name()
			text = fmt.Sprintf("Lomtárba került: %s – %s", r.Definition.Name, by)
		case "restored":
			by = r.Modified.Name()
			text = fmt.Sprintf("Visszaállítva: %s – %s", r.Definition.Name, by)
		default:
			continue
		}
		st.mu.Lock()
		st.recent[r.ID] = RecentChange{Kind: ev.Kind, By: by, At: time.Now()}
		st.mu.Unlock()
		notices = append(notices, text)
	}
	if len(events) > 0 || first || !wasOn {
		a.reconfigure()
	} else {
		a.schedulePush() // locks may have changed
	}
	for i, n := range notices {
		if i == 4 {
			a.P.Push("toast", map[string]any{"text": fmt.Sprintf("… és még %d változás a közös listában", len(notices)-4)})
			break
		}
		a.P.Push("toast", map[string]any{"text": n})
	}
	if s.Team.ToastOnChanges && len(notices) > 0 {
		a.P.Notify("Közös riportlista változott", strings.Join(notices, "\n"), NotifyInfo)
	}
}

// ---- Editing in shared mode ---------------------------------------------------------

// ConflictInfo is returned when a save hits a newer version.
type ConflictInfo struct {
	Current    model.Item `json:"current"`
	CurrentRev int        `json:"currentRev"`
	ModifiedBy string     `json:"modifiedBy"`
	ModifiedAt time.Time  `json:"modifiedAt"`
	Fields     []string   `json:"fields"` // differing fields (mine vs. theirs)
	Message    string     `json:"message"`
}

func teamErr(err error) error {
	var ro *teamstore.ReadOnlyError
	if errors.As(err, &ro) {
		return errors.New(ro.Reason)
	}
	return err
}

// teamSave stores an item in the shared folder. force=true overwrites a
// newer version (the user chose "keep mine").
func (a *App) teamSave(ts *teamstore.Store, it model.Item, force bool) (SaveResult, error) {
	muted := !it.Notify
	if it.ID == "" {
		r, err := ts.Create(it, "")
		if err != nil {
			return SaveResult{}, teamErr(err)
		}
		it.ID = r.ID
	} else {
		if !ts.HoldsLock(it.ID) {
			if _, err := ts.Acquire(it.ID); err != nil {
				return SaveResult{}, teamErr(err)
			}
		}
		base := it.Rev
		if force {
			base = -1
		}
		_, err := ts.Save(it, base)
		if errors.Is(err, teamstore.ErrNotLocked) {
			// Our lock was broken meanwhile (by hand or as orphaned). If the
			// report is free again, take the lock and let the revision check
			// decide – a newer version then shows up as a conflict below.
			if _, lerr := ts.Acquire(it.ID); lerr != nil {
				var le *teamstore.LockedError
				if errors.As(lerr, &le) {
					return SaveResult{}, fmt.Errorf("a szerkesztési zárat közben %s vette át – a módosításai nem kerültek mentésre", le.Lock.Holder())
				}
				return SaveResult{}, teamErr(lerr)
			}
			_, err = ts.Save(it, base)
		}
		var ce *teamstore.ConflictError
		if errors.As(err, &ce) {
			cur := ce.Current.Definition
			cur.ID, cur.Rev = ce.Current.ID, ce.Current.Revision
			return SaveResult{Conflict: &ConflictInfo{
				Current: cur, CurrentRev: ce.Current.Revision, ModifiedBy: ce.Current.Modified.Name(),
				ModifiedAt: ce.Current.Modified.At, Fields: teamstore.ChangedFields(cur, it), Message: ce.Error(),
			}}, nil
		}
		if err != nil {
			return SaveResult{}, teamErr(err)
		}
		_ = ts.Release(it.ID)
	}
	// Personal notification choice.
	_ = a.setMute(it.ID, muted)
	a.reconfigure()
	return SaveResult{View: a.view(it.ID)}, nil
}

func (a *App) setMute(id string, mute bool) error {
	_, err := a.Settings.Update(func(s *model.Settings) error {
		if s.Overrides == nil {
			s.Overrides = map[string]model.ItemOverride{}
		}
		ov := s.Overrides[id]
		ov.Mute = mute
		if ov == (model.ItemOverride{}) {
			delete(s.Overrides, id)
		} else {
			s.Overrides[id] = ov
		}
		return nil
	})
	return err
}

// withTeamLock runs fn while holding the report's lock (taken and released
// here unless the caller already holds it).
func withTeamLock(ts *teamstore.Store, id string, fn func() error) error {
	held := ts.HoldsLock(id)
	if !held {
		if _, err := ts.Acquire(id); err != nil {
			return teamErr(err)
		}
		defer ts.Release(id)
	}
	return teamErr(fn())
}

// ---- Enabling / migration ---------------------------------------------------------------

// TeamCheck is the result of checking a folder before enabling shared mode.
type TeamCheck struct {
	Folder    string                `json:"folder"`
	Converted bool                  `json:"converted"`
	Probe     teamstore.ProbeResult `json:"probe"`
	OK        bool                  `json:"ok"`
	Message   string                `json:"message"`
}

// MigrationRow is one local item offered for copying into the shared folder.
type MigrationRow struct {
	Item      model.Item `json:"item"`
	Duplicate string     `json:"duplicate,omitempty"` // name of the existing shared report
	DupReason string     `json:"dupReason,omitempty"` // id | path
	DupID     string     `json:"dupId,omitempty"`
	Action    string     `json:"action"` // add | skip | overwrite
}

func (a *App) checkTeamFolder(folder string) TeamCheck {
	folder = CleanPath(folder)
	tc := TeamCheck{Folder: folder}
	if driveRe.MatchString(folder) {
		if unc, ok := a.P.ToUNC(folder); ok {
			tc.Folder, tc.Converted = unc, true
		}
	}
	if tc.Folder == "" {
		tc.Message = "Adja meg a közös mappát."
		return tc
	}
	ts := teamstore.Open(teamstore.Options{Root: tc.Folder, Identity: a.opt.Identity, Timeout: time.Duration(a.Settings.Get().TimeoutSec) * time.Second})
	tc.Probe = ts.Probe()
	switch {
	case !tc.Probe.Reachable:
		tc.Message = "A mappa nem érhető el: " + tc.Probe.Error
	case !tc.Probe.Writable:
		tc.Message = "A mappa nem írható: " + tc.Probe.Error
	case tc.Probe.ReadOnly != "":
		tc.Message = tc.Probe.ReadOnly
	case tc.Probe.Initialized:
		tc.OK, tc.Message = true, fmt.Sprintf("Meglévő közös mappa, %d riporttal. Elérhető és írható.", tc.Probe.Reports)
	case tc.Probe.Empty:
		tc.OK, tc.Message = true, "Üres mappa – a program előkészíti közös használatra."
	default:
		tc.OK, tc.Message = true, "A mappa nem üres, de még nincs benne BI Monitor lista – a program a saját almappáiba (reports, locks) dolgozik."
	}
	return tc
}

func (a *App) migrationPreview(ts *teamstore.Store) []MigrationRow {
	rows := []MigrationRow{}
	for _, it := range a.Settings.Get().Items {
		row := MigrationRow{Item: it, Action: "add"}
		if r, why, ok := ts.Duplicate(it); ok {
			row.Duplicate, row.DupReason, row.DupID, row.Action = r.Definition.Name, why, r.ID, "skip"
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) applyMigration(ts *teamstore.Store, rows []MigrationRow) (map[string]int, error) {
	counts := map[string]int{"added": 0, "overwritten": 0, "skipped": 0}
	var errs []string
	for _, row := range rows {
		it := row.Item
		switch row.Action {
		case "add":
			if _, _, dup := ts.Duplicate(it); dup {
				it.ID = "" // same id exists: copy as a new report
			}
			if _, err := ts.Create(it, "imported"); err != nil {
				errs = append(errs, it.Name+": "+teamErr(err).Error())
				continue
			}
			counts["added"]++
		case "overwrite":
			it.ID = row.DupID
			err := withTeamLock(ts, it.ID, func() error { _, err := ts.Save(it, -1); return err })
			if err != nil {
				errs = append(errs, it.Name+": "+err.Error())
				continue
			}
			counts["overwritten"]++
		default:
			counts["skipped"]++
		}
	}
	a.reconfigure()
	if len(errs) > 0 {
		return counts, errors.New("néhány elem átmásolása nem sikerült:\n" + strings.Join(errs, "\n"))
	}
	return counts, nil
}

// ---- RPC ----------------------------------------------------------------------------------

func (a *App) registerSharedModeAPI() {
	a.register("teamCheck", func(folder string) (TeamCheck, error) { return a.checkTeamFolder(folder), nil })

	type enableParam struct {
		Folder string `json:"folder"`
	}
	type enableResult struct {
		Check     TeamCheck      `json:"check"`
		Migration []MigrationRow `json:"migration"`
	}
	a.register("teamEnable", func(p enableParam) (enableResult, error) {
		tc := a.checkTeamFolder(p.Folder)
		if !tc.OK {
			return enableResult{Check: tc}, errors.New(tc.Message)
		}
		s, err := a.Settings.Update(func(s *model.Settings) error {
			s.Team.Enabled = true
			s.Team.Folder = tc.Folder
			return nil
		})
		if err != nil {
			return enableResult{}, err
		}
		ts := a.newTeamStore(s.Team)
		if err := ts.Init(); err != nil {
			return enableResult{Check: tc}, fmt.Errorf("a közös mappa előkészítése nem sikerült: %w", err)
		}
		if _, err := ts.Sync(); err != nil {
			return enableResult{Check: tc}, err
		}
		a.startTeamWith(s.Team, ts)
		var mig []MigrationRow
		if cur := a.teamStore(); cur != nil {
			mig = a.migrationPreview(cur)
		}
		return enableResult{Check: tc, Migration: mig}, nil
	})

	a.register("teamMigrate", func(rows []MigrationRow) (map[string]int, error) {
		ts := a.teamStore()
		if ts == nil {
			return nil, errors.New("a közös mód nincs bekapcsolva")
		}
		return a.applyMigration(ts, rows)
	})

	a.register("teamDisable", func() error {
		a.stopTeam()
		_, err := a.Settings.Update(func(s *model.Settings) error { s.Team.Enabled = false; return nil })
		a.reconfigure()
		return err
	})

	type teamSettingsParam struct {
		PollSec        int  `json:"pollSec"`
		LockTTLMin     int  `json:"lockTtlMin"`
		ToastOnChanges bool `json:"toastOnChanges"`
	}
	a.register("teamSettings", func(p teamSettingsParam) error {
		if p.PollSec < 5 || p.PollSec > 3600 {
			return errors.New("a frissítési időköz 5 és 3600 másodperc között lehet")
		}
		if p.LockTTLMin < 1 || p.LockTTLMin > 240 {
			return errors.New("a zár lejárati ideje 1 és 240 perc között lehet")
		}
		s, err := a.Settings.Update(func(s *model.Settings) error {
			s.Team.PollSec, s.Team.LockTTLMin, s.Team.ToastOnChanges = p.PollSec, p.LockTTLMin, p.ToastOnChanges
			return nil
		})
		if err == nil && s.Team.Enabled && a.teamStore() != nil {
			a.startTeam(s.Team) // apply new timings
		}
		return err
	})

	a.register("teamSyncNow", func() (Snapshot, error) {
		if st := a.teamState(); st != nil {
			a.teamSync(st, false)
		}
		return a.snapshot(), nil
	})

	a.register("browseFolder", func(current string) (BrowseResult, error) {
		p, ok := a.P.FolderDialog("Közös mappa kiválasztása", CleanPath(current))
		if !ok {
			return BrowseResult{}, nil
		}
		out := BrowseResult{Path: p}
		if driveRe.MatchString(p) {
			if unc, ok := a.P.ToUNC(p); ok {
				out.Path, out.Converted = unc, true
			} else {
				out.Local = true
			}
		}
		return out, nil
	})

	// LockResult tells the editor whether it may edit.
	type lockResult struct {
		OK       bool       `json:"ok"`
		Lock     *LockView  `json:"lock,omitempty"`
		Rev      int        `json:"rev"`
		Item     model.Item `json:"item"`
		Reason   string     `json:"reason,omitempty"`
		ReadOnly bool       `json:"readOnly"`
	}
	a.register("lockItem", func(id string) (lockResult, error) {
		ts := a.teamStore()
		if ts == nil {
			return lockResult{OK: true}, nil // personal mode: no locking
		}
		_, err := ts.Acquire(id)
		res := lockResult{}
		if r, ok := ts.Get(id); ok {
			res.Rev = r.Revision
			it := r.Definition
			it.ID, it.Rev = r.ID, r.Revision
			it.Notify = !a.Settings.Get().Overrides[id].Mute
			res.Item = it
		}
		var le *teamstore.LockedError
		switch {
		case err == nil:
			res.OK = true
		case errors.As(err, &le):
			res.Lock, res.Reason = lockView(le.Lock), "Szerkeszti: "+le.Lock.Holder()
		default:
			res.Reason, res.ReadOnly = teamErr(err).Error(), true
		}
		a.schedulePush()
		return res, nil
	})

	a.register("unlockItem", func(id string) error {
		if ts := a.teamStore(); ts != nil {
			err := ts.Release(id)
			a.schedulePush()
			return err
		}
		return nil
	})

	a.register("forceUnlock", func(id string) (string, error) {
		ts := a.teamStore()
		if ts == nil {
			return "", errors.New("a közös mód nincs bekapcsolva")
		}
		prev, err := ts.ForceUnlock(id)
		if err != nil {
			return "", teamErr(err)
		}
		log.Printf("zár kézi feloldása: %s (korábban: %s) – feloldotta: %s", id, prev.Holder(), a.opt.Identity.User)
		if st := a.teamState(); st != nil {
			a.teamSync(st, false)
		}
		return prev.Holder(), nil
	})

	type saveParam struct {
		Item  model.Item `json:"item"`
		Force bool       `json:"force"`
	}
	a.register("saveItemForce", func(p saveParam) (SaveResult, error) {
		ts := a.teamStore()
		if ts == nil {
			return a.saveItem(p.Item)
		}
		if _, err := a.normalizeItem(&p.Item); err != nil {
			return SaveResult{}, err
		}
		return a.teamSave(ts, p.Item, p.Force)
	})

	type trashRow struct {
		ID      string          `json:"id"`
		Item    model.Item      `json:"item"`
		Deleted teamstore.Stamp `json:"deleted"`
	}
	a.register("trashList", func() ([]trashRow, error) {
		out := []trashRow{}
		ts := a.teamStore()
		if ts == nil {
			return out, nil
		}
		for _, r := range ts.Reports(true) {
			if r.Deleted != nil {
				it := r.Definition
				it.ID = r.ID
				out = append(out, trashRow{ID: r.ID, Item: it, Deleted: *r.Deleted})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Deleted.At.After(out[j].Deleted.At) })
		return out, nil
	})
	a.register("restoreItem", func(id string) error {
		ts := a.teamStore()
		if ts == nil {
			return errors.New("a közös mód nincs bekapcsolva")
		}
		err := withTeamLock(ts, id, func() error { _, err := ts.Restore(id); return err })
		a.reconfigure()
		return err
	})
	a.register("purgeItem", func(id string) error {
		ts := a.teamStore()
		if ts == nil {
			return errors.New("a közös mód nincs bekapcsolva")
		}
		if _, err := ts.Acquire(id); err != nil {
			return teamErr(err)
		}
		err := ts.Purge(id)
		_ = ts.Release(id)
		a.reconfigure()
		return teamErr(err)
	})

	type changeView struct {
		Rev    int             `json:"rev"`
		Stamp  teamstore.Stamp `json:"stamp"`
		Action string          `json:"action"`
		Fields []string        `json:"fields,omitempty"`
		Note   string          `json:"note,omitempty"`
	}
	a.register("itemChanges", func(id string) ([]changeView, error) {
		out := []changeView{}
		ts := a.teamStore()
		if ts == nil {
			return out, nil
		}
		r, ok := ts.Get(id)
		if !ok {
			return out, nil
		}
		for i := len(r.Changes) - 1; i >= 0; i-- {
			c := r.Changes[i]
			out = append(out, changeView{Rev: c.Rev, Stamp: c.Stamp, Action: c.Action, Fields: c.Fields, Note: c.Note})
		}
		if st := a.teamState(); st != nil {
			st.mu.Lock()
			delete(st.recent, id) // seen
			st.mu.Unlock()
		}
		return out, nil
	})

	type muteParam struct {
		ID    string `json:"id"`
		Group string `json:"group"`
		Mute  bool   `json:"mute"`
	}
	a.register("setMute", func(p muteParam) error {
		if p.Group != "" {
			_, err := a.Settings.Update(func(s *model.Settings) error {
				var out []string
				for _, g := range s.MutedGroups {
					if g != p.Group {
						out = append(out, g)
					}
				}
				if p.Mute {
					out = append(out, p.Group)
				}
				s.MutedGroups = out
				return nil
			})
			a.schedulePush()
			return err
		}
		err := a.setMute(p.ID, p.Mute)
		a.reconfigure()
		return err
	})
}
