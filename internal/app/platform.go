package app

import "bimonitor/internal/model"

// NotifyKind selects the icon of a desktop notification.
type NotifyKind int

const (
	NotifyInfo NotifyKind = iota
	NotifyWarning
	NotifyError
	NotifyApp
)

// FileFilter for file dialogs: display name + "*.a;*.b".
type FileFilter struct{ Name, Pattern string }

// Platform is everything the app needs from the operating system / shell.
// The Windows implementation lives in internal/desktop; tests and the
// development server use lightweight fakes.
type Platform interface {
	// Notify shows a desktop (toast) notification.
	Notify(title, text string, kind NotifyKind)
	// UpdateTray reflects the overall status in the tray icon.
	UpdateTray(sev model.Severity, tooltip string)
	// Push sends an event to the UI if it is open.
	Push(event string, data any)

	OpenFileDialog(title, initialDir string, filters []FileFilter) (string, bool)
	SaveFileDialog(title, defaultName, defExt string, filters []FileFilter) (string, bool)
	FolderDialog(title, initialDir string) (string, bool)
	ShellOpen(path string) error
	ShowInFolder(path string) error
	// ToUNC converts a mapped drive path to UNC (unchanged if not mapped).
	ToUNC(path string) (string, bool)

	SetAutostart(enabled bool) error
	AutostartEnabled() bool

	Protect(plain []byte) ([]byte, error)
	Unprotect(enc []byte) ([]byte, error)
}

// NullPlatform does nothing; embed it in fakes.
type NullPlatform struct{}

func (NullPlatform) Notify(string, string, NotifyKind)                          {}
func (NullPlatform) UpdateTray(model.Severity, string)                          {}
func (NullPlatform) Push(string, any)                                           {}
func (NullPlatform) OpenFileDialog(string, string, []FileFilter) (string, bool) { return "", false }
func (NullPlatform) SaveFileDialog(string, string, string, []FileFilter) (string, bool) {
	return "", false
}
func (NullPlatform) FolderDialog(string, string) (string, bool) { return "", false }
func (NullPlatform) ShellOpen(string) error                     { return nil }
func (NullPlatform) ShowInFolder(string) error                  { return nil }
func (NullPlatform) ToUNC(p string) (string, bool)              { return p, false }
func (NullPlatform) SetAutostart(bool) error                    { return nil }
func (NullPlatform) AutostartEnabled() bool                     { return false }
func (NullPlatform) Protect(b []byte) ([]byte, error)           { return b, nil }
func (NullPlatform) Unprotect(b []byte) ([]byte, error)         { return b, nil }
