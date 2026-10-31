package dbmap

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// FolderName is the sub-folder of the shared folder (or of the local data
// folder) that holds the maps. It is entirely separate from the files of
// the shared monitoring list (bicheck.json, reports\, locks\), which are
// never touched by this package.
const FolderName = "adatbazis-terkep"

// Store keeps the map (<key>.json, small, rewritten on every edit) and the
// last fetched structure (<key>.schema.json, rewritten only on a fetch) of
// each database. With a shared folder every computer of the team works on
// the same files; a local copy is kept so the map can be viewed offline.
type Store struct {
	Shared  string // shared folder (team mode) or "" (local only)
	Local   string // local data folder
	Who     string // "Name (PC)" recorded on edits
	Timeout time.Duration

	mu sync.Mutex
}

// Key turns server+database into a file name.
func Key(server, database string) string {
	k := strings.ToLower(strings.TrimSpace(server) + "__" + strings.TrimSpace(database))
	k = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(k, "_")
	if len(k) > 120 {
		k = k[:120]
	}
	return k
}

func (s *Store) sharedDir() string {
	if s.Shared == "" {
		return ""
	}
	return filepath.Join(s.Shared, FolderName)
}
func (s *Store) localDir() string { return filepath.Join(s.Local, FolderName) }

func (s *Store) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 15 * time.Second
	}
	return s.Timeout
}

// Snapshot is what the UI gets: the map and the structure.
type Snapshot struct {
	Doc    *Doc    `json:"doc"`
	Schema *Schema `json:"schema"`
	// FromCache: the shared folder could not be read, this is the local copy.
	FromCache bool   `json:"fromCache,omitempty"`
	Warning   string `json:"warning,omitempty"`
	Shared    bool   `json:"shared"`
}

// Load reads the map and structure of a database (an empty map if none).
func (s *Store) Load(server, database string) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(server, database)
}

func (s *Store) load(server, database string) (*Snapshot, error) {
	key := Key(server, database)
	snap := &Snapshot{Shared: s.Shared != ""}
	var doc Doc
	var sch Schema
	if dir := s.sharedDir(); dir != "" {
		var errDoc, errSch error
		err := withTimeout(s.timeout(), func() error {
			errDoc = readJSON(filepath.Join(dir, key+".json"), &doc)
			errSch = readJSON(filepath.Join(dir, key+".schema.json"), &sch)
			return nil
		})
		switch {
		case err != nil || (errDoc != nil && !errors.Is(errDoc, fs.ErrNotExist)):
			snap.FromCache = true
			snap.Warning = "A közös mappa most nem olvasható, a gépen tárolt másolat látszik (módosítani nem lehet)."
		case errors.Is(errDoc, fs.ErrNotExist):
			// first use in shared mode: start from the local map if any
			doc, sch = Doc{}, Schema{}
			_ = readJSON(filepath.Join(s.localDir(), key+".json"), &doc)
			_ = readJSON(filepath.Join(s.localDir(), key+".schema.json"), &sch)
		default:
			if errSch != nil {
				sch = Schema{}
			}
			s.cache(key, &doc, &sch)
		}
		if !snap.FromCache {
			snap.Doc, snap.Schema = finish(&doc, &sch, server, database)
			return snap, nil
		}
		doc, sch = Doc{}, Schema{}
	}
	_ = readJSON(filepath.Join(s.localDir(), key+".json"), &doc)
	_ = readJSON(filepath.Join(s.localDir(), key+".schema.json"), &sch)
	snap.Doc, snap.Schema = finish(&doc, &sch, server, database)
	return snap, nil
}

func finish(doc *Doc, sch *Schema, server, database string) (*Doc, *Schema) {
	if doc.Format == 0 {
		*doc = *NewDoc(server, database)
	}
	doc.fix()
	var out *Schema
	if sch.FetchedAt.IsZero() && len(sch.Tables) == 0 {
		out = nil
	} else {
		out = sch
		if out.Tables == nil {
			out.Tables = []Table{}
		}
		if out.FKs == nil {
			out.FKs = []FK{}
		}
	}
	return doc, out
}

