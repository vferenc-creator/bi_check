// Package thunderbird holds the BI Monitor Thunderbird add-on (the files next
// to this one) and builds its .xpi – optionally with the connection settings
// of this computer baked in, so the add-on works right after installation.
package thunderbird

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"regexp"
	"time"
)

// ID is the add-on id (also the file name in a Thunderbird profile).
const ID = "bimonitor@energofish.hu"

//go:embed manifest.json lib.js background.js popup.html popup.js popup.css options.html options.js options.css ui.css icons/*.png
var files embed.FS

// Config is written into the package as config.json: the add-on reads it on
// start and uses it when it has no own settings yet (or the config is newer).
type Config struct {
	Port   int    `json:"port"`
	Key    string `json:"key"`
	Issued string `json:"issued"` // changes on every new key → the add-on takes it over
}

var numericVersion = regexp.MustCompile(`^\d+(\.\d+){0,3}$`)

// Build returns the .xpi. version goes into the manifest (non-numeric
// versions such as "dev" become 0.0.0); cfg may be nil.
func Build(version string, cfg *Config) ([]byte, error) {
	if !numericVersion.MatchString(version) {
		version = "0.0.0"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		if p == "manifest.json" {
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				return err
			}
			m["version"] = version
			if data, err = json.MarshalIndent(m, "", "  "); err != nil {
				return err
			}
		}
		return add(p, data)
	})
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		b, _ := json.MarshalIndent(cfg, "", "  ")
		if err := add("config.json", b); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
