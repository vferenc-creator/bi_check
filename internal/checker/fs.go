package checker

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// FileInfo describes a file found on disk.
type FileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	ModTime time.Time `json:"modTime"`
	Size    int64     `json:"size"`
}

// FS abstracts the file system so the checker can be tested with fakes.
type FS interface {
	// Stat returns information about a file (or directory, for probes).
	Stat(path string) (FileInfo, error)
	// ListFiles returns the regular files in dir.
	ListFiles(dir string, join func(name string) string) ([]FileInfo, error)
}

// OSFS is the real file system.
type OSFS struct{}

// Stat implements FS.
func (OSFS) Stat(path string) (FileInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: path, Name: st.Name(), ModTime: st.ModTime(), Size: st.Size()}, nil
}

// ListFiles implements FS.
func (OSFS) ListFiles(dir string, join func(string) string) ([]FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // deleted meanwhile
			}
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out = append(out, FileInfo{Path: join(e.Name()), Name: e.Name(), ModTime: info.ModTime(), Size: info.Size()})
	}
	return out, nil
}
