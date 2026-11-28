package desktop

import (
	"context"
	"crypto/ed25519"
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
	// CanInstall: "Upgrade now" can replace this copy (a download from
	// the website, in a place the user can write, with trusted update
	// keys in this build).
	CanInstall bool `json:"can_install"`
	// Stage is the upgrade under way: verifying, downloading (Percent),
	// installing, restarting; InstallError the last one's failure.
	Stage        string `json:"stage,omitempty"`
	Percent      int    `json:"percent"`
	InstallError string `json:"install_error,omitempty"`
}

type updateInfo struct {
	current  string // this copy's version (cli.Version; tests change it)
	checking bool
	checked  time.Time
	latest   string
	err      string
	method   update.Method
	plan     update.Plan
	planOK   bool
	stage    string
	percent  int
	instErr  string
}

func executable() string {
	exe, err := os.Executable()
	if err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
	}
	return exe
}

func detectMethod() update.Method {
	return update.Detect(update.Env{GOOS: runtime.GOOS, Exe: executable(), Getenv: os.Getenv, Exists: func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}})
}

// newUpdateInfo works out how this copy is upgraded.
func newUpdateInfo(current string) updateInfo {
	u := updateInfo{current: current, method: detectMethod()}
	if u.method.Kind == update.KindDownload {
		// Writable creates a file next to the program: once, here, not at
		// every state the page is sent.
		if p, err := update.PlanFor(runtime.GOOS, runtime.GOARCH, executable()); err == nil && p.Writable() {
			u.plan, u.planOK = p, true
		}
	}
	return u
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
	st.Stage, st.Percent, st.InstallError = u.stage, u.percent, u.instErr
	st.CanInstall = st.Available && u.planOK && len(a.updateKeys()) > 0
	return st
}

// updateKeys are the keys a release must be signed with (a test seam).
func (a *App) updateKeys() []ed25519.PublicKey {
	if a.cfg.UpdateKeys != nil {
		return a.cfg.UpdateKeys
	}
	return update.Keys()
}

// InstallUpdate upgrades this copy to the latest release: downloads its
// archive, checks it against the release's signed SHA256SUMS (and on macOS
// the app's code signature), swaps the program, starts the new one and
// calls done, which quits this one. It refuses while anyone is connected
// to this computer. Progress and failure are in the state.
func (a *App) InstallUpdate(done func()) error {
	st := a.updateState()
	if !st.CanInstall {
		return Errorf("bad_request", "this copy cannot upgrade itself; download the new version")
	}
	if st.Stage != "" {
		return Errorf("busy", "an upgrade is already under way")
	}
	if h, err := a.running(); err == nil && len(h.Sessions()) > 0 {
		return Errorf("busy_sessions", "someone is connected to this computer; disconnect first")
	}
	a.mu.Lock()
	plan, tag := a.upd.plan, a.upd.latest
	a.upd.stage, a.upd.percent, a.upd.instErr = "verifying", -1, ""
	a.mu.Unlock()
	a.wake()
	go func() {
		err := a.installUpdate(plan, tag)
		a.mu.Lock()
		if err != nil {
			a.upd.stage, a.upd.instErr = "", err.Error()
		} else {
			a.upd.stage = "restarting"
		}
		a.mu.Unlock()
		a.wake()
		if err != nil {
			a.log.Warn("upgrade failed", "to", tag, "err", err)
			return
		}
		a.log.Info("upgraded; starting the new version", "to", tag)
		if done != nil {
			done()
		}
	}()
	return nil
}

func (a *App) installUpdate(plan update.Plan, tag string) error {
	f := update.Fetcher{Client: a.cfg.UpdateClient, Base: a.cfg.UpdateBase}
	staged, err := update.Download(a.ctx, f, a.updateKeys(), tag, plan, func(stage string, pct int) {
		a.mu.Lock()
		a.upd.stage, a.upd.percent = stage, pct
		a.mu.Unlock()
		a.wake()
	})
	if err != nil {
		return err
	}
	if plan.Bundle {
		if err := update.VerifyBundle(staged); err != nil {
			os.RemoveAll(staged)
			return err
		}
	}
	if err := update.Install(plan, staged); err != nil {
		os.RemoveAll(staged)
		return err
	}
	if a.cfg.UpdateRestart != nil {
		return a.cfg.UpdateRestart(plan)
	}
	return restartInto(plan)
}

// restartInto starts the upgraded program with this one's arguments. The
// new one waits for this one to let go of the one-app lock
// (WaitLockEnv) instead of showing this one's window.
func restartInto(plan update.Plan) error {
	exe := plan.Target
	if plan.Bundle {
		exe = filepath.Join(plan.Target, "Contents", "MacOS", filepath.Base(executable()))
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), WaitLockEnv+"=1")
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Process.Release()
	return nil
}

// WaitLockEnv tells a starting app to wait for the running one to quit
// (an upgrade restarting it) rather than show that one's window.
const WaitLockEnv = "REMOHOP_UPGRADE_RESTART"

// cleanupUpdate removes what an earlier upgrade left (the old program).
func (a *App) cleanupUpdate() {
	a.mu.Lock()
	plan, ok := a.upd.plan, a.upd.planOK
	a.mu.Unlock()
	if ok {
		update.Cleanup(plan)
	}
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

// TelegramURL is the project's Telegram channel (the website's support
// page and footer link to it too).
const TelegramURL = "https://t.me/remohop"

// sitePage is one of the pages the app links to (Settings › About, and
// the guide): privacy, terms, licensing, support or the AI agents guide on
// the website, in lang, or the Telegram channel. Only these: the page
// cannot ask the app to open anything else.
func sitePage(page, lang string) (string, error) {
	switch page {
	case "telegram":
		return TelegramURL, nil
	case "privacy", "terms", "licensing", "support", "docs/ai-agents":
	default:
		return "", Errorf("bad_request", "unknown page")
	}
	if normLang(lang) == "zh" {
		return "https://remohop.com/" + page + "/", nil
	}
	return "https://remohop.com/en/" + page + "/", nil
}
