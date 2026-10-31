package app

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"bimonitor/internal/model"
	"bimonitor/thunderbird"
)

// One-click installation of the Thunderbird add-on: the .xpi – with this
// computer's port and pairing key inside – is put into the Thunderbird
// profile's "extensions" folder. On its next start Thunderbird finds it and
// asks the user to enable it; nothing else is needed.

// TBProfile is a Thunderbird profile found on this computer.
type TBProfile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Default   bool   `json:"default"`
	Installed bool   `json:"installed"` // our add-on is in its extensions folder
	Running   bool   `json:"running"`   // Thunderbird is using it right now
}

// tbRoots are the folders that may hold profiles.ini.
func (a *App) tbRoots() []string {
	if a.opt.ThunderbirdRoot != "" {
		return []string{a.opt.ThunderbirdRoot}
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		return []string{filepath.Join(os.Getenv("APPDATA"), "Thunderbird")}
	case "darwin":
		return []string{filepath.Join(home, "Library", "Thunderbird")}
	}
	return []string{filepath.Join(home, ".thunderbird"), filepath.Join(home, "snap", "thunderbird", "common", ".thunderbird"),
		filepath.Join(home, ".var", "app", "org.mozilla.Thunderbird", ".thunderbird")}
}

// readINI parses profiles.ini into sections.
func readINI(path string) (map[string]map[string]string, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	secs := map[string]map[string]string{}
	var order []string
	cur := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), bom))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = line[1 : len(line)-1]
			if secs[cur] == nil {
				secs[cur] = map[string]string{}
				order = append(order, cur)
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && cur != "" {
			secs[cur][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return secs, order, sc.Err()
}

func profileRunning(dir string) bool {
	if runtime.GOOS == "windows" {
		// Thunderbird keeps parent.lock open exclusively while it runs.
		f, err := os.OpenFile(filepath.Join(dir, "parent.lock"), os.O_RDWR, 0)
		if err != nil {
			return !errors.Is(err, os.ErrNotExist)
		}
		f.Close()
		return false
	}
	_, err := os.Lstat(filepath.Join(dir, "lock"))
	return err == nil
}

// tbProfiles lists the Thunderbird profiles; the ones Thunderbird starts
// with by default are marked Default.
func (a *App) tbProfiles() ([]TBProfile, error) {
	for _, root := range a.tbRoots() {
		secs, order, err := readINI(filepath.Join(root, "profiles.ini"))
		if err != nil {
			continue
		}
		defaults := map[string]bool{}
		for _, name := range order {
			if strings.HasPrefix(name, "Install") && secs[name]["Default"] != "" {
				defaults[filepath.Clean(filepath.FromSlash(secs[name]["Default"]))] = true
			}
		}
		var out []TBProfile
		for _, name := range order {
			s := secs[name]
			if !strings.HasPrefix(name, "Profile") || s["Path"] == "" {
				continue
			}
			rel := filepath.Clean(filepath.FromSlash(s["Path"]))
			dir := rel
			if s["IsRelative"] != "0" {
				dir = filepath.Join(root, rel)
			}
			p := TBProfile{Name: s["Name"], Path: dir, Default: defaults[rel] || defaults[dir] || (len(defaults) == 0 && s["Default"] == "1")}
			if _, err := os.Stat(filepath.Join(dir, "extensions", thunderbird.ID+".xpi")); err == nil {
				p.Installed = true
			}
			p.Running = profileRunning(dir)
			out = append(out, p)
		}
		if len(out) == 1 {
			out[0].Default = true
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].Default && !out[j].Default })
		return out, nil
	}
	return nil, errors.New("ezen a gépen nem található Thunderbird-profil – indítsa el egyszer a Thunderbirdöt, vagy telepítse a bővítményt kézzel")
}

// TBInstallResult tells the user what happened.
type TBInstallResult struct {
	Profiles []TBProfile `json:"profiles"`
	Running  bool        `json:"running"`
	Message  string      `json:"message"`
}

