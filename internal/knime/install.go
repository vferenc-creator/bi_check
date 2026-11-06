// Package knime runs KNIME workflows headless (the "KNIME" menu of the BI
// Monitor): it finds the KNIME installation, installs the NodePit Batch
// extension from its zipped update site, keeps the jobs and their runs, and
// starts, times out and evaluates the runs.
//
// Since KNIME 5.9 the built-in batch executor can no longer be installed, so
// the free NodePit Batch executor is used:
//
//	knime -nosplash -application com.nodepit.batch.application.NodePitBatchExecutor
//	      -data <workspace> [--reset] [--no-save] [--variable=name;value;type]
//	      [--credential=name;login;password] <workflow>
//
// It is independent of the file and e-mail monitoring of the application.
package knime

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	// FeatureIU is the installable unit of NodePit Batch.
	FeatureIU = "com.nodepit.batch.feature.feature.group"
	// AppID is the Eclipse application of the NodePit Batch executor.
	AppID = "com.nodepit.batch.application.NodePitBatchExecutor"
	// DirectorApp installs and removes extensions (Eclipse p2).
	DirectorApp = "org.eclipse.equinox.p2.director"
	// NodePitSite hosts the NodePit Batch downloads.
	NodePitSite = "https://download.nodepit.com/"

	nodepitBundle = "com.nodepit.batch.application_"
	featurePrefix = "features/com.nodepit.batch.feature_"
)

// Install describes a KNIME installation.
type Install struct {
	Exe      string `json:"exe"`
	Dir      string `json:"dir"`
	Version  string `json:"version"`  // e.g. 5.12.0 ("" = unknown)
	NodePit  string `json:"nodepit"`  // installed NodePit Batch version ("" = none)
	Writable bool   `json:"writable"` // extensions can be installed without admin rights
	Error    string `json:"error,omitempty"`
}

// Minor is "5.12" of "5.12.0" ("" if unknown).
func (in Install) Minor() string { return minorOf(in.Version) }

func minorOf(v string) string {
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return ""
	}
	return p[0] + "." + p[1]
}

// ExeName is the launcher of KNIME on this OS.
func ExeName() string {
	if runtime.GOOS == "windows" {
		return "knime.exe"
	}
	return "knime"
}

// Inspect reads a KNIME installation from its launcher (or its folder).
func Inspect(path string) Install {
	path = strings.TrimSpace(strings.Trim(strings.TrimSpace(path), `"`))
	in := Install{Exe: path}
	if path == "" {
		in.Error = "nincs megadva a KNIME"
		return in
	}
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, ExeName())
		in.Exe = path
	}
	if _, err := os.Stat(path); err != nil {
		in.Error = "a KNIME nem található: " + path
		return in
	}
	in.Dir = filepath.Dir(path)
	plugins := filepath.Join(in.Dir, "plugins")
	ents, err := os.ReadDir(plugins)
	if err != nil {
		in.Error = "ez nem KNIME-telepítés (nincs plugins mappa): " + in.Dir
		return in
	}
	var core, product, nodepit []string
	for _, e := range ents {
		n := strings.TrimSuffix(e.Name(), ".jar")
		switch {
		case strings.HasPrefix(n, "org.knime.core_"):
			core = append(core, strings.TrimPrefix(n, "org.knime.core_"))
		case strings.HasPrefix(n, "org.knime.product_"):
			product = append(product, strings.TrimPrefix(n, "org.knime.product_"))
		case strings.HasPrefix(n, nodepitBundle):
			nodepit = append(nodepit, strings.TrimPrefix(n, nodepitBundle))
		}
	}
	vs := core
	if len(vs) == 0 {
		vs = product
	}
	if len(vs) == 0 {
		in.Error = "ez nem KNIME-telepítés (nincs org.knime.core): " + in.Dir
		return in
	}
	in.Version = shortVersion(newest(vs))
	if len(nodepit) > 0 {
		in.NodePit = shortVersion(newest(nodepit))
	}
	in.Writable = writable(plugins)
	return in
}

// shortVersion: "5.12.0.v202604161011" → "5.12.0".
func shortVersion(v string) string {
	p := strings.Split(v, ".")
	if len(p) > 3 {
		p = p[:3]
	}
	return strings.Join(p, ".")
}

func newest(vs []string) string {
	sort.Slice(vs, func(i, j int) bool { return compareVersion(vs[i], vs[j]) < 0 })
	return vs[len(vs)-1]
}

