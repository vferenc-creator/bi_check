// Package store persists the settings (JSON) and the history (SQLite).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bimonitor/internal/model"
)

// SettingsStore loads and atomically saves settings.json.
type SettingsStore struct {
	path string
	mu   sync.Mutex
	s    model.Settings
	// LoadWarning is set when the file was unreadable and defaults were used.
	LoadWarning string
}

// OpenSettings loads settings from path (defaults if the file is missing).
// A corrupt file is moved aside instead of being overwritten.
func OpenSettings(path string) (*SettingsStore, error) {
	st := &SettingsStore{path: path, s: model.DefaultSettings()}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st, st.save()
	case err != nil:
		return nil, err
	}
	var s model.Settings
	if err := json.Unmarshal(b, &s); err != nil {
		bad := path + ".hibas-" + time.Now().Format("20060102-150405")
		_ = os.Rename(path, bad)
		st.LoadWarning = fmt.Sprintf("A beállításfájl sérült volt, ezért alapértékekkel indultunk. A régi fájl: %s", bad)
		return st, st.save()
	}
	s.Normalize()
	st.s = s
	return st, nil
}

// Path of the settings file.
func (st *SettingsStore) Path() string { return st.path }

// Get returns a deep copy of the settings.
func (st *SettingsStore) Get() model.Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return clone(st.s)
}

// Update applies fn to a copy and saves it if fn returns nil.
func (st *SettingsStore) Update(fn func(*model.Settings) error) (model.Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	cp := clone(st.s)
	if err := fn(&cp); err != nil {
		return clone(st.s), err
	}
	cp.Normalize()
	old := st.s
	st.s = cp
	if err := st.save(); err != nil {
		st.s = old
		return clone(old), err
	}
	return clone(cp), nil
}

func clone(s model.Settings) model.Settings {
	b, _ := json.Marshal(s)
	var out model.Settings
	_ = json.Unmarshal(b, &out)
	return out
}

// save writes atomically: temp file + rename, keeping one backup.
func (st *SettingsStore) save() error {
	b, err := json.MarshalIndent(st.s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(st.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if _, err := os.Stat(st.path); err == nil {
		_ = copyFile(st.path, st.path+".bak")
	}
	if err := os.Rename(tmp.Name(), st.path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