// writeXPI puts the add-on into the given profile folders.
func (a *App) writeXPI(dirs []string) error {
	s := a.Settings.Get()
	xpi, err := thunderbird.Build(Version, &thunderbird.Config{Port: s.Thunderbird.Port, Key: s.Thunderbird.Key, Issued: s.Thunderbird.KeyIssued})
	if err != nil {
		return err
	}
	var errs []string
	for _, dir := range dirs {
		ext := filepath.Join(dir, "extensions")
		if err := os.MkdirAll(ext, 0o755); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		dst := filepath.Join(ext, thunderbird.ID+".xpi")
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, xpi, 0o644); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			if profileRunning(dir) {
				errs = append(errs, "a Thunderbird használja a régi bővítményfájlt – zárja be a Thunderbirdöt, és próbálja újra")
			} else {
				errs = append(errs, err.Error())
			}
			continue
		}
		log.Printf("thunderbird: bővítmény telepítve: %s", dst)
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (a *App) installThunderbird() (TBInstallResult, error) {
	// The add-on needs the connection: switch it on (and get a key) first.
	if _, err := a.Settings.Update(func(s *model.Settings) error {
		s.Thunderbird.Enabled = true
		if s.Thunderbird.Key == "" {
			s.Thunderbird.Key, s.Thunderbird.KeyIssued = newKey(), time.Now().UTC().Format(time.RFC3339Nano)
		}
		if s.Thunderbird.KeyIssued == "" {
			s.Thunderbird.KeyIssued = time.Now().UTC().Format(time.RFC3339Nano)
		}
		return nil
	}); err != nil {
		return TBInstallResult{}, err
	}
	a.syncThunderbird()
	profiles, err := a.tbProfiles()
	if err != nil {
		return TBInstallResult{}, err
	}
	var dirs []string
	for _, p := range profiles {
		if p.Default {
			dirs = append(dirs, p.Path)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{profiles[0].Path}
	}
	if err := a.writeXPI(dirs); err != nil {
		return TBInstallResult{}, fmt.Errorf("a bővítmény telepítése nem sikerült: %w", err)
	}
	if _, err := a.Settings.Update(func(s *model.Settings) error {
		s.Thunderbird.Installed, s.Thunderbird.InstalledVersion = dirs, Version
		return nil
	}); err != nil {
		return TBInstallResult{}, err
	}
	res := TBInstallResult{}
	res.Profiles, _ = a.tbProfiles()
	for _, p := range res.Profiles {
		if p.Running {
			res.Running = true
		}
	}
	if res.Running {
		res.Message = "A bővítmény a helyére került. Zárja be és indítsa újra a Thunderbirdöt, majd a megjelenő kérdésre engedélyezze a „BI Output Monitor” kiegészítőt."
	} else {
		res.Message = "A bővítmény a helyére került. Indítsa el a Thunderbirdöt, és a megjelenő kérdésre engedélyezze a „BI Output Monitor” kiegészítőt."
	}
	return res, nil
}

// refreshInstalledXPI rewrites an add-on the app installed earlier when the
// app was updated or the key changed (Thunderbird takes it over on restart).
func (a *App) refreshInstalledXPI(force bool) {
	s := a.Settings.Get()
	tb := s.Thunderbird
	if !tb.Enabled || len(tb.Installed) == 0 || (!force && (tb.InstalledVersion == Version || Version == "dev")) {
		return
	}
	var dirs []string
	for _, d := range tb.Installed {
		if _, err := os.Stat(filepath.Join(d, "extensions", thunderbird.ID+".xpi")); err == nil {
			dirs = append(dirs, d) // only where it is still installed
		}
	}
	if len(dirs) == 0 {
		return
	}
	if err := a.writeXPI(dirs); err != nil {
		log.Printf("thunderbird: a bővítmény frissítése nem sikerült: %v", err)
		return
	}
	_, _ = a.Settings.Update(func(s *model.Settings) error { s.Thunderbird.InstalledVersion = Version; return nil })
}
