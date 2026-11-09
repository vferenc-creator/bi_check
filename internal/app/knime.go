package app

// The "KNIME" menu: running KNIME workflows headless (NodePit Batch),
// scheduled or on demand. It keeps its own files in the machine-local data
// folder (%LOCALAPPDATA%\EnergofishMonitor\knime: the runs and their logs
// belong to this computer and must not grow the roaming profile) and does
// not touch the file and e-mail monitoring.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bimonitor/internal/knime"
	"bimonitor/internal/secret"
)

type knimeState struct {
	once   sync.Once
	store  *knime.Store
	runner *knime.Runner
	err    error

	mu         sync.Mutex
	installing bool

	share knimeShareState // state of the runs in the shared folder
}

func (a *App) knimeDir() string { return filepath.Join(a.cacheDir(), "knime") }

// knimeInit opens the store and prepares the runner (once).
func (a *App) knimeInit() (*knime.Store, *knime.Runner, error) {
	k := &a.kn
	k.once.Do(func() {
		st, err := knime.OpenStore(a.knimeDir())
		if err != nil {
			k.err = fmt.Errorf("a KNIME adatai nem olvashatók (%s): %w", a.knimeDir(), err)
			return
		}
		k.store = st
		k.runner = &knime.Runner{
			Store:    st,
			Calendar: a.Calendar(),
			Location: a.Loc,
			Password: func(jobID, cred string) (string, error) { return secret.Load(st.SecretPath(jobID, cred)) },
			OnChange: func() { a.P.Push("knime", nil); a.knimeShareKick() },
			OnFinish: a.knimeFinished,
		}
	})
	return k.store, k.runner, k.err
}

// startKnime begins the scheduler of the KNIME runs.
func (a *App) startKnime(ctx context.Context) {
	if _, r, err := a.knimeInit(); err != nil {
		log.Printf("knime: %v", err)
	} else {
		r.Start(ctx)
		go a.knimeShareLoop(ctx)
	}
}

// stopKnime stops the running KNIME processes at program exit and waits for
// them (so no KNIME is left running and the temporary workspaces go).
func (a *App) stopKnime() {
	if a.kn.runner != nil {
		a.kn.runner.Stop(30 * time.Second)
	}
}

