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

// A settings file from an older version has an empty database connection
// with Windows login: it gets the Energofish BI defaults (SQL user, no
// password), while a connection the user set up is kept as it is.
func TestOldSettingsGetDatabaseDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	old := `{"checkIntervalSec":60,"database":{"server":"","database":"","auth":"windows","encrypt":"trust","timeoutSec":30,"maxRows":100000}}`
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	d := st.Get().Database
	if d.Server != "EF-BI" || d.Port != 1433 || d.Database != "Dashboard" || d.Auth != "sql" || d.User != "d.admin" {
		t.Fatalf("defaults not applied: %+v", d)
	}

	own := `{"checkIntervalSec":60,"database":{"server":"MASIK","database":"Teszt","auth":"windows"}}`
	if err := os.WriteFile(p, []byte(own), 0o600); err != nil {
		t.Fatal(err)
	}
	if st, err = OpenSettings(p); err != nil {
		t.Fatal(err)
	}
	if d := st.Get().Database; d.Server != "MASIK" || d.Database != "Teszt" || d.Auth != "windows" || d.User != "" {
		t.Fatalf("own connection changed: %+v", d)
	}
}

// The "Teljes tábla" row limit is kept as set (e.g. 10 million) – it used to
// fall back to 100 000 above one million. Above the maximum it is capped.
func TestDatabaseRowLimitIsKept(t *testing.T) {
	st, err := OpenSettings(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]int{{10_000_000, 10_000_000}, {250_000_000, model.MaxDBRows}, {5, 100_000}, {1_500_000, 1_500_000}} {
		s, err := st.Update(func(s *model.Settings) error { s.Database.MaxRows = c[0]; return nil })
		if err != nil || s.Database.MaxRows != c[1] {
			t.Fatalf("%d → %d, want %d (%v)", c[0], s.Database.MaxRows, c[1], err)
		}
	}
}
