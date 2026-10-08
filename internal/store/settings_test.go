package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bimonitor/internal/model"
)

func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	st, err := OpenSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Get().CheckIntervalSec != 60 {
		t.Fatal("defaults not applied")
	}
	_, err = st.Update(func(s *model.Settings) error {
		it := model.NewItem()
		it.Name = "Teszt"
		s.Items = append(s.Items, it)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st2, err := OpenSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := st2.Get().Items; len(got) != 1 || got[0].Name != "Teszt" {
		t.Fatalf("items not persisted: %+v", got)
	}
	// Mutating a returned copy must not affect the store.
	cp := st2.Get()
	cp.Items[0].Name = "x"
	if st2.Get().Items[0].Name != "Teszt" {
		t.Fatal("Get must return a copy")
	}
}

func TestCorruptSettingsAreMovedAside(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	os.WriteFile(p, []byte("{not json"), 0o644)
	st, err := OpenSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.LoadWarning == "" {
		t.Fatal("expected a warning")
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name(), ".hibas-") {
			found = true
		}
	}
	if !found {
		t.Fatal("corrupt file should be kept for inspection")
	}
}

func TestUpdateErrorKeepsOldState(t *testing.T) {
	st, _ := OpenSettings(filepath.Join(t.TempDir(), "s.json"))
	_, err := st.Update(func(s *model.Settings) error {
		s.CheckIntervalSec = 999
		return os.ErrInvalid
	})
	if err == nil || st.Get().CheckIntervalSec != 60 {
		t.Fatal("failed update must not change settings")
	}
}
