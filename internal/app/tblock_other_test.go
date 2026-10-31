//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"testing"
)

// holdProfileLock simulates a running Thunderbird (the "lock" symlink).
func holdProfileLock(t *testing.T, dir string) {
	if err := os.Symlink("127.0.0.1:+1234", filepath.Join(dir, "lock")); err != nil {
		t.Fatal(err)
	}
}
