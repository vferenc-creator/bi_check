package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x", "sql-jelszo.bin")
	if v, err := Load(p); err != nil || v != "" {
		t.Fatal(v, err)
	}
	if err := Save(p, "Tit;kos=Jelszó}"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "Tit;kos") {
		t.Fatal("stored in clear text")
	}
	if v, err := Load(p); err != nil || v != "Tit;kos=Jelszó}" {
		t.Fatal(v, err)
	}
	if err := Save(p, ""); err != nil || Exists(p) {
		t.Fatal("clear", err)
	}
	os.WriteFile(p, []byte("garbage"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("garbage must fail")
	}
}
