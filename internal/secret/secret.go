// Package secret stores small secrets (the SQL password) so that only the
// current Windows user can read them back (DPAPI). Other platforms (the
// development server) only obfuscate.
package secret

import (
	"errors"
	"os"
	"path/filepath"
)

// Save writes the protected secret to path ("" removes the file).
func Save(path, value string) error {
	if value == "" {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	b, err := protect([]byte(value))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads the secret ("" if there is none).
func Load(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	v, err := unprotect(b)
	if err != nil {
		return "", errors.New("a mentett jelszó nem olvasható (másik Windows-felhasználó vagy gép?) – adja meg újra")
	}
	return string(v), nil
}

// Exists reports whether a secret is stored.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
