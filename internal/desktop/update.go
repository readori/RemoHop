package desktop

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/nowindow"
	"github.com/readori/RemoHop/internal/update"
)

// Checking for a newer release: once shortly after start, then once a day
// while the app runs. Only the version file is read; the app tells the
// user and opens the download page (or names the package manager's
// command), it never installs anything by itself.
const (
	updateDelay = 30 * time.Second
	updateEvery = 24 * time.Hour
	// NoUpdateCheckEnv switches the check off for managed installations,
	// whatever the setting says.
	NoUpdateCheckEnv = "REMOHOP_NO_UPDATE_CHECK"
)

// UpdateState is the update check as the page shows it.
type UpdateState struct {
	// Auto: the app checks by itself (the setting).
	Auto bool `json:"auto"`
	// Managed: switched off by REMOHOP_NO_UPDATE_CHECK.
	Managed  bool   `json:"managed"`
	Checking bool   `json:"checking"`
	Checked  int64  `json:"checked,omitempty"` // Unix milliseconds of the last answer
	Current  string `json:"current"`
	Latest   string `json:"latest,omitempty"`
	// Available: Latest is newer than this copy.
	Available bool `json:"available"`
	// Skipped: the user chose to skip Latest.
	Skipped bool          `json:"skipped"`
	Error   string        `json:"error,omitempty"`
	Method  update.Method `json:"method"`
}

type updateInfo struct {
	current  string // this copy's version (cli.Version; tests change it)
	checking bool
	checked  time.Time
	latest   string
	err      string
	method   update.Method
}

func detectMethod() update.Method {
	exe, err := os.Executable()
	if err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
	}
	return update.Detect(update.Env{GOOS: runtime.GOOS, Exe: exe, Getenv: os.Getenv, Exists: func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}})
}

func (a *App) updateManaged() bool { return os.Getenv(NoUpdateCheckEnv) != "" }

// updateAuto reports whether the app checks by itself.
func (a *App) updateAuto() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.updateManaged() && (a.settings.UpdateCheck == nil || *a.settings.UpdateCheck)
}

func (a *App) updateState() UpdateState {
	a.mu.Lock()
	u, skip := a.upd, a.settings.SkipVersion
	auto := a.settings.UpdateCheck == nil || *a.settings.UpdateCheck
	a.mu.Unlock()
	st := UpdateState{Auto: auto, Managed: a.updateManaged(), Checking: u.checking, Current: u.current,
		Latest: u.latest, Error: u.err, Method: u.method}
	if !u.checked.IsZero() {
		st.Checked = u.checked.UnixMilli()
	}
	st.Available = u.latest != "" && update.Newer(u.latest, u.current)
	st.Skipped = st.Available && skip == u.latest
	return st
}

// updateLoop checks after a short delay and then once a day. It looks
// every hour whether a day has passed, so a computer that slept through
// the day still checks soon after it wakes.
func (a *App) updateLoop() {
	delay, every := a.cfg.UpdateDelay, a.cfg.UpdateEvery
	if delay <= 0 {
		delay = updateDelay
	}
	if every <= 0 {
		every = updateEvery
	}
	poll := min(time.Hour, every)
	t := time.NewTimer(delay)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		last := a.upd.checked
		a.mu.Unlock()
		if a.updateAuto() && (last.IsZero() || a.cfg.Now().Sub(last) >= every) {
			a.CheckUpdate(a.ctx)
		}
		t.Reset(poll)
	}
}

// CheckUpdate reads the version file now. The outcome is in the state.
func (a *App) CheckUpdate(ctx context.Context) {
	a.mu.Lock()
	if a.upd.checking {
		a.mu.Unlock()
		return
	}
	a.upd.checking = true
	a.mu.Unlock()
	a.wake()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := a.cfg.UpdateClient
	if client == nil {
		client = &http.Client{}
	}
	r, err := update.Fetch(ctx, client, a.cfg.UpdateURL)

	a.mu.Lock()
	a.upd.checking = false
	if err != nil {
		a.upd.err = "check_failed"
		a.mu.Unlock()
		a.log.Info("update check failed", "err", err)
		a.wake()
		return
	}
	was, current := a.upd.latest, a.upd.current
	a.upd.err, a.upd.latest, a.upd.checked = "", r.Version, a.cfg.Now()
	a.mu.Unlock()
	if r.Version != was && update.Newer(r.Version, current) {
		a.log.Info("a newer release is available", "current", current, "latest", r.Version)
	}
	a.wake()
}

// SetUpdateAuto switches the daily check on or off.
func (a *App) SetUpdateAuto(on bool) error {
	return a.update(func(s *Settings) {
		if on {
			s.UpdateCheck = nil // the default
		} else {
			off := false
			s.UpdateCheck = &off
		}
	})
}

// SkipUpdate stops showing the notice for version (the next one shows
// again). An empty version shows it again.
func (a *App) SkipUpdate(version string) error {
	if version != "" {
		if v, ok := update.ParseVersion(version); !ok || v.Pre != "" {
			return Errorf("bad_request", "not a release version")
		}
	}
	return a.update(func(s *Settings) { s.SkipVersion = version })
}

// UpdateLink is the page to open for target: "download" (the website's
// download page in lang) or "notes" (the latest release's page).
func (a *App) UpdateLink(target, lang string) (string, error) {
	switch target {
	case "download":
		return update.DownloadURL(normLang(lang)), nil
	case "notes":
		a.mu.Lock()
		latest := a.upd.latest
		a.mu.Unlock()
		if latest == "" {
			return "", Errorf("bad_request", "no release known yet")
		}
		return update.Release{Version: latest}.NotesURL(), nil
	}
	return "", Errorf("bad_request", "unknown page")
}

// openURL opens an https address in the default browser.
func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("/usr/bin/open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := nowindow.Set(cmd).Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