func (a *App) knimeFinished(run knime.Run, j knime.Job) {
	try := ""
	if run.Attempts > 1 {
		try = fmt.Sprintf(" [%d/%d. próbálkozás]", run.Attempt, run.Attempts)
	}
	log.Printf("knime: %s – %s (%s)%s %s", j.Name, run.Status, run.Trigger, try, firstLine(run.Message))
	if !j.Notify || !run.NextRetry.IsZero() { // a retry follows: only the final failure notifies
		return
	}
	switch run.Status {
	case knime.StatusError, knime.StatusTimeout, knime.StatusMissed:
		msg := firstLine(run.Message)
		if run.Attempt > 1 {
			msg = fmt.Sprintf("%d próbálkozás után sem sikerült. %s", run.Attempt, msg)
		}
		a.P.Notify("KNIME: "+j.Name, msg, NotifyError)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---- API ---------------------------------------------------------------------

type knimeJobView struct {
	Job   knime.Job    `json:"job"`
	Last  *knime.Run   `json:"last,omitempty"`
	Live  *knime.Run   `json:"live,omitempty"` // queued or running
	Next  *time.Time   `json:"next,omitempty"`
	Retry *knime.Retry `json:"retry,omitempty"` // pending new try after a failure
	Text  string       `json:"scheduleText"`
}

type knimeView struct {
	Config     knime.Config   `json:"config"`
	Install    knime.Install  `json:"install"`
	ZipURL     string         `json:"zipUrl"` // download address for this KNIME version
	Jobs       []knimeJobView `json:"jobs"`
	Runs       []knime.Run    `json:"runs"`
	Installing bool           `json:"installing"`
	Dir        string         `json:"dir"`
	// the shared folder: our publishing and the other computers' runs
	Share  knimeShareView    `json:"share"`
	Remote []knimeRemoteView `json:"remote"`
}

type knimeJobSave struct {
	Job knime.Job `json:"job"`
	// Passwords: credential name → new password ("" = keep the stored one).
	Passwords map[string]string `json:"passwords"`
	// Clear: credential names whose stored password is removed.
	Clear []string `json:"clear"`
}

type knimeInstallParam struct {
	Zip      string `json:"zip"`      // a zip chosen on this computer
	Download bool   `json:"download"` // download the zip for this KNIME version
	Force    bool   `json:"force"`    // install even if the zip is for another KNIME version
}

type knimeInstallResult struct {
	OK      bool          `json:"ok"`
	Message string        `json:"message"`
	Output  string        `json:"output"`
	Install knime.Install `json:"install"`
	// Mismatch: the zip is for another KNIME version (asks to confirm).
	Mismatch string `json:"mismatch,omitempty"`
}

type knimeZipResult struct {
	Path  string        `json:"path"`
	Info  knime.ZipInfo `json:"info"`
	Error string        `json:"error,omitempty"`
}

func (a *App) knimeView() (knimeView, error) {
	st, r, err := a.knimeInit()
	if err != nil {
		return knimeView{}, err
	}
	cfg := st.Config()
	v := knimeView{Config: cfg, Jobs: []knimeJobView{}, Dir: st.Dir()}
	if cfg.Exe != "" {
		v.Install = knime.Inspect(cfg.Exe)
		if m := v.Install.Minor(); m != "" {
			v.ZipURL = knime.ZipURL(m)
		}
	}
	live := map[string]knime.Run{}
	for _, x := range r.Live() {
		live[x.JobID] = x
	}
	for _, j := range st.Jobs() {
		jv := knimeJobView{Job: j, Text: "csak kézi indítás"}
		if j.Scheduled {
			jv.Text = j.Schedule.Describe()
			if !j.Enabled {
				jv.Text += " (szünetel)"
			}
		}
		if x, ok := st.LastRun(j.ID); ok {
			jv.Last = &x
		}
		if x, ok := live[j.ID]; ok {
			jv.Live = &x
		}
		if t := r.Next(j); !t.IsZero() {
			jv.Next = &t
		}
		if rt, ok := st.PendingRetry(j.ID); ok {
			jv.Retry = &rt
		}
		v.Jobs = append(v.Jobs, jv)
	}
	v.Runs = st.Runs("", 200)
	v.Share, v.Remote = a.knimeShareViews()
	a.kn.mu.Lock()
	v.Installing = a.kn.installing
	a.kn.mu.Unlock()
	return v, nil
}

func (a *App) registerKnimeAPI() {
	a.register("knimeState", func() (knimeView, error) { return a.knimeView() })

	a.register("knimeSetConfig", func(c knime.Config) (knimeView, error) {
		st, _, err := a.knimeInit()
		if err != nil {
			return knimeView{}, err
		}
		c.Exe = strings.TrimSpace(strings.Trim(strings.TrimSpace(c.Exe), `"`))
		c.Preferences = strings.TrimSpace(strings.Trim(strings.TrimSpace(c.Preferences), `"`))
		if c.Preferences != "" {
			if st, err := os.Stat(c.Preferences); err != nil || st.IsDir() {
				return knimeView{}, errors.New("a beállításfájl nem található: " + c.Preferences)
			}
		}
		if c.Exe != "" {
			in := knime.Inspect(c.Exe)
			if in.Error != "" {
				return knimeView{}, errors.New(in.Error)
			}
			c.Exe = in.Exe
		}
		if _, err := st.SetConfig(c); err != nil {
			return knimeView{}, err
		}
		a.knimeShareKick()
		return a.knimeView()
	})

	// Looks for KNIME in the usual places and saves the first one found.
	a.register("knimeDetect", func() (knimeView, error) {
		st, _, err := a.knimeInit()
		if err != nil {
			return knimeView{}, err
		}
		in, ok := knime.Detect()
		if !ok {
			return knimeView{}, errors.New("nem találtam KNIME-ot a szokásos helyeken – adja meg a knime.exe helyét a Tallózás gombbal")
		}
		c := st.Config()
		c.Exe = in.Exe
		if _, err := st.SetConfig(c); err != nil {
			return knimeView{}, err
		}
		a.knimeShareKick()
		return a.knimeView()
	})

	a.register("knimeBrowseExe", func(current string) (BrowseResult, error) {
		dir := ""
		if current != "" {
			dir = filepath.Dir(current)
		}
		p, ok := a.P.OpenFileDialog("A KNIME programfájlja (knime.exe)", dir, []FileFilter{{"KNIME (knime.exe)", "knime.exe"}, {"Programok (*.exe)", "*.exe"}})
		if !ok {
			return BrowseResult{}, nil
		}
		return BrowseResult{Path: p}, nil
	})

	a.register("knimeBrowsePrefs", func() (BrowseResult, error) {
		p, ok := a.P.OpenFileDialog("KNIME beállításfájl (File → Export Preferences)", "", []FileFilter{{"KNIME beállítások (*.epf)", "*.epf"}})
		if !ok {
			return BrowseResult{}, nil
		}
		return BrowseResult{Path: p}, nil
	})

	a.register("knimeBrowseZip", func() (knimeZipResult, error) {
		p, ok := a.P.OpenFileDialog("NodePit Batch bővítmény (zip)", "", []FileFilter{{"Zip-fájlok (*.zip)", "*.zip"}})
		if !ok {
			return knimeZipResult{}, nil
		}
		zi, err := knime.InspectZip(p)
		out := knimeZipResult{Path: p, Info: zi}
		if err != nil {
			out.Error = err.Error()
		}
		return out, nil
	})

	a.register("knimeInstall", a.knimeInstall)

	a.register("knimeBrowseWorkflow", func(p struct {
		File    bool   `json:"file"`
		Current string `json:"current"`
	}) (BrowseResult, error) {
		cur := strings.TrimSpace(p.Current)
		if p.File {
			dir := ""
			if cur != "" {
				dir = filepath.Dir(cur)
			}
			f, ok := a.P.OpenFileDialog("KNIME workflow fájl", dir, []FileFilter{{"KNIME workflow (*.knwf, *.knar)", "*.knwf;*.knar"}})
			if !ok {
				return BrowseResult{}, nil
			}
			return BrowseResult{Path: f}, nil
		}
		d, ok := a.P.FolderDialog("A KNIME workflow mappája (benne: workflow.knime)", cur)
		if !ok {
			return BrowseResult{}, nil
		}
		return BrowseResult{Path: d}, nil
	})

	a.register("knimeSaveJob", func(p knimeJobSave) (knime.Job, error) {
		st, _, err := a.knimeInit()
		if err != nil {
			return knime.Job{}, err
		}
		j := p.Job
		old, existed := st.Job(j.ID)
		if j.ID == "" || !existed {
			j.ID = knime.NewID()
		}
		if err := knime.Validate(&j); err != nil {
			return j, err
		}
		had := map[string]bool{}
		if existed {
			for _, c := range old.Credentials {
				had[c.Name] = c.HasPassword
			}
		}
		clear := map[string]bool{}
		for _, n := range p.Clear {
			clear[n] = true
		}
		keep := map[string]bool{}
		for i := range j.Credentials {
			c := &j.Credentials[i]
			keep[c.Name] = true
			path := st.SecretPath(j.ID, c.Name)
			switch pw := p.Passwords[c.Name]; {
			case pw != "":
				if err := secret.Save(path, pw); err != nil {
					return j, fmt.Errorf("a jelszó mentése nem sikerült: %w", err)
				}
				c.HasPassword = true
			case clear[c.Name]:
				_ = secret.Save(path, "")
				c.HasPassword = false
			default:
				c.HasPassword = had[c.Name]
			}
		}
		if existed {
			for _, c := range old.Credentials {
				if !keep[c.Name] {
					_ = secret.Save(st.SecretPath(j.ID, c.Name), "")
				}
			}
		}
		saved, err := st.SaveJob(j, time.Now())
		if err == nil {
			a.P.Push("knime", nil)
			a.knimeShareKick()
		}
		return saved, err
	})

	a.register("knimeDeleteJob", func(p idParam) error {
		st, r, err := a.knimeInit()
		if err != nil {
			return err
		}
		if run, busy := r.Busy(p.ID); busy {
			r.Cancel(run.ID)
		}
		if err := st.DeleteJob(p.ID); err != nil {
			return err
		}
		a.P.Push("knime", nil)
		a.knimeShareKick()
		return nil
	})

	a.register("knimeRunNow", func(p idParam) (knime.Run, error) {
		st, r, err := a.knimeInit()
		if err != nil {
			return knime.Run{}, err
		}
		j, ok := st.Job(p.ID)
		if !ok {
			return knime.Run{}, errors.New("ez a futtatás már nem létezik")
		}
		if st.Config().Exe == "" {
			return knime.Run{}, errors.New("előbb adja meg a KNIME helyét (KNIME és NodePit Batch kártya)")
		}
		return r.Enqueue(j, "kézi", time.Time{})
	})

	a.register("knimeCancel", func(p idParam) (bool, error) {
		_, r, err := a.knimeInit()
		if err != nil {
			return false, err
		}
		return r.Cancel(p.ID), nil
	})

	// Drops the pending retry of a job (p.ID = job ID).
	a.register("knimeCancelRetry", func(p idParam) (bool, error) {
		_, r, err := a.knimeInit()
		if err != nil {
			return false, err
		}
		return r.CancelRetry(p.ID), nil
	})

	a.register("knimeLog", func(p idParam) (string, error) {
		st, _, err := a.knimeInit()
		if err != nil {
			return "", err
		}
		run, ok := st.Run(p.ID)
		if !ok || run.Log == "" {
			return "", errors.New("ehhez a futáshoz nincs napló")
		}
		b, err := os.ReadFile(st.LogPath(run.Log))
		if err != nil {
			return "", fmt.Errorf("a napló nem olvasható: %w", err)
		}
		if len(b) > 400<<10 {
			b = append([]byte("… (a napló eleje lemaradt)\n"), b[len(b)-400<<10:]...)
		}
		return string(b), nil
	})

	a.register("knimeOpenLog", func(p idParam) error {
		st, _, err := a.knimeInit()
		if err != nil {
			return err
		}
		run, ok := st.Run(p.ID)
		if !ok || run.Log == "" {
			return errors.New("ehhez a futáshoz nincs napló")
		}
		return a.P.ShellOpen(st.LogPath(run.Log))
	})
}

// knimeInstall installs (or updates) NodePit Batch in the configured KNIME
// from a zip on this computer or downloaded for the KNIME version.
func (a *App) knimeInstall(p knimeInstallParam) (knimeInstallResult, error) {
	st, _, err := a.knimeInit()
	if err != nil {
		return knimeInstallResult{}, err
	}
	a.kn.mu.Lock()
	if a.kn.installing {
		a.kn.mu.Unlock()
		return knimeInstallResult{}, errors.New("a telepítés már folyamatban van")
	}
	a.kn.installing = true
	a.kn.mu.Unlock()
	defer func() {
		a.kn.mu.Lock()
		a.kn.installing = false
		a.kn.mu.Unlock()
		a.P.Push("knime", nil)
	}()
	a.P.Push("knime", nil)

	cfg := st.Config()
	in := knime.Inspect(cfg.Exe)
	if cfg.Exe == "" || in.Error != "" {
		return knimeInstallResult{}, errors.New("előbb adja meg a KNIME helyét (KNIME keresése vagy Tallózás)")
	}
	res := knimeInstallResult{Install: in}
	if !in.Writable {
		res.Message = "A KNIME mappájába (" + in.Dir + ") ez a felhasználó nem írhat, ezért a bővítmény nem telepíthető. " +
			"Indítsa a BI Monitort erre a lépésre rendszergazdaként (jobb klikk → Futtatás rendszergazdaként), vagy telepítse a KNIME-ot egy saját mappába (pl. C:\\KNIME)."
		return res, nil
	}
	zipPath := strings.TrimSpace(p.Zip)
	var zi knime.ZipInfo
	if p.Download {
		minor := in.Minor()
		if minor == "" {
			return res, errors.New("a KNIME verziója nem állapítható meg, ezért nem tudom, melyik zipet töltsem le – válassza ki a zipet a gépről")
		}
		url := knime.ZipURL(minor)
		zipPath = filepath.Join(st.Dir(), "letoltesek", minor+".zip")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		zi, err = knime.Download(ctx, url, zipPath)
		cancel()
		if err != nil {
			res.Message = err.Error() + " Letöltheti böngészőből is (" + url + "), majd válassza a „Zip a gépről” lehetőséget."
			return res, nil
		}
	} else {
		if zipPath == "" {
			return res, errors.New("válassza ki a zipet")
		}
		if zi, err = knime.InspectZip(zipPath); err != nil {
			res.Message = err.Error()
			return res, nil
		}
		if zi.ForKNIME != "" && in.Minor() != "" && zi.ForKNIME != in.Minor() && !p.Force {
			res.Mismatch = fmt.Sprintf("A zip neve szerint a KNIME %s-hoz készült, a telepített KNIME %s.", zi.ForKNIME, in.Version)
			return res, nil
		}
	}

	// no new run starts while the extension is (re)installed
	_, runner, _ := a.knimeInit()
	runner.Hold(true)
	defer runner.Hold(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var out strings.Builder
	step := func(title string, args []string) int {
		code, o, err := knime.RunCommand(ctx, in.Exe, args)
		fmt.Fprintf(&out, "== %s – kilépési kód %d\n%s\n", title, code, o)
		if err != nil {
			fmt.Fprintf(&out, "%v\n", err)
		}
		return code
	}
	repo := knime.RepoURL(zipPath)
	code := step("NodePit Batch "+zi.Version+" telepítése", knime.DirectorArgs(repo, false))
	now := knime.Inspect(in.Exe)
	if in.NodePit != "" && now.NodePit != zi.Version && ctx.Err() == nil {
		// update that the installer refused over the old version: remove
		// the old one, then install again
		step("Régi NodePit Batch ("+in.NodePit+") eltávolítása", knime.DirectorArgs("", true))
		code = step("NodePit Batch "+zi.Version+" telepítése (újra)", knime.DirectorArgs(repo, false))
		now = knime.Inspect(in.Exe)
	}
	res.Output = out.String()
	res.Install = now
	switch {
	case now.NodePit != "" && (zi.Version == "" || now.NodePit == zi.Version):
		res.OK = true
		res.Message = "A NodePit Batch " + now.NodePit + " telepítve a KNIME " + now.Version + "-ba. Most már felvehet futtatásokat."
	case now.NodePit != "":
		res.Message = "A NodePit Batch " + now.NodePit + " maradt telepítve, a(z) " + zi.Version + " nem került fel. Zárja be a KNIME-ot, és próbálja újra. Részletek lent."
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Message = "A telepítés 10 perc alatt sem fejeződött be – a NodePit Batch most nincs telepítve, a futtatások addig nem indulnak."
	case in.NodePit != "":
		res.Message = fmt.Sprintf("A frissítés nem sikerült (kilépési kód: %d), és a NodePit Batch most nincs telepítve – a futtatások addig nem indulnak. Zárja be a KNIME-ot, és telepítse újra. Részletek lent.", code)
	default:
		res.Message = fmt.Sprintf("A telepítés nem sikerült (kilépési kód: %d). Fut a KNIME? Zárja be, és próbálja újra. Részletek lent.", code)
	}
	log.Printf("knime: NodePit Batch telepítés – %s", res.Message)
	return res, nil
}
