package teamstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bimonitor/internal/calendar"
	"bimonitor/internal/model"
	"bimonitor/internal/schedule"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

var t0 = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)

// instance simulates one BI Monitor running on one PC.
func instance(t *testing.T, root, user string, clk *clock) *Store {
	t.Helper()
	s := Open(Options{
		Root:     root,
		CacheDir: filepath.Join(t.TempDir(), "cache-"+user),
		Identity: Identity{User: "EF\\" + user, Display: user, Host: "PC-" + user},
		LockTTL:  10 * time.Minute,
		Timeout:  5 * time.Second,
		Now:      clk.Now,
	})
	return s
}

func newShared(t *testing.T) (root string, a, b *Store, ca, cb *clock) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "BI_Check_kozos")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	ca, cb = &clock{t: t0}, &clock{t: t0}
	a = instance(t, root, "anna", ca)
	b = instance(t, root, "bela", cb)
	if p := a.Probe(); !p.Reachable || !p.Writable || !p.Empty || p.Error != "" {
		t.Fatalf("probe: %+v", p)
	}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Store{a, b} {
		if _, err := s.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func item(name string) model.Item {
	it := model.NewItem()
	it.ID = ""
	it.Name = name
	it.Path = `\\EFS-FSRHQ\Groups\BI\` + strings.ReplaceAll(name, " ", "_") + ".xlsx"
	it.Schedule = schedule.Spec{Type: schedule.Daily, Times: []string{"06:00"}}
	return it
}

func TestInitProbeAndSyncAcrossInstances(t *testing.T) {
	root, a, b, _, _ := newShared(t)
	if p := b.Probe(); !p.Initialized || p.Empty || p.ReadOnly != "" {
		t.Fatalf("second probe: %+v", p)
	}
	if err := b.Init(); err != nil { // idempotent
		t.Fatal(err)
	}
	r, err := a.Create(item("Napi export"), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != 1 || r.Created.User != `EF\anna` || r.Created.Host != "PC-anna" || r.Schema != SchemaVersion || len(r.Changes) != 1 {
		t.Fatalf("%+v", r)
	}
	ev, err := b.Sync()
	if err != nil || len(ev) != 1 || ev[0].Kind != "added" || ev[0].ByMe || ev[0].Report.Created.Name() != "anna" {
		t.Fatalf("events %+v %v", ev, err)
	}
	if got := b.Reports(false); len(got) != 1 || got[0].Definition.Name != "Napi export" {
		t.Fatalf("%+v", got)
	}
	// Unchanged files are not re-read; nothing new happens.
	if ev, _ := b.Sync(); len(ev) != 0 {
		t.Fatalf("no events expected, got %+v", ev)
	}
	if _, err := os.Stat(filepath.Join(root, "reports", r.ID+".json")); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyOneInstanceGetsTheLock(t *testing.T) {
	root, a, _, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	const n = 12
	stores := make([]*Store, n)
	for i := range stores {
		stores[i] = instance(t, root, fmt.Sprintf("user%d", i), &clock{t: t0})
		stores[i].Sync()
	}
	for round := 0; round < 5; round++ {
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners, locked := 0, 0
		start := make(chan struct{})
		for _, s := range stores {
			wg.Add(1)
			go func(s *Store) {
				defer wg.Done()
				<-start
				_, err := s.Acquire(r.ID)
				mu.Lock()
				defer mu.Unlock()
				var le *LockedError
				switch {
				case err == nil:
					winners++
				case errors.As(err, &le):
					locked++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}(s)
		}
		close(start)
		wg.Wait()
		if winners != 1 || locked != n-1 {
			t.Fatalf("round %d: winners=%d locked=%d", round, winners, locked)
		}
		for _, s := range stores {
			s.ReleaseAll()
		}
		if _, err := os.Stat(filepath.Join(root, "locks", r.ID+".lock")); !os.IsNotExist(err) {
			t.Fatalf("lock not released: %v", err)
		}
	}
}

func TestLockedReportShowsHolderAndOthersCannotSave(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	if _, err := a.Acquire(r.ID); err != nil {
		t.Fatal(err)
	}
	_, err := b.Acquire(r.ID)
	var le *LockedError
	if !errors.As(err, &le) || le.Lock.Owner.Display != "anna" || !strings.Contains(le.Error(), "anna (PC-anna)") {
		t.Fatalf("expected LockedError by anna, got %v", err)
	}
	b.Sync()
	if l, ok := b.Locks()[r.ID]; !ok || l.Mine || l.Expired {
		t.Fatalf("b should see anna's lock: %+v", l)
	}
	it := r.Definition
	it.Name = "Béla verziója"
	if _, err := b.Save(it, r.Revision); err != ErrNotLocked {
		t.Fatalf("save without lock: %v", err)
	}
	// Releasing somebody else's lock is a no-op.
	b.Release(r.ID)
	if _, err := b.Acquire(r.ID); !errors.As(err, &le) {
		t.Fatal("anna's lock must survive Béla's Release")
	}
}

func TestOrphanedLockExpiresAndOldOwnerCannotSave(t *testing.T) {
	_, a, b, ca, cb := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	if _, err := a.Acquire(r.ID); err != nil {
		t.Fatal(err)
	}
	// 9 minutes later Anna's heartbeat keeps the lock alive.
	ca.Add(9 * time.Minute)
	cb.Add(9 * time.Minute)
	if lost := a.Heartbeat(); len(lost) != 0 {
		t.Fatal(lost)
	}
	cb.Add(5 * time.Minute) // 5 min after the heartbeat: still valid
	var le *LockedError
	if _, err := b.Acquire(r.ID); !errors.As(err, &le) {
		t.Fatalf("lock should still be valid: %v", err)
	}
	// Anna's PC freezes: no heartbeat for 11 minutes → orphaned.
	cb.Add(6 * time.Minute)
	if _, err := b.Acquire(r.ID); err != nil {
		t.Fatalf("expired lock should be taken over: %v", err)
	}
	ca.Add(11 * time.Minute)
	it := r.Definition
	it.Name = "Anna késői mentése"
	if _, err := a.Save(it, r.Revision); err != ErrNotLocked {
		t.Fatalf("old owner must not save after losing the lock: %v", err)
	}
	if lost := a.Heartbeat(); len(lost) != 0 {
		// Save already noticed it; heartbeat has nothing more to report
		_ = lost
	}
	it.Name = "Béla mentése"
	saved, err := b.Save(it, r.Revision)
	if err != nil || saved.Revision != 2 || saved.Modified.User != `EF\bela` {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestHeartbeatReportsLostLock(t *testing.T) {
	_, a, b, _, cb := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	a.Acquire(r.ID)
	cb.Add(11 * time.Minute)
	if _, err := b.Acquire(r.ID); err != nil {
		t.Fatal(err)
	}
	if lost := a.Heartbeat(); len(lost) != 1 || lost[0] != r.ID {
		t.Fatalf("lost = %v", lost)
	}
	if a.HoldsLock(r.ID) {
		t.Fatal("a must not think it still holds the lock")
	}
}

func TestTwoBreakersOnlyOneWins(t *testing.T) {
	root, a, _, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	a.Acquire(r.ID)
	late := &clock{t: t0.Add(30 * time.Minute)}
	var stores []*Store
	for i := 0; i < 8; i++ {
		s := instance(t, root, fmt.Sprintf("breaker%d", i), late)
		s.Sync()
		stores = append(stores, s)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	start := make(chan struct{})
	for _, s := range stores {
		wg.Add(1)
		go func(s *Store) {
			defer wg.Done()
			<-start
			if _, err := s.Acquire(r.ID); err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(s)
	}
	close(start)
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d", winners)
	}
}

func TestConflictingSave(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	opened, _ := b.Get(r.ID) // Béla opens rev 1 for viewing

	a.Acquire(r.ID)
	ita := r.Definition
	ita.GraceMinutes = 45
	if _, err := a.Save(ita, 1); err != nil {
		t.Fatal(err)
	}
	a.Release(r.ID)

	b.Acquire(r.ID)
	itb := opened.Definition
	itb.Name = "Béla neve"
	_, err := b.Save(itb, opened.Revision)
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.Current.Revision != 2 || ce.Current.Definition.GraceMinutes != 45 || ce.Current.Modified.Name() != "anna" {
		t.Fatalf("expected conflict, got %v", err)
	}
	// File untouched by the refused save.
	if cur, _ := b.readCurrent(r.ID); cur.Revision != 2 || cur.Definition.Name != "Riport" {
		t.Fatalf("file changed: %+v", cur)
	}
	// User chooses "keep mine": overwrite on purpose.
	saved, err := b.Save(itb, -1)
	if err != nil || saved.Revision != 3 || saved.Definition.Name != "Béla neve" {
		t.Fatalf("%+v %v", saved, err)
	}
	last := saved.Changes[len(saved.Changes)-1]
	if last.Action != "modified" || last.Stamp.User != `EF\bela` || !contains(last.Fields, "név") || !contains(last.Fields, "türelmi idő") {
		t.Fatalf("change log: %+v", last)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestHalfWrittenFilesAreIgnored(t *testing.T) {
	root, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	// A crashed writer left a temp file and a truncated report.
	os.WriteFile(filepath.Join(root, "reports", ".tmp-"+r.ID+".json-x-1"), []byte(`{"schema":1,"id":"`), 0o644)
	os.WriteFile(filepath.Join(root, "reports", r.ID+".json"), []byte(`{"schema":1,"id":"`+r.ID+`","revision":7,"definit`), 0o644)
	os.WriteFile(filepath.Join(root, "reports", "garbage.json"), []byte(`not json`), 0o644)
	ev, err := b.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) != 0 {
		t.Fatalf("no events for broken files: %+v", ev)
	}
	got, ok := b.Get(r.ID)
	if !ok || got.Revision != 1 || got.Definition.Name != "Riport" {
		t.Fatalf("last good version must be kept: %+v", got)
	}
	st := b.Status()
	if len(st.Bad) != 2 || !st.Online {
		t.Fatalf("status %+v", st)
	}
	// Writer recovers and writes a proper version → picked up.
	a.Acquire(r.ID)
	it := r.Definition
	it.Name = "Javított"
	if _, err := a.Save(it, -1); err != nil {
		t.Fatal(err)
	}
	b.Sync()
	if got, _ := b.Get(r.ID); got.Definition.Name != "Javított" {
		t.Fatalf("%+v", got)
	}
	// An unreadable (being written) lock file counts as held.
	os.WriteFile(filepath.Join(root, "locks", "zzz.lock"), nil, 0o644)
	os.Chtimes(filepath.Join(root, "locks", "zzz.lock"), t0, t0)
	var le *LockedError
	if _, err := b.Acquire("zzz"); !errors.As(err, &le) {
		t.Fatalf("empty lock file must count as held: %v", err)
	}
}

func TestNewerSchemaIsReadOnly(t *testing.T) {
	root, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	os.WriteFile(filepath.Join(root, "bicheck.json"), []byte(`{"format":"`+FolderFormat+`","schema":2,"minWriterSchema":2}`), 0o644)
	b.Sync()
	if st := b.Status(); st.ReadOnly == "" || !strings.Contains(st.ReadOnly, "újabb programverzió") {
		t.Fatalf("expected read-only: %+v", st)
	}
	var ro *ReadOnlyError
	if _, err := b.Acquire(r.ID); !errors.As(err, &ro) {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := b.Create(item("Új"), ""); !errors.As(err, &ro) {
		t.Fatalf("create: %v", err)
	}
	if p := b.Probe(); p.ReadOnly == "" {
		t.Fatal("probe should report read-only")
	}
	// Folder fine, but one report written by a newer build.
	os.WriteFile(filepath.Join(root, "bicheck.json"), []byte(`{"format":"`+FolderFormat+`","schema":1,"minWriterSchema":1}`), 0o644)
	cur, _ := a.readCurrent(r.ID)
	cur.Schema = 3
	cur.Revision = 9
	b2, _ := jsonMarshal(cur)
	os.WriteFile(filepath.Join(root, "reports", r.ID+".json"), b2, 0o644)
	b.Sync()
	if st := b.Status(); st.ReadOnly == "" {
		t.Fatal("newer report schema must make the store read-only")
	}
	a.Sync()
	if _, err := a.Acquire(r.ID); !errors.As(err, &ro) {
		t.Fatalf("a must refuse too: %v", err)
	}
	if after, _ := a.readCurrent(r.ID); after.Schema != 3 {
		t.Fatal("newer file must stay untouched")
	}
}

func TestFolderDisappearsAndReturns(t *testing.T) {
	root, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	cacheDir := b.opt.CacheDir

	hidden := root + "-offline"
	if err := os.Rename(root, hidden); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(); err == nil {
		t.Fatal("sync should fail")
	}
	st := b.Status()
	if st.Online || st.Error == "" || len(b.Reports(false)) != 1 {
		t.Fatalf("offline status %+v, reports %d", st, len(b.Reports(false)))
	}
	if _, err := b.Acquire(r.ID); err != ErrOffline {
		t.Fatalf("acquire offline: %v", err)
	}
	if _, err := b.Create(item("X"), ""); err != ErrOffline {
		t.Fatalf("create offline: %v", err)
	}
	// A restarted instance works from the local cache while offline.
	b2 := Open(Options{Root: root, CacheDir: cacheDir, Identity: b.opt.Identity, Now: b.opt.Now})
	if got := b2.Reports(false); len(got) != 1 || got[0].ID != r.ID {
		t.Fatalf("cache: %+v", got)
	}
	b2.Sync()
	if b2.Status().Online || !b2.Status().FromCache {
		t.Fatal("restarted instance should be offline, from cache")
	}

	if err := os.Rename(hidden, root); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	if !b.Status().Online {
		t.Fatal("should be back online")
	}
	if _, err := b.Acquire(r.ID); err != nil {
		t.Fatalf("editing allowed again: %v", err)
	}
}

func TestSoftDeleteRestorePurge(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	if _, err := a.Delete(r.ID, r.Revision); err != ErrNotLocked {
		t.Fatalf("delete needs the lock: %v", err)
	}
	a.Acquire(r.ID)
	d, err := a.Delete(r.ID, r.Revision)
	if err != nil || d.Deleted == nil || d.Deleted.User != `EF\anna` {
		t.Fatalf("%+v %v", d, err)
	}
	ev, _ := b.Sync()
	if len(ev) != 1 || ev[0].Kind != "deleted" {
		t.Fatalf("%+v", ev)
	}
	if len(b.Reports(false)) != 0 || len(b.Reports(true)) != 1 {
		t.Fatal("trash")
	}
	a.Release(r.ID)
	b.Acquire(r.ID)
	rs, err := b.Restore(r.ID)
	if err != nil || rs.Deleted != nil || rs.Revision != 3 {
		t.Fatalf("%+v %v", rs, err)
	}
	actions := []string{}
	for _, c := range rs.Changes {
		actions = append(actions, c.Action+"/"+c.Stamp.Name())
	}
	if strings.Join(actions, ",") != "created/anna,deleted/anna,restored/bela" {
		t.Fatalf("log %v", actions)
	}
	if err := b.Purge(r.ID); err == nil {
		t.Fatal("purge only from trash")
	}
	b.Delete(r.ID, -1)
	if err := b.Purge(r.ID); err != nil {
		t.Fatal(err)
	}
	ev, _ = a.Sync()
	if len(ev) != 1 || ev[0].Kind != "removed" || len(a.Reports(true)) != 0 {
		t.Fatalf("%+v", ev)
	}
}

func TestForceUnlockIsLogged(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	r, _ := a.Create(item("Riport"), "")
	b.Sync()
	a.Acquire(r.ID)
	prev, err := b.ForceUnlock(r.ID)
	if err != nil || prev.Owner.Display != "anna" {
		t.Fatalf("%+v %v", prev, err)
	}
	b.Sync()
	got, _ := b.Get(r.ID)
	last := got.Changes[len(got.Changes)-1]
	if last.Action != "unlocked" || last.Stamp.Name() != "bela" || !strings.Contains(last.Note, "anna") {
		t.Fatalf("%+v", last)
	}
	if _, ok := b.Locks()[r.ID]; ok {
		t.Fatal("lock must be gone")
	}
	if _, err := a.Save(r.Definition, -1); err != ErrNotLocked {
		t.Fatal("anna lost the lock")
	}
}

func TestSharedCalendar(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	if _, _, ok := b.Calendar(); ok {
		t.Fatal("no calendar yet")
	}
	if err := a.SaveCalendar(calendar.Overrides{RestDays: []string{"2026-10-09"}}, 0); err != nil {
		t.Fatal(err)
	}
	b.Sync()
	o, rev, ok := b.Calendar()
	if !ok || rev != 1 || len(o.RestDays) != 1 {
		t.Fatalf("%+v %d %v", o, rev, ok)
	}
	if err := b.SaveCalendar(calendar.Overrides{}, 0); err == nil {
		t.Fatal("stale calendar revision must be refused")
	}
	if err := b.SaveCalendar(calendar.Overrides{WorkDays: []string{"2026-10-17"}}, rev); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicateAndCreateWithExistingID(t *testing.T) {
	_, a, b, _, _ := newShared(t)
	it := item("Riport")
	it.ID = "local-123"
	if _, err := a.Create(it, "imported"); err != nil {
		t.Fatal(err)
	}
	b.Sync()
	if _, why, ok := b.Duplicate(it); !ok || why != "id" {
		t.Fatal("id duplicate")
	}
	other := item("Másik")
	other.ID = "x"
	other.Path = strings.ToUpper(it.Path)
	if _, why, ok := b.Duplicate(other); !ok || why != "path" {
		t.Fatal("path duplicate (case-insensitive)")
	}
	if _, err := b.Create(it, ""); err == nil {
		t.Fatal("creating an existing id must fail")
	}
}

func TestNotifyFlagIsNotShared(t *testing.T) {
	_, a, _, _, _ := newShared(t)
	it := item("R")
	it.Notify = false
	it.Rev = 5
	it.Source = "x"
	r, _ := a.Create(it, "")
	if !r.Definition.Notify || r.Definition.Rev != 0 || r.Definition.Source != "" {
		t.Fatalf("%+v", r.Definition)
	}
}
