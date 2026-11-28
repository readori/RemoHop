package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/ffmpeg"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
)

// Unattended access: this computer reachable without anyone at it, from
// boot or sign-in, with a strong password the user chooses (and pairings
// made with it). It is `remohop service install`: on Windows the RemoHop
// service (LocalSystem, --unattended: the sign-in screen and UAC prompts
// too), which needs an administrator, so the app starts itself elevated
// for that one step; on macOS and Linux a per-user service (LaunchAgent,
// systemd --user) that needs no elevation and serves the user's desktop.
//
// The service has the host's role from then on: on macOS and Linux it is
// the same identity and ID as the app's (the same config directory), and
// the app shows it as "served by the background service"; on Windows the
// service has its own identity in %ProgramData%\RemoHop.

// UnattendedState is shown under "This computer".
type UnattendedState struct {
	Available bool   `json:"available"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Admin     bool   `json:"needs_admin"`
	ID        string `json:"id,omitempty"`
	Busy      bool   `json:"busy,omitempty"`
	Error     string `json:"error,omitempty"`
	// StatusError is why the service's status could not be read (shown in
	// the diagnostic report only).
	StatusError string `json:"status_error,omitempty"`
}

type unattendedCache struct {
	mu    sync.Mutex
	st    UnattendedState
	at    time.Time
	busy  bool
	err   string
	winID string
}

var unattended unattendedCache

// serviceStatus runs `remohop service status --json` in-process.
func serviceStatus(ctx context.Context) (installed, running bool) {
	installed, running, _ = serviceStatusErr(ctx)
	return installed, running
}

// serviceStatusErr is serviceStatus with the reason the status could not
// be read, if it could not.
func serviceStatusErr(ctx context.Context) (installed, running bool, why string) {
	c := cli.Lookup("service")
	if c == nil {
		return false, false, "this program cannot manage services"
	}
	var out, errb bytes.Buffer
	c.Run(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, []string{"status", "--json"})
	var st struct {
		Installed bool `json:"installed"`
		Running   bool `json:"running"`
	}
	if json.Unmarshal(out.Bytes(), &st) != nil {
		why = cli.Sanitize(strings.TrimSpace(errb.String()))
		if why == "" {
			why = "no answer"
		}
		return false, false, why
	}
	return st.Installed, st.Running, ""
}

// UnattendedState reports unattended access (the service is asked at most
// every few seconds).
func (a *App) UnattendedState() UnattendedState {
	u := &unattended
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.at) > 5*time.Second && !u.busy {
		a.readUnattended()
	}
	st := u.st
	st.Busy, st.Error = u.busy, u.err
	return st
}

// readUnattended asks the service how it is. unattended.mu is held.
func (a *App) readUnattended() {
	u := &unattended
	installed, running, why := serviceStatusErr(a.ctx)
	u.st = UnattendedState{Available: unattendedAvailable(), Installed: installed, Running: running, Admin: runtime.GOOS == "windows", StatusError: why}
	if installed {
		if runtime.GOOS == "windows" {
			if u.winID == "" {
				a.mu.Lock()
				u.winID = signal.FormatID(a.settings.ServiceID)
				a.mu.Unlock()
			}
			u.st.ID = u.winID
		} else if hs, _ := hostd.ReadStatus(a.dir); hs != nil {
			u.st.ID = signal.FormatID(hs.ID)
		}
	}
	u.at = time.Now()
}

func unattendedAvailable() bool {
	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		return cli.Lookup("service") != nil
	}
	return false
}

// CheckStrongPassword is the rule for an unattended password: it guards a
// computer nobody watches, so more than the host's minimum.
func CheckStrongPassword(pw string) error {
	p := auth.NormalizePassword(pw)
	if err := auth.CheckPassword(p); err != nil {
		return Errorf("weak_password", err.Error())
	}
	var lower, upper, digit, other bool
	for _, r := range p {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			classes++
		}
	}
	if len([]rune(p)) < 12 || (classes < 3 && len([]rune(p)) < 20) {
		return Errorf("weak_password", "use at least 12 characters with three of: lower case, upper case, digits, symbols (or 20 characters)")
	}
	return nil
}

func (a *App) setBusy(busy bool, err error) {
	u := &unattended
	u.mu.Lock()
	u.busy, u.at = busy, time.Time{}
	if err != nil {
		u.err = err.Error()
	} else {
		u.err = ""
	}
	u.mu.Unlock()
	a.wake()
}

// EnableUnattended installs the service with password.
func (a *App) EnableUnattended(ctx context.Context, password string) error {
	if !unattendedAvailable() {
		return Errorf("unsupported", "unattended access is not available here")
	}
	if err := CheckStrongPassword(password); err != nil {
		return err
	}
	a.setBusy(true, nil)
	var err, problem error
	if runtime.GOOS == "windows" {
		// The step may go on installing ffmpeg for the service after the
		// service is up: then this returns, and the switch shows "Working"
		// until it ends.
		var r elevatedResult
		r, err = a.elevatedStep(ctx, "enable", password, func(r elevatedResult, err error) {
			a.setBusy(false, a.serviceFFmpegResult(r, err))
		})
		if err == nil && r.ID != "" {
			a.setServiceID(r.ID)
		}
		if err == nil && r.More {
			a.log.Info("unattended access turned on; installing ffmpeg for the service")
			unattended.mu.Lock()
			a.readUnattended() // show it on, while busy
			unattended.mu.Unlock()
			a.wake()
			return nil
		}
		if err == nil {
			problem = a.serviceFFmpegResult(r, nil)
		}
	} else {
		err = a.installUserService(ctx, password)
	}
	if err != nil {
		a.log.Warn("unattended access could not be turned on", "err", err)
		problem = err
	} else {
		a.log.Info("unattended access turned on")
	}
	a.setBusy(false, problem)
	return err
}

// serviceFFmpegResult records what the administrator step found or
// installed for the Windows service's screen sharing, and returns the
// problem to show under unattended access, if there is one: the service
// has no ffmpeg. err is the step's own failure.
func (a *App) serviceFFmpegResult(r elevatedResult, err error) error {
	if r.FFmpegNote != "" {
		a.log.Warn("ffmpeg for the service", "note", r.FFmpegNote)
	}
	if err != nil {
		a.log.Warn("ffmpeg could not be installed for the service", "err", err)
		return Errorf("service_ffmpeg", "ffmpeg was not installed for unattended access, so it cannot share the screen: "+err.Error())
	}
	if r.FFmpeg == nil {
		return nil // the step did not look (no service)
	}
	path := ""
	if r.FFmpeg.Found != nil {
		path = r.FFmpeg.Found.Path
	}
	a.update(func(s *Settings) { s.ServiceFFmpeg = path })
	switch {
	case path != "":
		a.log.Info("ffmpeg for the service", "path", path)
		return nil
	case r.FFmpegError != "":
		a.log.Warn("ffmpeg could not be installed for the service", "err", r.FFmpegError)
		return Errorf("service_ffmpeg", "unattended access cannot share the screen: installing ffmpeg for it failed: "+r.FFmpegError)
	case r.FFmpeg.Why != "":
		a.log.Info("the service has no ffmpeg", "why", r.FFmpeg.Why)
		return Errorf("service_ffmpeg", "unattended access cannot share the screen: it has no ffmpeg, and "+r.FFmpeg.Why)
	}
	return nil
}

// serviceFFmpegToo: the user installs ffmpeg with the app's button while
// unattended access is on (Windows). The service cannot use the user's
// copy (see internal/ffmpeg, target.go), so an administrator step (one UAC
// prompt) installs one for it too, unless an earlier step found that it
// has one.
func (a *App) serviceFFmpegToo() {
	if runtime.GOOS != "windows" {
		return
	}
	st := a.UnattendedState()
	a.mu.Lock()
	known := a.settings.ServiceFFmpeg
	a.mu.Unlock()
	if !needServiceFFmpeg(st, known) {
		return
	}
	a.log.Info("asking an administrator to install ffmpeg for the service too")
	a.setBusy(true, nil)
	go func() {
		r, err := a.elevatedResult(a.ctx, "ffmpeg", "")
		a.setBusy(false, a.serviceFFmpegResult(r, err))
	}()
}

// needServiceFFmpeg reports whether to ask an administrator to install
// ffmpeg for the unattended-access service: it is installed, no step is
// under way, and no step has found an ffmpeg for it (known).
func needServiceFFmpeg(st UnattendedState, known string) bool {
	return st.Installed && !st.Busy && known == ""
}

// DisableUnattended removes the service (and on macOS and Linux the fixed
// password, so the app's own temporary password is the only one again).
func (a *App) DisableUnattended(ctx context.Context) error {
	a.setBusy(true, nil)
	var err error
	if runtime.GOOS == "windows" {
		_, err = a.elevated(ctx, "disable", "")
		if err == nil {
			a.update(func(s *Settings) { s.ServiceFFmpeg = "" })
		}
	} else {
		err = runService(ctx, "uninstall")
		if err == nil {
			err = config.Set(hostd.SettingPassword, "")
		}
		if err == nil {
			a.restartHost()
		}
	}
	a.setBusy(false, err)
	return err
}

func runService(ctx context.Context, args ...string) error {
	c := cli.Lookup("service")
	if c == nil {
		return errors.New("this program cannot install services")
	}
	var out, errb bytes.Buffer
	if code := c.Run(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, args); code != 0 {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return errors.New(cli.Sanitize(msg))
	}
	return nil
}

// installUserService: macOS and Linux. The service takes over this
// computer's identity, so the app's own host stops first.
func (a *App) installUserService(ctx context.Context, password string) error {
	if err := config.Set(hostd.SettingPassword, auth.NormalizePassword(password)); err != nil {
		return err
	}
	a.stopHost()
	if err := runService(ctx, "install", "--user"); err != nil {
		config.Set(hostd.SettingPassword, "")
		a.restartHost()
		return err
	}
	a.mu.Lock()
	on := a.settings.Incoming
	a.mu.Unlock()
	if on {
		a.startHost() // shows the service as serving this computer
	}
	return nil
}

// elevatedResult is what the elevated helper reports.
type elevatedResult struct {
	ID       string                 `json:"id,omitempty"`
	Sessions []hostd.ControlSession `json:"sessions,omitempty"`
	Closed   int                    `json:"closed,omitempty"`
	// Password is the service's password, for status --reveal only.
	Password string `json:"password,omitempty"`
	Error    string `json:"error,omitempty"`
	// More: the step did what was asked and goes on installing ffmpeg for
	// the service; the result it writes when it ends replaces this one.
	More bool `json:"more,omitempty"`
	// FFmpeg is the service's ffmpeg after the step (enable, ffmpeg), when
	// it looked; FFmpegError says why installing it failed (the step
	// itself did not), FFmpegNote what was moved aside first.
	FFmpeg      *ffmpeg.Plan `json:"ffmpeg,omitempty"`
	FFmpegError string       `json:"ffmpeg_error,omitempty"`
	FFmpegNote  string       `json:"ffmpeg_note,omitempty"`
}

// elevated runs `<this program> desktop-unattended <op>` as administrator
// (Windows asks the user) and waits for it. extra are more arguments.
func (a *App) elevated(ctx context.Context, op, password string, extra ...string) (string, error) {
	r, err := a.elevatedResult(ctx, op, password, extra...)
	return r.ID, err
}

func (a *App) elevatedResult(ctx context.Context, op, password string, extra ...string) (elevatedResult, error) {
	return a.elevatedStep(ctx, op, password, nil, extra...)
}

// elevatedStep runs the administrator step op and returns its result. A
// step may write a result with More before it ends: it has done what was
// asked and goes on with something slow (installing ffmpeg for the
// service). If later is set, elevatedStep then returns that result at once,
// and later gets the final one when the step ends.
func (a *App) elevatedStep(ctx context.Context, op, password string, later func(elevatedResult, error), extra ...string) (elevatedResult, error) {
	run, err := config.SubDir("run")
	if err != nil {
		return elevatedResult{}, err
	}
	tmp, err := os.MkdirTemp(run, "unattended-")
	if err != nil {
		return elevatedResult{}, err
	}
	result := filepath.Join(tmp, "result.json")
	args := append([]string{"desktop-unattended", op, "--result", result}, extra...)
	if password != "" {
		pwFile := filepath.Join(tmp, "password")
		if err := os.WriteFile(pwFile, []byte(auth.NormalizePassword(password)), 0o600); err != nil {
			os.RemoveAll(tmp)
			return elevatedResult{}, err
		}
		args = append(args, "--password-file", pwFile)
	}
	exe, err := os.Executable()
	if err != nil {
		os.RemoveAll(tmp)
		return elevatedResult{}, err
	}
	a.log.Info("administrator step", "op", op, "exe", exe)
	finish := func(err error) (elevatedResult, error) {
		defer os.RemoveAll(tmp)
		if err != nil {
			a.log.Warn("administrator step did not start", "op", op, "err", err)
			return elevatedResult{}, err
		}
		return a.readElevated(op, result)
	}
	if later == nil {
		return finish(runElevated(ctx, exe, args))
	}
	// The step outlives this request when it goes on: wait for it with
	// the app's context.
	done := make(chan error, 1)
	go func() { done <- runElevated(a.ctx, exe, args) }()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return finish(err)
		case <-ctx.Done():
			go func() { <-done; os.RemoveAll(tmp) }()
			return elevatedResult{}, ctx.Err()
		case <-tick.C:
			// The step writes its result whole (renamed into place), so
			// a result that reads is complete.
			var r elevatedResult
			if b, err := os.ReadFile(result); err == nil && json.Unmarshal(b, &r) == nil && r.More && r.Error == "" {
				go func() { later(finish(<-done)) }()
				return r, nil
			}
		}
	}
}

// readElevated reads the result the step op wrote.
func (a *App) readElevated(op, result string) (elevatedResult, error) {
	b, err := os.ReadFile(result)
	if err != nil {
		// The helper ran but wrote nothing: it refused its arguments (for
		// example a different administrator account, whose RemoHop folder
		// is not this user's) or could not start at all.
		a.log.Warn("administrator step left no result", "op", op, "err", err)
		return elevatedResult{}, Errorf("elevation", "the administrator step did not finish")
	}
	var r elevatedResult
	if err := json.Unmarshal(b, &r); err != nil {
		return elevatedResult{}, err
	}
	if r.Error != "" {
		a.log.Warn("administrator step failed", "op", op, "err", r.Error)
		return r, errors.New(r.Error)
	}
	return r, nil
}

const unattendedUsage = `Usage: desktop-unattended enable|disable|status|disconnect|password|ffmpeg --result FILE
                          [--password-file FILE] [--id N] [--reveal]

The administrator steps of the desktop app's "Unattended access" (Windows),
which the app starts elevated, one at a time, when the user asks: install
the RemoHop service with --unattended and set its password (enable), remove
it (disable), report its ID and sessions (status; with --reveal also its
password, which the user asked to see), end a session (disconnect,
--id 0 for all), change its password (password) or install ffmpeg for it
(ffmpeg). Enable installs ffmpeg for the service too when it has none (as
ffmpeg does): it writes its result with "more" first, then the final one.
Not meant to be typed.
`

func init() {
	cli.Register(&cli.Command{Name: "desktop-unattended", Hidden: true, Summary: "the desktop app's administrator step", Usage: unattendedUsage,
		Run: runUnattendedHelper})
}

func runUnattendedHelper(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("%s", unattendedUsage)
		return cli.ExitUsage
	}
	op := args[0]
	fs := flag.NewFlagSet("desktop-unattended", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	result := fs.String("result", "", "")
	pwFile := fs.String("password-file", "", "")
	id := fs.Int("id", 0, "")
	reveal := fs.Bool("reveal", false, "")
	known := map[string]bool{"enable": true, "disable": true, "status": true, "disconnect": true, "password": true, "ffmpeg": true}
	if err := fs.Parse(args[1:]); err != nil || *result == "" || !known[op] {
		env.Errorf("%s", unattendedUsage)
		return cli.ExitUsage
	}
	// It runs as administrator: it writes and reads only files in this user's
	// private run directory, where the app put them.
	if run, err := config.SubDir("run"); err != nil || !inDir(run, *result) || (*pwFile != "" && !inDir(run, *pwFile)) {
		env.Errorf("desktop-unattended: the files must be in the RemoHop run directory")
		return cli.ExitUsage
	}
	// This user's settings, before a step points this process at the
	// service's: whether this user switched ffmpeg downloads off counts.
	userConfigDir, hadConfigDir := os.LookupEnv("REMOHOP_CONFIG_DIR")
	backToUserSettings := func() {
		if hadConfigDir {
			os.Setenv("REMOHOP_CONFIG_DIR", userConfigDir)
		} else {
			os.Unsetenv("REMOHOP_CONFIG_DIR")
		}
	}
	var r elevatedResult
	err := func() error {
		dir, err := serviceConfigDir()
		if err != nil {
			return err
		}
		switch op {
		case "ffmpeg":
			serviceFFmpeg(ctx, &r, func() {})
			return nil
		case "disable":
			return runService(ctx, "uninstall")
		case "status":
			if st, _ := hostd.ReadStatus(dir); st != nil {
				r.ID = st.ID
			}
			if *reveal {
				// The service's settings are readable by administrators
				// only; the result file is in the user's private run
				// directory and removed as soon as the app has read it.
				if err := os.Setenv("REMOHOP_CONFIG_DIR", dir); err != nil {
					return err
				}
				all, err := config.All()
				if err != nil {
					return err
				}
				r.Password = all[hostd.SettingPassword]
				return nil
			}
			ss, err := hostd.ControlSessions(dir)
			r.Sessions = ss
			return err
		case "disconnect":
			n, err := hostd.ControlDisconnect(dir, *id)
			r.Closed = n
			return err
		}
		pw, err := os.ReadFile(*pwFile)
		if err != nil {
			return err
		}
		if op == "password" {
			if err := CheckStrongPassword(string(pw)); err != nil {
				return err
			}
			if err := os.Setenv("REMOHOP_CONFIG_DIR", dir); err != nil {
				return err
			}
			return config.Set(hostd.SettingPassword, strings.TrimSpace(string(pw)))
		}
		// The service has its own settings (in ProgramData), which know
		// nothing of the rendezvous server chosen in the app (this user's
		// settings, still the ones this process reads here): it is given
		// on the service's command line, or the service's host stops at
		// once ("no rendezvous server configured") and Windows restarts it
		// over and over.
		logFrom := fileSize(filepath.Join(dir, "service.log")) // an earlier attempt's lines are not this one's
		sigArgs := serviceSignalArgs()
		if err := runService(ctx, append([]string{"install", "--unattended"}, sigArgs...)...); err != nil {
			return err
		}
		// The service's own settings: point this process at them.
		if err := os.Setenv("REMOHOP_CONFIG_DIR", dir); err != nil {
			return err
		}
		if err := config.Set(hostd.SettingPassword, strings.TrimSpace(string(pw))); err != nil {
			return err
		}
		id, err := waitServiceID(ctx, dir, logFrom, sigArgs[0] == "--no-signal", 30*time.Second)
		if err != nil {
			// A service that cannot run must not stay behind, restarting.
			runService(ctx, "uninstall")
			return err
		}
		r.ID = id
		// Screen sharing through the service needs ffmpeg it can use.
		// Unattended access is on now: the app may go on while a
		// download runs.
		backToUserSettings()
		serviceFFmpeg(ctx, &r, func() {
			r.More = true
			writeElevatedResult(*result, r)
			r.More = false
		})
		return nil
	}()
	if err != nil {
		r.Error = err.Error()
	}
	if werr := writeElevatedResult(*result, r); werr != nil {
		env.Errorf("desktop-unattended: %v", werr)
		return cli.ExitError
	}
	if err != nil {
		return cli.ExitError
	}
	return cli.ExitOK
}

// writeElevatedResult writes r whole, so that the app, which may read it
// while the step goes on, never sees part of it.
func writeElevatedResult(path string, r elevatedResult) error {
	b, _ := json.Marshal(r)
	return config.WriteFileAtomic(path, b, 0o600)
}

// serviceFFmpeg makes sure the unattended-access service has an ffmpeg it
// can use, which cannot be the user's copy: when it finds none, RemoHop's
// verified copy is installed into its own directory, which only SYSTEM and
// the Administrators can change (ffmpeg.PrepareFor, InstallFor). Not when
// the service has one, there is no build for this system or downloads are
// switched off (this user's switches and the machine's). starting is called
// before a download. The outcome goes into r.
func serviceFFmpeg(ctx context.Context, r *elevatedResult, starting func()) {
	if ffmpeg.Service == nil {
		return
	}
	t, ok, err := ffmpeg.Service(ctx)
	if err != nil {
		r.FFmpeg, r.FFmpegError = &ffmpeg.Plan{}, err.Error()
		return
	}
	if !ok {
		return
	}
	plan, note, err := ffmpeg.PrepareFor(t)
	r.FFmpegNote = note
	if err != nil {
		r.FFmpeg, r.FFmpegError = &ffmpeg.Plan{}, err.Error()
		return
	}
	if plan.Install {
		starting()
		if f, err := ffmpeg.InstallFor(ctx, t, ffmpeg.Options{}); err != nil {
			plan, r.FFmpegError = ffmpeg.Plan{}, err.Error()
		} else {
			plan = ffmpeg.Plan{Found: &f}
		}
	}
	r.FFmpeg = &plan
}

// serviceSignalArgs are the `service install` arguments for the rendezvous
// server chosen in the app: --signal ADDR, or --no-signal when the user
// chose none (reachable by address only).
func serviceSignalArgs() []string {
	all, _ := config.All()
	if addr := strings.TrimSpace(all[signal.SettingAddr]); addr != "" {
		return []string{"--signal", addr}
	}
	return []string{"--no-signal"}
}

// waitServiceID waits for the newly started service to register and
// returns its ID. A service that registers late (a slow network) is not a
// failure: "" and no error, the ID shows later. A service whose host
// stopped is: the error is the host's last complaint from its log.
// Without a rendezvous server (noID) there is no ID to wait for: the host
// being up is enough.
func waitServiceID(ctx context.Context, dir string, logFrom int64, noID bool, wait time.Duration) (string, error) {
	deadline := time.Now().Add(wait)
	for {
		if st, _ := hostd.ReadStatus(dir); st != nil && (st.ID != "" || noID) {
			return st.ID, nil
		}
		if why := hostFailure(dir, logFrom); why != "" {
			if _, running := serviceStatus(ctx); !running {
				return "", Errorf("service_failed", "the service could not start: "+why)
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if why := hostFailure(dir, logFrom); why != "" {
				return "", Errorf("service_failed", "the service could not start: "+why)
			}
			return "", nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// hostFailure is the reason the service's host last stopped, from the
// service log ("remohop: host: …" before "host stopped") after offset
// from, or "".
func hostFailure(dir string, from int64) string {
	b, err := os.ReadFile(filepath.Join(dir, "service.log"))
	if err != nil || int64(len(b)) <= from {
		return ""
	}
	b = b[from:]
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], "host stopped") {
		return ""
	}
	for i := len(lines) - 2; i >= 0 && i >= len(lines)-12; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "remohop: host: ") {
			return cli.Sanitize(strings.TrimPrefix(l, "remohop: host: "))
		}
	}
	return cli.Sanitize(strings.TrimSpace(lines[len(lines)-1]))
}

func fileSize(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// serviceConfigDir is the Windows service's config directory (see
// internal/service: %ProgramData%\RemoHop).
func serviceConfigDir() (string, error) {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "RemoHop"), nil
}

// ServiceSessions lists the sessions of the unattended-access service. On
// Windows the service's state is the administrators' only, so this asks for
// an administrator (UAC) and also refreshes the service's ID.
func (a *App) ServiceSessions(ctx context.Context) ([]hostd.ControlSession, error) {
	if runtime.GOOS == "windows" {
		r, err := a.elevatedResult(ctx, "status", "")
		if r.ID != "" {
			a.setServiceID(r.ID)
		}
		if r.Sessions == nil {
			r.Sessions = []hostd.ControlSession{}
		}
		return r.Sessions, err
	}
	ss, err := hostd.ControlSessions(a.dir)
	if ss == nil {
		ss = []hostd.ControlSession{}
	}
	return ss, err
}

// UnattendedReveal returns the unattended-access password, which the user
// asked to see. On Windows it is in the service's settings, which only
// administrators can read, so this asks for one (UAC) and also refreshes
// the service's ID; on macOS and Linux it is in the user's own settings.
func (a *App) UnattendedReveal(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		r, err := a.elevatedResult(ctx, "status", "", "--reveal")
		if r.ID != "" {
			a.setServiceID(r.ID)
		}
		return r.Password, err
	}
	all, err := config.All()
	if err != nil {
		return "", err
	}
	return all[hostd.SettingPassword], nil
}

// ServiceDisconnect ends a session of the service (0: all of them),
// elevated on Windows.
func (a *App) ServiceDisconnect(ctx context.Context, id int) error {
	if runtime.GOOS == "windows" {
		_, err := a.elevatedResult(ctx, "disconnect", "", "--id", strconv.Itoa(id))
		return err
	}
	_, err := hostd.ControlDisconnect(a.dir, id)
	return err
}

// ServicePassword changes the unattended password, elevated on Windows.
func (a *App) ServicePassword(ctx context.Context, password string) error {
	if err := CheckStrongPassword(password); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		_, err := a.elevatedResult(ctx, "password", password)
		return err
	}
	return config.Set(hostd.SettingPassword, auth.NormalizePassword(password))
}

// setServiceID remembers the Windows service's ID (its state is not readable
// without an administrator, so the app keeps what the elevated steps found).
func (a *App) setServiceID(id string) {
	unattended.mu.Lock()
	unattended.winID = signal.FormatID(id)
	unattended.at = time.Time{}
	unattended.mu.Unlock()
	a.update(func(s *Settings) { s.ServiceID = id })
}

// inDir reports whether path is inside dir (both cleaned and absolute).
func inDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsAbs(path) && rel != "." && !strings.HasPrefix(rel, "..")
}
