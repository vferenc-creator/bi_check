package teamstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configure a Store.
type Options struct {
	Root     string // UNC path of the shared folder
	CacheDir string // local cache directory ("" = no cache)
	Identity Identity
	LockTTL  time.Duration // orphaned lock after this long without heartbeat (default 10m)
	Timeout  time.Duration // per file system operation (default 10s)
	Now      func() time.Time
}

type fileMeta struct {
	mod  time.Time
	size int64
}

// Store is safe for concurrent use.
type Store struct {
	opt Options

	mu         sync.Mutex
	reports    map[string]*Report
	meta       map[string]fileMeta
	bad        map[string]string // id → parse error (last good version kept)
	locks      map[string]Lock
	myLocks    map[string]bool
	manifest   *Manifest
	online     bool
	lastErr    string
	lastSync   time.Time
	readOnly   string // non-empty: reason why we must not write
	cacheValid bool
}

// Status summarizes the connection.
type Status struct {
	Root      string    `json:"root"`
	Online    bool      `json:"online"`
	LastSync  time.Time `json:"lastSync"`
	Error     string    `json:"error,omitempty"`
	ReadOnly  string    `json:"readOnly,omitempty"`
	Reports   int       `json:"reports"`
	Bad       []string  `json:"bad,omitempty"`
	FromCache bool      `json:"fromCache"`
}

// Event describes a change seen by Sync.
type Event struct {
	Kind   string // added | modified | deleted | restored | removed
	Report Report
	ByMe   bool
}

func (s *Store) now() time.Time { return s.opt.Now() }

func (s *Store) dir(parts ...string) string {
	return filepath.Join(append([]string{s.opt.Root}, parts...)...)
}

