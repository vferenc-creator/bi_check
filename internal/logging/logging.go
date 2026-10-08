// Package logging sets up a size-limited log file.
package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const maxSize = 5 << 20 // rotate at 5 MB, keep one old file

type rotating struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func (r *rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil || r.size+int64(len(p)) > maxSize {
		if r.f != nil {
			r.f.Close()
			_ = os.Rename(r.path, r.path+".1")
		}
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return len(p), nil // never fail the caller because of logging
		}
		st, _ := f.Stat()
		r.f, r.size = f, 0
		if st != nil {
			r.size = st.Size()
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Setup directs the standard logger to dir/monitor.log (and to extra, if any).
func Setup(dir string, extra io.Writer) {
	_ = os.MkdirAll(dir, 0o755)
	var w io.Writer = &rotating{path: filepath.Join(dir, "monitor.log")}
	if extra != nil {
		w = io.MultiWriter(w, extra)
	}
	log.SetOutput(w)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}