// ErrNewerFormat: the map was written by a newer version of the program.
var ErrNewerFormat = errors.New("a térképet a program egy újabb verziója mentette – frissítse a programot a módosításhoz")

// Update changes the map under a lock: fn gets the current map and
// structure; if newSchema is returned it is stored as well.
func (s *Store) Update(server, database string, fn func(d *Doc, sch *Schema) (newSchema *Schema, err error)) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(server, database)
	dir := s.sharedDir()
	if dir == "" {
		dir = s.localDir()
	}
	var out *Snapshot
	err := withTimeout(2*s.timeout(), func() error {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("a térkép mappája nem hozható létre: %w", err)
		}
		unlock, err := lockFile(filepath.Join(dir, key+".lock"), s.timeout())
		if err != nil {
			return err
		}
		defer unlock()
		snap, err := s.load(server, database)
		if err != nil {
			return err
		}
		if snap.FromCache {
			return errors.New("a közös mappa most nem érhető el, a módosítás nem menthető")
		}
		if snap.Doc.Format > DocFormat {
			return ErrNewerFormat
		}
		newSch, err := fn(snap.Doc, snap.Schema)
		if err != nil {
			return err
		}
		snap.Doc.Rev++
		snap.Doc.UpdatedAt = time.Now()
		snap.Doc.UpdatedBy = s.Who
		snap.Doc.Format = DocFormat
		if newSch != nil {
			b, err := json.Marshal(newSch)
			if err != nil {
				return err
			}
			if err := writeAtomic(dir, key+".schema.json", b); err != nil {
				return err
			}
			snap.Schema = newSch
		}
		b, err := json.MarshalIndent(snap.Doc, "", " ")
		if err != nil {
			return err
		}
		if err := writeAtomic(dir, key+".json", b); err != nil {
			return err
		}
		if s.Shared != "" {
			s.cache(key, snap.Doc, snap.Schema)
		}
		out = snap
		return nil
	})
	if errors.Is(err, errTimeout) {
		return nil, errors.New("a térkép mentése időtúllépés miatt nem sikerült (lassú vagy elérhetetlen mappa)")
	}
	return out, err
}

// cache keeps a local copy of the shared map (best effort).
func (s *Store) cache(key string, doc *Doc, sch *Schema) {
	dir := s.localDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	if b, err := json.MarshalIndent(doc, "", " "); err == nil {
		_ = writeAtomic(dir, key+".json", b)
	}
	if sch != nil {
		if b, err := json.Marshal(sch); err == nil {
			_ = writeAtomic(dir, key+".schema.json", b)
		}
	}
}

// ---- file helpers --------------------------------------------------------------

var errTimeout = errors.New("időtúllépés")

func withTimeout(d time.Duration, op func() error) error {
	ch := make(chan error, 1)
	go func() { ch <- op() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return errTimeout
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func retry(op func() error) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = op(); err == nil || errors.Is(err, fs.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(20<<i) * time.Millisecond)
	}
	return err
}

func readJSON(path string, v any) error {
	var b []byte
	err := retry(func() error {
		var e error
		b, e = os.ReadFile(path)
		return e
	})
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("sérült fájl (%s): %v", filepath.Base(path), err)
	}
	return nil
}

func writeAtomic(dir, name string, data []byte) error {
	tmp := filepath.Join(dir, ".tmp-"+name+"-"+randHex(4))
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := retry(func() error { return os.Rename(tmp, filepath.Join(dir, name)) }); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// lockFile takes an exclusive lock file (another computer editing the same
// map waits a moment); a lock older than 30 s is from a crashed writer.
func lockFile(path string, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for i := 0; ; i++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return func() { _ = retry(func() error { return os.Remove(path) }) }, nil
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > 30*time.Second {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("a térképet épp más menti – próbálja újra néhány másodperc múlva")
		}
		time.Sleep(time.Duration(30+10*min(i, 20)) * time.Millisecond)
	}
}
