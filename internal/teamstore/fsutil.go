package teamstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewID returns a random report id (GUID-like).
func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

var errTimeout = errors.New("időtúllépés")

// errCorrupt: the file exists but is not valid JSON (half written).
var errCorrupt = errors.New("sérült vagy félbeszakadt fájl")

// withTimeout runs op, giving up after d (the op keeps running in the
// background – SMB calls cannot be cancelled).
func withTimeout(d time.Duration, op func() error) error {
	if d <= 0 {
		return op()
	}
	ch := make(chan error, 1)
	go func() { ch <- op() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return errTimeout
	}
}

// transientFS reports errors that typically clear within milliseconds on
// Windows/SMB: sharing or lock violation and "access denied" on a file that
// another process is renaming or deleting.
func transientFS(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if runtime.GOOS == "windows" && errors.As(err, &errno) {
		return errno == 32 || errno == 33 // ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION
	}
	return false
}

// backoffJitter is a short, randomised pause so racing instances do not
// retry in lockstep.
func backoffJitter(attempt int) time.Duration {
	return time.Duration(10*attempt+mrand.IntN(40)) * time.Millisecond
}

// retry repeats op on errors other than "not exist"/"exist" – sharing
// violations caused by antivirus or a concurrent reader are transient.
func retry(op func() error) error {
	var err error
	for i := 0; i < 6; i++ {
		if err = op(); err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrExist) {
			return err
		}
		time.Sleep(time.Duration(20<<i) * time.Millisecond)
	}
	return err
}

// writeAtomic writes data to dir/name via a temp file + rename, then reads
// it back to verify.
func writeAtomic(dir, name string, data []byte, tag string) error {
	tmp := filepath.Join(dir, ".tmp-"+name+"-"+tag+"-"+randHex(3))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	final := filepath.Join(dir, name)
	if err := retry(func() error { return os.Rename(tmp, final) }); err != nil {
		os.Remove(tmp)
		return err
	}
	back, err := os.ReadFile(final)
	if err != nil {
		return err
	}
	if !json.Valid(back) {
		return fmt.Errorf("a(z) %s visszaolvasása után sérült tartalom", name)
	}
	return nil
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
		return fmt.Errorf("%w (%s): %v", errCorrupt, filepath.Base(path), err)
	}
	return nil
}

func jsonMarshal(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }
