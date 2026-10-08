package checker

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeFS simulates a few shares.
type fakeFS struct {
	mu    sync.Mutex
	files map[string]FileInfo // full path (lowercase) → info
	dirs  map[string]bool     // lowercase
	down  map[string]error    // server (upper) → error for every call
	hang  map[string]bool     // server → block until released
	deny  map[string]bool     // lowercase path prefix → permission error
	block chan struct{}
	calls atomic.Int64
}

func newFake() *fakeFS {
	return &fakeFS{files: map[string]FileInfo{}, dirs: map[string]bool{}, down: map[string]error{}, hang: map[string]bool{}, deny: map[string]bool{}, block: make(chan struct{})}
}

func (f *fakeFS) addFile(path string, mod time.Time, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := strings.LastIndex(path, `\`)
	f.files[strings.ToLower(path)] = FileInfo{Path: path, Name: path[i+1:], ModTime: mod, Size: size}
	for d := path[:i]; strings.Count(d, `\`) >= 3; d = d[:strings.LastIndex(d, `\`)] {
		f.dirs[strings.ToLower(d)] = true
	}
}

func server(p string) string {
	parts := strings.SplitN(strings.TrimPrefix(p, `\\`), `\`, 2)
	return strings.ToUpper(parts[0])
}

func (f *fakeFS) pre(p string) error {
	f.calls.Add(1)
	f.mu.Lock()
	hang := f.hang[server(p)]
	err := f.down[server(p)]
	denied := false
	for pre := range f.deny {
		if strings.HasPrefix(strings.ToLower(p), pre) {
			denied = true
		}
	}
	f.mu.Unlock()
	if hang {
		<-f.block
	}
	if err != nil {
		return &os.PathError{Op: "stat", Path: p, Err: err}
	}
	if denied {
		return &os.PathError{Op: "stat", Path: p, Err: eDenied}
	}
	return nil
}

func (f *fakeFS) Stat(p string) (FileInfo, error) {
	if err := f.pre(p); err != nil {
		return FileInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if fi, ok := f.files[strings.ToLower(p)]; ok {
		return fi, nil
	}
	if f.dirs[strings.ToLower(p)] {
		return FileInfo{Path: p}, nil
	}
	parent := strings.ToLower(p[:strings.LastIndex(p, `\`)])
	if f.dirs[parent] {
		return FileInfo{}, &os.PathError{Op: "stat", Path: p, Err: eNoFile}
	}
	return FileInfo{}, &os.PathError{Op: "stat", Path: p, Err: eNoPath}
}

func (f *fakeFS) ListFiles(dir string, join func(string) string) ([]FileInfo, error) {
	if err := f.pre(dir); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.dirs[strings.ToLower(dir)] {
		return nil, &os.PathError{Op: "readdir", Path: dir, Err: eNoPath}
	}
	var out []FileInfo
	prefix := strings.ToLower(dir) + `\`
	for k, v := range f.files {
		if strings.HasPrefix(k, prefix) && !strings.Contains(k[len(prefix):], `\`) {
			out = append(out, v)
		}
	}
	return out, nil
}

var ref = time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func TestExactFile(t *testing.T) {
	f := newFake()
	f.addFile(`\\EFS\Groups\BI\export.xlsx`, ref.Add(-time.Hour), 1234)
	c := New(f, Options{})
	r := c.Check(`\\EFS\Groups\BI\export.xlsx`, ref, false)
	if r.Kind != Found || r.File.Size != 1234 || r.Server != "EFS" {
		t.Fatalf("%+v", r)
	}
	r = c.Check(`\\EFS\Groups\BI\missing.xlsx`, ref, false)
	if r.Kind != NotFound {
		t.Fatalf("want NotFound, got %+v", r)
	}
}

func TestMissingFolderOnReachableShare(t *testing.T) {
	f := newFake()
	f.addFile(`\\EFS\Groups\BI\export.xlsx`, ref, 1)
	c := New(f, Options{})
	r := c.Check(`\\EFS\Groups\NoSuchDir\x.xlsx`, ref, false)
	if r.Kind != NotFound || !strings.Contains(r.Reason, "mappa nem létezik") {
		t.Fatalf("%+v", r)
	}
	r = c.Check(`\\EFS\Groups\NoSuchDir\x_*.xlsx`, ref, false)
	if r.Kind != NotFound || !strings.Contains(r.Reason, "mappa nem létezik") {
		t.Fatalf("pattern: %+v", r)
	}
}

func TestServerDownIsUnreachableAndOpensBreaker(t *testing.T) {
	f := newFake()
	f.addFile(`\\EFS\Groups\BI\a.xlsx`, ref, 1)
	f.addFile(`\\EFS\Groups\BI\b.xlsx`, ref, 1)
	f.down["EFS"] = eServerDown
	clk := &clock{t: ref}
	c := New(f, Options{Now: clk.Now})
	r := c.Check(`\\EFS\Groups\BI\a.xlsx`, ref, false)
	if r.Kind != Unreachable || !r.ServerProblem {
		t.Fatalf("network error must not look like a missing file: %+v", r)
	}
	before := f.calls.Load()
	r = c.Check(`\\EFS\Groups\BI\b.xlsx`, ref, false)
	if r.Kind != Unreachable || f.calls.Load() != before {
		t.Fatalf("breaker should answer without touching the share: %+v calls=%d→%d", r, before, f.calls.Load())
	}
	// Server comes back, breaker half-opens after the backoff.
	delete(f.down, "EFS")
	clk.Add(31 * time.Second)
	if r = c.Check(`\\EFS\Groups\BI\b.xlsx`, ref, false); r.Kind != Found {
		t.Fatalf("should recover: %+v", r)
	}
}

func TestBackoffGrows(t *testing.T) {
	f := newFake()
	f.down["X"] = eServerDown
	clk := &clock{t: ref}
	c := New(f, Options{Now: clk.Now, BackoffMin: 10 * time.Second, BackoffMax: 35 * time.Second})
	c.Check(`\\X\s\a.csv`, ref, false)
	clk.Add(11 * time.Second)
	c.Check(`\\X\s\a.csv`, ref, false) // 2nd failure → 20s
	clk.Add(15 * time.Second)
	n := f.calls.Load()
	c.Check(`\\X\s\a.csv`, ref, false)
	if f.calls.Load() != n {
		t.Fatal("breaker should still be open after 15s of a 20s backoff")
	}
	clk.Add(6 * time.Second)
	c.Check(`\\X\s\a.csv`, ref, false) // 3rd failure → capped at 35s
	srv := c.Servers()
	if len(srv) != 1 || srv[0].RetryAt.Sub(clk.Now()) != 35*time.Second {
		t.Fatalf("cap not applied: %+v", srv)
	}
}

func TestTimeout(t *testing.T) {
	f := newFake()
	f.addFile(`\\SLOW\s\a.csv`, ref, 1)
	f.hang["SLOW"] = true
	defer close(f.block)
	c := New(f, Options{Timeout: 50 * time.Millisecond})
	start := time.Now()
	r := c.Check(`\\SLOW\s\a.csv`, ref, false)
	if r.Kind != Unreachable || time.Since(start) > 2*time.Second {
		t.Fatalf("expected quick timeout, got %+v after %s", r, time.Since(start))
	}
	// Other servers are unaffected.
	f.addFile(`\\FAST\s\b.csv`, ref, 1)
	if r := c.Check(`\\FAST\s\b.csv`, ref, false); r.Kind != Found {
		t.Fatalf("%+v", r)
	}
}

func TestPermissionDenied(t *testing.T) {
	f := newFake()
	f.addFile(`\\EFS\Groups\Secret\a.csv`, ref, 1)
	f.deny[strings.ToLower(`\\EFS\Groups\Secret`)] = true
	c := New(f, Options{})
	r := c.Check(`\\EFS\Groups\Secret\a.csv`, ref, false)
	if r.Kind != Unreachable || r.ServerProblem || !strings.Contains(r.Reason, "Hozzáférés") {
		t.Fatalf("%+v", r)
	}
}

func TestPatternNewest(t *testing.T) {
	f := newFake()
	f.addFile(`\\EF-BI\exp\sales_20261006.parquet`, ref.Add(-48*time.Hour), 100)
	f.addFile(`\\EF-BI\exp\sales_20261007.parquet`, ref.Add(-24*time.Hour), 120)
	f.addFile(`\\EF-BI\exp\sales_20261008.parquet`, ref.Add(-time.Minute), 130)
	f.addFile(`\\EF-BI\exp\other.parquet`, ref, 1)
	c := New(f, Options{})
	r := c.Check(`\\EF-BI\exp\sales_*.parquet`, ref, false)
	if r.Kind != Found || r.File.Name != "sales_20261008.parquet" || r.Matches != 3 || len(r.Candidates) != 3 {
		t.Fatalf("%+v", r)
	}
	// Date token: today's file only.
	r = c.Check(`\\EF-BI\exp\sales_{yyyyMMdd}.parquet`, ref, false)
	if r.Kind != Found || r.File.Size != 130 {
		t.Fatalf("%+v", r)
	}
	r = c.Check(`\\EF-BI\exp\sales_{yyyyMMdd}.parquet`, ref.AddDate(0, 0, 1), false)
	if r.Kind != NotFound {
		t.Fatalf("tomorrow's file does not exist yet: %+v", r)
	}
	r = c.Check(`\\EF-BI\exp\sales_{yyyyMMdd}.parquet`, ref.AddDate(0, 0, 1), true)
	if r.Kind != Found || r.File.Size != 130 {
		t.Fatalf("any-date mode: %+v", r)
	}
	r = c.Check(`\\EF-BI\exp\nothing_*.csv`, ref, false)
	if r.Kind != NotFound || r.Matches != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestListingCache(t *testing.T) {
	f := newFake()
	f.addFile(`\\S\x\a_1.csv`, ref, 1)
	clk := &clock{t: ref}
	c := New(f, Options{Now: clk.Now})
	c.Check(`\\S\x\a_*.csv`, ref, false)
	n := f.calls.Load()
	c.Check(`\\S\x\*.csv`, ref, false)
	if f.calls.Load() != n {
		t.Fatal("second listing of the same folder within TTL should be cached")
	}
	clk.Add(10 * time.Second)
	c.Check(`\\S\x\*.csv`, ref, false)
	if f.calls.Load() == n {
		t.Fatal("cache should expire")
	}
}

func TestOSFS(t *testing.T) {
	dir := t.TempDir()
	p := dir + string(os.PathSeparator) + "riport_20261008.xlsx"
	os.WriteFile(p, []byte("hello"), 0o644)
	os.Mkdir(dir+string(os.PathSeparator)+"sub", 0o755)
	c := New(nil, Options{})
	r := c.Check(p, ref, false)
	if r.Kind != Found || r.File.Size != 5 {
		t.Fatalf("%+v", r)
	}
	pat := dir + string(os.PathSeparator) + "riport_{yyyyMMdd}.xlsx"
	if r := c.Check(pat, ref, false); r.Kind != Found {
		t.Fatalf("%+v", r)
	}
	if r := c.Check(dir+string(os.PathSeparator)+"nope.xlsx", ref, false); r.Kind != NotFound {
		t.Fatalf("%+v", r)
	}
	missing := dir + string(os.PathSeparator) + "nodir" + string(os.PathSeparator) + "x.csv"
	if r := c.Check(missing, ref, false); r.Kind != NotFound {
		t.Fatalf("missing local folder: %+v", r)
	}
}
