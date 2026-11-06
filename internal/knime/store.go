package knime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/schedule"
)

// Variable is a workflow variable passed to the run.
type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Type  string `json:"type"` // String, int, double
}

// Credential fills a workflow credential (e.g. of a DB Connector). The
// password is kept apart, encrypted for the Windows user (secret package).
type Credential struct {
	Name        string `json:"name"`
	Login       string `json:"login"`
	HasPassword bool   `json:"hasPassword"`
}

// Job is one workflow with its parameters and schedule.
type Job struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Workflow    string        `json:"workflow"` // folder of the workflow, or a .knwf / .knar file
	Variables   []Variable    `json:"variables"`
	Credentials []Credential  `json:"credentials"`
	Reset       bool          `json:"reset"`      // reset before execution
	Save        bool          `json:"save"`       // save the workflow after execution
	Copy        bool          `json:"copy"`       // run a copy (works while the workflow is open in KNIME)
	TimeoutMin  int           `json:"timeoutMin"` // 0 = no limit
	Scheduled   bool          `json:"scheduled"`  // false = manual runs only
	Schedule    schedule.Spec `json:"schedule"`
	Enabled     bool          `json:"enabled"` // scheduled runs on
	Notify      bool          `json:"notify"`  // desktop notification on failure
	Note        string        `json:"note"`
	ExtraArgs   string        `json:"extraArgs"` // further NodePit options (advanced)
	Created     time.Time     `json:"created"`
	Updated     time.Time     `json:"updated"`
}

// Run statuses.
const (
	StatusQueued   = "queued"
	StatusRunning  = "running"
	StatusOK       = "ok"
	StatusError    = "error"
	StatusTimeout  = "timeout"
	StatusCanceled = "canceled"
	StatusMissed   = "missed"  // the program was not running at the scheduled time
	StatusSkipped  = "skipped" // the previous run of the job was still going
)

// Run is one execution of a job.
type Run struct {
	ID         string    `json:"id"`
	JobID      string    `json:"jobId"`
	JobName    string    `json:"jobName"`
	Trigger    string    `json:"trigger"` // "kézi" or "ütemezett"
	Due        time.Time `json:"due,omitempty"`
	Queued     time.Time `json:"queued"`
	Started    time.Time `json:"started,omitempty"`
	Ended      time.Time `json:"ended,omitempty"`
	Status     string    `json:"status"`
	ExitCode   *int      `json:"exitCode,omitempty"`
	Message    string    `json:"message"`
	DurationMs int64     `json:"durationMs"`
	Log        string    `json:"log,omitempty"` // file name in the logs folder
}

// Config of the KNIME module.
type Config struct {
	Exe        string `json:"exe"`
	Parallel   int    `json:"parallel"`   // runs at the same time (1–4)
	ConsoleLog bool   `json:"consoleLog"` // pass -consoleLog (output also on stdout)
	KeepRuns   int    `json:"keepRuns"`   // runs kept in the history
}

func (c *Config) normalize() {
	if c.Parallel < 1 {
		c.Parallel = 1
	}
	if c.Parallel > 4 {
		c.Parallel = 4
	}
	if c.KeepRuns < 50 || c.KeepRuns > 5000 {
		c.KeepRuns = 1000
	}
}

type jobsFile struct {
	Config Config               `json:"config"`
	Jobs   []Job                `json:"jobs"`
	Last   map[string]time.Time `json:"last"` // job ID → last scheduled instant handled
}

// Store keeps the jobs (jobs.json) and the run history (runs.json) in the
// KNIME folder of the application data, apart from the monitoring settings.
type Store struct {
	dir string
	mu  sync.Mutex
	f   jobsFile
	run []Run // newest first
}

// OpenStore loads (or creates) the store in dir.
func OpenStore(dir string) (*Store, error) {
	// KNIME runs in its own folder: every path handed to it must be absolute
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if err := os.MkdirAll(filepath.Join(dir, "naplok"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, f: jobsFile{Last: map[string]time.Time{}}}
	if err := readJSON(filepath.Join(dir, "jobs.json"), &s.f); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(dir, "runs.json"), &s.run); err != nil {
		return nil, err
	}
	if s.f.Last == nil {
		s.f.Last = map[string]time.Time{}
	}
	if s.f.Jobs == nil {
		s.f.Jobs = []Job{}
	}
	s.f.Config.normalize()
	// runs that were going when the program stopped
	for i := range s.run {
		if r := &s.run[i]; r.Status == StatusRunning || r.Status == StatusQueued {
			r.Status, r.Message = StatusCanceled, "A program leállt a futás közben."
			if r.Ended.IsZero() {
				r.Ended = r.Started
			}
		}
	}
	return s, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		bad := path + ".hibas-" + time.Now().Format("20060102-150405")
		_ = os.Rename(path, bad)
		return nil // start empty, keep the damaged file aside
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Dir is the folder of the store.
func (s *Store) Dir() string { return s.dir }

// LogPath is the full path of a run's log file.
func (s *Store) LogPath(name string) string {
	return filepath.Join(s.dir, "naplok", filepath.Base(name))
}

// SecretPath is where a credential password of a job is kept.
func (s *Store) SecretPath(jobID, cred string) string {
	return filepath.Join(s.dir, "jelszavak", jobID+"-"+hex.EncodeToString([]byte(cred))+".bin")
}

func (s *Store) saveJobs() error { return writeJSON(filepath.Join(s.dir, "jobs.json"), s.f) }
func (s *Store) saveRuns() error { return writeJSON(filepath.Join(s.dir, "runs.json"), s.run) }

// Config returns the module settings.
func (s *Store) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Config
}

