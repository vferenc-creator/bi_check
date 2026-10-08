// Package paths resolves where the monitor keeps its files.
//
//	%APPDATA%\EnergofishMonitor       settings.json (roams with the profile)
//	%LOCALAPPDATA%\EnergofishMonitor  history.db, logs, WebView2 cache (machine-local)
package paths

import (
	"os"
	"path/filepath"

	"bimonitor/branding"
)

// Override (for tests or --data-dir) replaces both directories.
var Override string

func ensure(dir string) string {
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// ConfigDir is the roaming configuration directory.
func ConfigDir() string {
	if Override != "" {
		return ensure(Override)
	}
	base, err := os.UserConfigDir() // %APPDATA% on Windows
	if err != nil {
		base = "."
	}
	return ensure(filepath.Join(base, branding.Current.DataFolder))
}

// LocalDir is the machine-local data directory.
func LocalDir() string {
	if Override != "" {
		return ensure(filepath.Join(Override, "local"))
	}
	base, err := os.UserCacheDir() // %LOCALAPPDATA% on Windows
	if err != nil {
		base = "."
	}
	return ensure(filepath.Join(base, branding.Current.DataFolder))
}

// SettingsFile is the full path of settings.json.
func SettingsFile() string { return filepath.Join(ConfigDir(), "settings.json") }

// HistoryFile is the full path of the history database.
func HistoryFile() string { return filepath.Join(LocalDir(), "history.db") }

// LogDir is where log files go.
func LogDir() string { return ensure(filepath.Join(LocalDir(), "logs")) }

// WebViewDir is the WebView2 user data folder.
func WebViewDir() string { return ensure(filepath.Join(LocalDir(), "WebView2")) }