// Open creates a store and loads the local cache (no network access).
func Open(opt Options) *Store {
	if opt.LockTTL <= 0 {
		opt.LockTTL = 10 * time.Minute
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Identity.Instance == "" {
		opt.Identity.Instance = randHex(8)
	}
	s := &Store{
		opt: opt, reports: map[string]*Report{}, meta: map[string]fileMeta{}, bad: map[string]string{},
		locks: map[string]Lock{}, myLocks: map[string]bool{},
	}
	s.loadCache()
	return s
}

// Root returns the shared folder path.
func (s *Store) Root() string { return s.opt.Root }

// Identity of this instance.
func (s *Store) Identity() Identity { return s.opt.Identity }

func (s *Store) stamp() Stamp {
	id := s.opt.Identity
	return Stamp{User: id.User, Display: id.Display, Host: id.Host, At: s.now()}
}

// ---- Folder check / initialisation ----------------------------------------------

// ProbeResult tells whether a folder can be used.
type ProbeResult struct {
	Reachable   bool   `json:"reachable"`
	Writable    bool   `json:"writable"`
	Initialized bool   `json:"initialized"` // bicheck.json exists
	Empty       bool   `json:"empty"`       // folder has no files (safe to initialise)
	Reports     int    `json:"reports"`
	ReadOnly    string `json:"readOnly,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Probe checks reachability, write access and exclusive-create support
// without changing anything permanent.
func (s *Store) Probe() ProbeResult {
	var res ProbeResult
	err := withTimeout(s.opt.Timeout, func() error {
		st, err := os.Stat(s.opt.Root)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("ez nem mappa: %s", s.opt.Root)
		}
		res.Reachable = true
		entries, err := os.ReadDir(s.opt.Root)
		if err != nil {
			return err
		}
		res.Empty = len(entries) == 0
		var m Manifest
		if err := readJSON(s.dir("bicheck.json"), &m); err == nil {
			res.Initialized = true
			if ro := readOnlyReason(&m); ro != "" {
				res.ReadOnly = ro
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if es, err := os.ReadDir(s.dir("reports")); err == nil {
			for _, e := range es {
				if strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
					res.Reports++
				}
			}
		}
		// Write test: temp file + rename + exclusive create + delete.
		probe := ".probe-" + s.opt.Identity.Instance
		if err := writeAtomic(s.opt.Root, probe, []byte(`{"probe":true}`), "p"); err != nil {
			return fmt.Errorf("a mappa nem írható: %w", err)
		}
		defer os.Remove(s.dir(probe))
		f, err := os.OpenFile(s.dir(probe), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return errors.New("a megosztás nem támogatja a kizárólagos fájllétrehozást (zárolás nem lehetséges)")
		}
		if !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("zárolási próba: %w", err)
		}
		res.Writable = true
		return nil
	})
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func readOnlyReason(m *Manifest) string {
	if m.Format != "" && m.Format != FolderFormat {
		return "A mappa nem BI Monitor közös mappa (" + m.Format + ")."
	}
	if m.Schema > SchemaVersion || m.MinWriterSchema > SchemaVersion {
		return fmt.Sprintf("Ezt a közös mappát egy újabb programverzió kezeli (formátum: %d, ez a program: %d). Frissítse a BI Monitort – addig csak olvasni tud.", m.Schema, SchemaVersion)
	}
	return ""
}

// Init creates bicheck.json and the subfolders if they do not exist yet.
func (s *Store) Init() error {
	return withTimeout(s.opt.Timeout, func() error {
		for _, d := range []string{"reports", "locks"} {
			if err := os.MkdirAll(s.dir(d), 0o755); err != nil {
				return err
			}
		}
		var m Manifest
		err := readJSON(s.dir("bicheck.json"), &m)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		m = Manifest{Format: FolderFormat, Schema: SchemaVersion, MinWriterSchema: 1, Created: s.stamp()}
		b, _ := json.MarshalIndent(m, "", "  ")
		// Exclusive create: if two people initialise at the same time, one wins.
		f, err := os.OpenFile(s.dir(".bicheck-init"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				return nil
			}
			return err
		}
		f.Close()
		defer os.Remove(s.dir(".bicheck-init"))
		return writeAtomic(s.opt.Root, "bicheck.json", b, s.opt.Identity.Instance)
	})
}

// ---- Sync -------------------------------------------------------------------

// Sync re-reads the shared folder: only files whose time/size changed are
// parsed again. On any network problem the store goes offline and keeps the
// last known state.
func (s *Store) Sync() ([]Event, error) {
	type snapshot struct {
		manifest *Manifest
		files    map[string]fileMeta
		parsed   map[string]*Report
		badNew   map[string]string
		locks    map[string]Lock
	}
	s.mu.Lock()
	known := map[string]fileMeta{}
	for k, v := range s.meta {
		known[k] = v
	}
	s.mu.Unlock()

	var snap snapshot
	err := withTimeout(s.opt.Timeout*3, func() error {
		snap = snapshot{files: map[string]fileMeta{}, parsed: map[string]*Report{}, badNew: map[string]string{}, locks: map[string]Lock{}}
		var m Manifest
		if err := readJSON(s.dir("bicheck.json"), &m); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if _, serr := os.Stat(s.opt.Root); serr != nil {
					return serr
				}
				return errors.New("a mappa nincs előkészítve közös használatra (hiányzik a bicheck.json)")
			}
			return err
		}
		snap.manifest = &m
		entries, err := os.ReadDir(s.dir("reports"))
		if err != nil {
			return err
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".tmp-") {
				if info, err := e.Info(); err == nil && s.now().Sub(info.ModTime()) > time.Hour {
					os.Remove(s.dir("reports", name)) // orphaned temp file of a crashed writer
				}
				continue
			}
			if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			fm := fileMeta{info.ModTime(), info.Size()}
			snap.files[id] = fm
			if old, ok := known[id]; ok && old == fm {
				continue
			}
			var r Report
			if err := readJSON(s.dir("reports", name), &r); err != nil {
				snap.badNew[id] = err.Error()
				continue
			}
			if r.ID == "" {
				r.ID = id
			}
			snap.parsed[id] = &r
		}
		if es, err := os.ReadDir(s.dir("locks")); err == nil {
			for _, e := range es {
				if !strings.HasSuffix(e.Name(), ".lock") {
					continue
				}
				id := strings.TrimSuffix(e.Name(), ".lock")
				if l, err := s.readLock(id); err == nil {
					snap.locks[id] = l
				}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.online = false
		s.lastErr = humanErr(err)
		return nil, errors.New(s.lastErr)
	}
	s.online = true
	s.lastErr = ""
	s.lastSync = s.now()
	s.manifest = snap.manifest
	s.readOnly = readOnlyReason(snap.manifest)

	var events []Event
	me := s.opt.Identity
	for id, fm := range snap.files {
		if msg, isBad := snap.badNew[id]; isBad {
			if _, had := s.bad[id]; !had {
				log.Printf("közös riport sérült (%s): %s – az utolsó jó verzió marad", id, msg)
			}
			s.bad[id] = msg
			continue // keep last good version, re-read next time
		}
		delete(s.bad, id)
		r, changed := snap.parsed[id]
		if !changed {
			continue
		}
		s.meta[id] = fm
		old := s.reports[id]
		s.reports[id] = r
		if r.Schema > SchemaVersion && s.readOnly == "" {
			s.readOnly = fmt.Sprintf("A(z) „%s” riportot egy újabb programverzió mentette (formátum %d). Frissítse a BI Monitort – addig csak olvasni tud.", r.Definition.Name, r.Schema)
		}
		byMe := r.Modified.User == me.User && r.Modified.Host == me.Host
		switch {
		case old == nil && r.Deleted == nil:
			events = append(events, Event{Kind: "added", Report: *r, ByMe: byMe})
		case old == nil:
		case old.Deleted == nil && r.Deleted != nil:
			events = append(events, Event{Kind: "deleted", Report: *r, ByMe: byMe})
		case old.Deleted != nil && r.Deleted == nil:
			events = append(events, Event{Kind: "restored", Report: *r, ByMe: byMe})
		case old.Revision != r.Revision:
			events = append(events, Event{Kind: "modified", Report: *r, ByMe: byMe})
		}
	}
	for id, r := range s.reports {
		if _, ok := snap.files[id]; !ok {
			delete(s.reports, id)
			delete(s.meta, id)
			delete(s.bad, id)
			events = append(events, Event{Kind: "removed", Report: *r})
		}
	}
	for id, l := range snap.locks {
		l.Mine = l.Owner.Instance == me.Instance
		snap.locks[id] = l
	}
	s.locks = snap.locks
	s.cacheValid = false
	s.saveCacheLocked()
	return events, nil
}

func humanErr(err error) string {
	switch {
	case errors.Is(err, errTimeout):
		return "a közös mappa nem válaszolt időben"
	case errors.Is(err, fs.ErrNotExist):
		return "a közös mappa nem található vagy nem érhető el"
	case errors.Is(err, fs.ErrPermission):
		return "nincs jogosultság a közös mappához"
	}
	return err.Error()
}

// Status returns the connection state.
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Root: s.opt.Root, Online: s.online, LastSync: s.lastSync, Error: s.lastErr, ReadOnly: s.readOnly, FromCache: !s.online && len(s.reports) > 0}
	for _, r := range s.reports {
		if r.Deleted == nil {
			st.Reports++
		}
	}
	for id := range s.bad {
		st.Bad = append(st.Bad, id)
	}
	sort.Strings(st.Bad)
	return st
}

// Online reports whether the last sync succeeded.
func (s *Store) Online() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online
}

// Reports returns the reports (deleted ones only if includeDeleted), sorted by name.
func (s *Store) Reports(includeDeleted bool) []Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Report, 0, len(s.reports))
	for _, r := range s.reports {
		if r.Deleted != nil && !includeDeleted {
			continue
		}
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Definition.Name) < strings.ToLower(out[j].Definition.Name)
	})
	return out
}

// Get returns one report.
func (s *Store) Get(id string) (Report, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[id]
	if !ok {
		return Report{}, false
	}
	return *r, true
}

// Locks returns the known locks (as of the last sync / own actions).
func (s *Store) Locks() map[string]Lock {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Lock{}
	for k, l := range s.locks {
		l.Expired = s.now().Sub(l.Heartbeat) > s.opt.LockTTL
		out[k] = l
	}
	return out
}

// writable returns an error if writing is not allowed right now.
func (s *Store) writable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.online {
		return ErrOffline
	}
	if s.readOnly != "" {
		return &ReadOnlyError{s.readOnly}
	}
	return nil
}

// ---- Local cache ----------------------------------------------------------------

type cacheFile struct {
	Root     string    `json:"root"`
	SyncedAt time.Time `json:"syncedAt"`
	Reports  []*Report `json:"reports"`
	Manifest *Manifest `json:"manifest,omitempty"`
}

func (s *Store) cachePath() string {
	if s.opt.CacheDir == "" {
		return ""
	}
	return filepath.Join(s.opt.CacheDir, "shared-cache.json")
}

func (s *Store) loadCache() {
	p := s.cachePath()
	if p == "" {
		return
	}
	var c cacheFile
	if err := readJSON(p, &c); err != nil || !strings.EqualFold(c.Root, s.opt.Root) {
		return
	}
	for _, r := range c.Reports {
		s.reports[r.ID] = r
	}
	s.manifest = c.Manifest
	if c.Manifest != nil {
		s.readOnly = readOnlyReason(c.Manifest)
	}
	s.lastSync = c.SyncedAt
	// meta stays empty: the first online sync re-reads everything.
}

func (s *Store) saveCacheLocked() {
	p := s.cachePath()
	if p == "" {
		return
	}
	c := cacheFile{Root: s.opt.Root, SyncedAt: s.lastSync, Manifest: s.manifest}
	for _, r := range s.reports {
		c.Reports = append(c.Reports, r)
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.MkdirAll(s.opt.CacheDir, 0o755)
	if err := writeAtomic(s.opt.CacheDir, "shared-cache.json", b, "c"); err != nil {
		log.Printf("közös gyorsítótár mentése: %v", err)
	}
}