func compareVersion(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, e1 := strconv.Atoi(pa[i])
		y, e2 := strconv.Atoi(pb[i])
		if e1 != nil || e2 != nil {
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
			continue
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return len(pa) - len(pb)
}

func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".bimonitor-irasproba-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Candidates are the usual places of knime.exe.
func Candidates() []string {
	var out []string
	add := func(globs ...string) {
		for _, g := range globs {
			m, _ := filepath.Glob(g)
			sort.Sort(sort.Reverse(sort.StringSlice(m))) // newest folder name first
			out = append(out, m...)
		}
	}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("LOCALAPPDATA") + `\Programs`, `C:`, `D:`, os.Getenv("USERPROFILE")} {
			if base == "" || base == `\Programs` {
				continue
			}
			add(filepath.Join(base, "KNIME", "knime.exe"), filepath.Join(base, "KNIME*", "knime.exe"), filepath.Join(base, "knime*", "knime.exe"))
		}
	} else {
		home, _ := os.UserHomeDir()
		add("/opt/knime*/knime", filepath.Join(home, "knime*", "knime"))
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, p := range out {
		k := strings.ToLower(filepath.Clean(p))
		if !seen[k] {
			seen[k] = true
			uniq = append(uniq, p)
		}
	}
	return uniq
}

// Detect returns the first KNIME installation found.
func Detect() (Install, bool) {
	for _, c := range Candidates() {
		if in := Inspect(c); in.Error == "" {
			return in, true
		}
	}
	return Install{}, false
}

// ZipInfo describes a zipped update site of NodePit Batch.
type ZipInfo struct {
	Path     string `json:"path"`
	Version  string `json:"version"`  // NodePit Batch version in the zip
	ForKNIME string `json:"forKnime"` // KNIME version from the file name ("5.12"), if any
}

var zipKnimeVer = regexp.MustCompile(`(?:^|[^0-9])([4-9]\.[0-9]{1,2})(?:[^0-9]|$)`)

// InspectZip checks that path is the zipped NodePit Batch update site
// (as downloaded from nodepit.com, e.g. "5.12.zip").
func InspectZip(path string) (ZipInfo, error) {
	zi := ZipInfo{Path: path}
	r, err := zip.OpenReader(path)
	if err != nil {
		return zi, fmt.Errorf("a fájl nem olvasható zipként: %w", err)
	}
	defer r.Close()
	meta, arts := false, false
	for _, f := range r.File {
		switch n := f.Name; {
		case n == "content.jar" || n == "content.xml" || n == "content.xml.xz":
			meta = true
		case n == "artifacts.jar" || n == "artifacts.xml" || n == "artifacts.xml.xz":
			arts = true
		case strings.HasPrefix(n, featurePrefix) && strings.HasSuffix(n, ".jar"):
			zi.Version = shortVersion(strings.TrimSuffix(strings.TrimPrefix(n, featurePrefix), ".jar"))
		}
	}
	if !meta || !arts {
		return zi, errors.New("ez nem egy KNIME-bővítmény frissítési helye (hiányzik a content/artifacts leíró)")
	}
	if zi.Version == "" {
		return zi, errors.New("a zipben nincs NodePit Batch bővítmény (com.nodepit.batch.feature)")
	}
	if m := zipKnimeVer.FindStringSubmatch(filepath.Base(path)); m != nil {
		zi.ForKNIME = m[1]
	}
	return zi, nil
}

// RepoURL is the p2 address of a zipped update site.
func RepoURL(zipPath string) string {
	abs, err := filepath.Abs(zipPath)
	if err != nil {
		abs = zipPath
	}
	return "jar:file:/" + strings.TrimPrefix(filepath.ToSlash(abs), "/") + "!/"
}

// ZipURL is the download address of the zipped NodePit Batch update site
// for a KNIME version, e.g. https://download.nodepit.com/batch/5.12.zip.
func ZipURL(minor string) string { return NodePitSite + "batch/" + minor + ".zip" }

// Download fetches url into dst (via a temporary file) and checks that it
// is a NodePit Batch update site.
func Download(ctx context.Context, url, dst string) (ZipInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ZipInfo{}, err
	}
	req.Header.Set("User-Agent", "BI-Monitor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ZipInfo{}, fmt.Errorf("a letöltés nem sikerült (van internet a gépen?): %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ZipInfo{}, fmt.Errorf("a letöltés nem sikerült: %s – lehet, hogy ehhez a KNIME-verzióhoz még nincs NodePit Batch (%s)", resp.Status, url)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return ZipInfo{}, err
	}
	tmp := dst + ".letoltes"
	f, err := os.Create(tmp)
	if err != nil {
		return ZipInfo{}, err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return ZipInfo{}, fmt.Errorf("a letöltés megszakadt: %w", err)
	}
	if _, err := InspectZip(tmp); err != nil {
		os.Remove(tmp)
		return ZipInfo{}, fmt.Errorf("a letöltött fájl nem NodePit Batch bővítmény: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return ZipInfo{}, err
	}
	return InspectZip(dst)
}

// DirectorArgs installs (or with uninstall removes) NodePit Batch.
func DirectorArgs(repo string, uninstall bool) []string {
	op := "-installIU"
	if uninstall {
		op = "-uninstallIU"
	}
	args := []string{"-nosplash", "--launcher.suppressErrors", "-consoleLog", "-application", DirectorApp, op, FeatureIU}
	if repo != "" {
		args = append(args, "-repository", repo)
	}
	return args
}