// SetConfig saves the module settings.
func (s *Store) SetConfig(c Config) (Config, error) {
	c.normalize()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Config = c
	return c, s.saveJobs()
}

// Jobs returns a copy of the jobs, by name.
func (s *Store) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Job(nil), s.f.Jobs...)
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Job returns one job.
func (s *Store) Job(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.f.Jobs {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// NewID makes a short random id.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Validate checks a job before saving.
func Validate(j *Job) error {
	j.Name = strings.TrimSpace(j.Name)
	j.Workflow = strings.TrimSpace(strings.Trim(strings.TrimSpace(j.Workflow), `"`))
	if j.Name == "" {
		return errors.New("adjon nevet a futtatásnak")
	}
	if j.Workflow == "" {
		return errors.New("adja meg a workflow mappáját (vagy .knwf fájlját)")
	}
	if j.TimeoutMin < 0 || j.TimeoutMin > 24*60 {
		return errors.New("az időkorlát 0 és 1440 perc között lehet (0 = nincs)")
	}
	vs := j.Variables[:0]
	for _, v := range j.Variables {
		v.Name = strings.TrimSpace(v.Name)
		if v.Name == "" {
			continue
		}
		switch strings.ToLower(v.Type) {
		case "", "string":
			v.Type = "String"
		case "int":
			v.Type = "int"
			if _, err := fmt.Sscan(v.Value, new(int)); err != nil {
				return fmt.Errorf("a(z) %q változó egész számot vár (int): %q", v.Name, v.Value)
			}
		case "double":
			v.Type = "double"
			if _, err := fmt.Sscan(strings.Replace(v.Value, ",", ".", 1), new(float64)); err != nil {
				return fmt.Errorf("a(z) %q változó számot vár (double): %q", v.Name, v.Value)
			}
			v.Value = strings.Replace(v.Value, ",", ".", 1)
		default:
			return fmt.Errorf("a(z) %q változó típusa String, int vagy double lehet", v.Name)
		}
		if strings.Contains(v.Name, ";") {
			return fmt.Errorf("a változó nevében nem lehet pontosvessző: %q", v.Name)
		}
		vs = append(vs, v)
	}
	j.Variables = vs
	cs := j.Credentials[:0]
	for _, c := range j.Credentials {
		c.Name, c.Login = strings.TrimSpace(c.Name), strings.TrimSpace(c.Login)
		if c.Name == "" {
			continue
		}
		if strings.Contains(c.Name, ";") || strings.Contains(c.Login, ";") {
			return fmt.Errorf("a hitelesítés nevében és felhasználójában nem lehet pontosvessző: %q", c.Name)
		}
		cs = append(cs, c)
	}
	j.Credentials = cs
	if j.Scheduled {
		if _, err := schedule.Compile(j.Schedule, nil, nil); err != nil {
			return fmt.Errorf("ütemezés: %w", err)
		}
	}
	return nil
}

// SaveJob adds or replaces a job. A changed schedule starts from now (no
// catching up of past instants).
func (s *Store) SaveJob(j Job, now time.Time) (Job, error) {
	if err := Validate(&j); err != nil {
		return j, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j.Updated = now
	for i, old := range s.f.Jobs {
		if old.ID == j.ID {
			j.Created = old.Created
			s.f.Jobs[i] = j
			if fmt.Sprint(old.Schedule) != fmt.Sprint(j.Schedule) || old.Scheduled != j.Scheduled || old.Enabled != j.Enabled {
				delete(s.f.Last, j.ID)
			}
			return j, s.saveJobs()
		}
	}
	if j.ID == "" {
		j.ID = NewID()
	}
	j.Created = now
	s.f.Jobs = append(s.f.Jobs, j)
	return j, s.saveJobs()
}

// DeleteJob removes a job (its runs stay in the history).
func (s *Store) DeleteJob(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.f.Jobs[:0]
	var gone *Job
	for _, j := range s.f.Jobs {
		if j.ID == id {
			j := j
			gone = &j
			continue
		}
		out = append(out, j)
	}
	s.f.Jobs = out
	delete(s.f.Last, id)
	if gone != nil {
		for _, c := range gone.Credentials {
			_ = os.Remove(s.SecretPath(id, c.Name))
		}
	}
	return s.saveJobs()
}

// LastDue is the last scheduled instant handled for a job (zero = none).
func (s *Store) LastDue(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Last[id]
}

// SetLastDue records the last scheduled instant handled.
func (s *Store) SetLastDue(id string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f.Last[id] = t
	return s.saveJobs()
}

// PutRun adds or updates a run in the history.
func (s *Store) PutRun(r Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.run {
		if s.run[i].ID == r.ID {
			s.run[i] = r
			return s.saveRuns()
		}
	}
	s.run = append([]Run{r}, s.run...)
	if max := s.f.Config.KeepRuns; len(s.run) > max {
		for _, old := range s.run[max:] {
			if old.Log != "" {
				_ = os.Remove(s.LogPath(old.Log))
			}
		}
		s.run = s.run[:max]
	}
	return s.saveRuns()
}

// Runs returns the newest runs (of one job if jobID != "").
func (s *Store) Runs(jobID string, limit int) []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Run{}
	for _, r := range s.run {
		if jobID == "" || r.JobID == jobID {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// Run returns one run.
func (s *Store) Run(id string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.run {
		if r.ID == id {
			return r, true
		}
	}
	return Run{}, false
}

// LastRun is the newest finished (or running) run of a job.
func (s *Store) LastRun(jobID string) (Run, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.run {
		if r.JobID == jobID && r.Status != StatusQueued {
			return r, true
		}
	}
	return Run{}, false
}
