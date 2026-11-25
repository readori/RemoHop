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
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
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
	c := cli.Lookup("service")
	if c == nil {
		return false, false
	}
	var out bytes.Buffer
	c.Run(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: io.Discard}, []string{"status", "--json"})
	var st struct {
		Installed bool `json:"installed"`
		Running   bool `json:"running"`
	}
	if json.Unmarshal(out.Bytes(), &st) != nil {
		return false, false
	}
	return st.Installed, st.Running
}

// UnattendedState reports unattended access (the service is asked at most
// every few seconds).
func (a *App) UnattendedState() UnattendedState {
	u := &unattended
	u.mu.Lock()
	defer u.mu.Unlock()
	if time.Since(u.at) > 5*time.Second && !u.busy {
		installed, running := serviceStatus(a.ctx)
		u.st = UnattendedState{Available: unattendedAvailable(), Installed: installed, Running: running, Admin: runtime.GOOS == "windows"}
		if installed {
			if runtime.GOOS == "windows" {
				u.st.ID = u.winID
			} else if hs, _ := hostd.ReadStatus(a.dir); hs != nil {
				u.st.ID = signal.FormatID(hs.ID)
			}
		}
		u.at = time.Now()
	}
	st := u.st
	st.Busy, st.Error = u.busy, u.err
	return st
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
	var err error
	if runtime.GOOS == "windows" {
		var id string
		id, err = a.elevated(ctx, "enable", password)
		if err == nil {
			unattended.mu.Lock()
			unattended.winID = signal.FormatID(id)
			unattended.mu.Unlock()
		}
	} else {
		err = a.installUserService(ctx, password)
	}
	a.setBusy(false, err)
	return err
}

// DisableUnattended removes the service (and on macOS and Linux the fixed
// password, so the app's own temporary password is the only one again).
func (a *App) DisableUnattended(ctx context.Context) error {
	a.setBusy(true, nil)
	var err error
	if runtime.GOOS == "windows" {
		_, err = a.elevated(ctx, "disable", "")
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
	ID    string `json:"id,omitempty"`
	Error string `json:"error,omitempty"`
}

// elevated runs `<this program> desktop-unattended <op>` as administrator
// (Windows asks the user) and waits for it.
func (a *App) elevated(ctx context.Context, op, password string) (string, error) {
	run, err := config.SubDir("run")
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(run, "unattended-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	result := filepath.Join(tmp, "result.json")
	args := []string{"desktop-unattended", op, "--result", result}
	if password != "" {
		pwFile := filepath.Join(tmp, "password")
		if err := os.WriteFile(pwFile, []byte(auth.NormalizePassword(password)), 0o600); err != nil {
			return "", err
		}
		args = append(args, "--password-file", pwFile)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := runElevated(ctx, exe, args); err != nil {
		return "", err
	}
	b, err := os.ReadFile(result)
	if err != nil {
		return "", Errorf("elevation", "the administrator step did not finish")
	}
	var r elevatedResult
	if err := json.Unmarshal(b, &r); err != nil {
		return "", err
	}
	if r.Error != "" {
		return "", errors.New(r.Error)
	}
	return r.ID, nil
}

const unattendedUsage = `Usage: desktop-unattended enable|disable --result FILE [--password-file FILE]

The administrator step of the desktop app's "Unattended access" (Windows):
install the RemoHop service with --unattended and set its password, or
remove it. The app starts this elevated; it is not meant to be typed.
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
	if err := fs.Parse(args[1:]); err != nil || *result == "" || (op != "enable" && op != "disable") {
		env.Errorf("%s", unattendedUsage)
		return cli.ExitUsage
	}
	var r elevatedResult
	err := func() error {
		dir, err := serviceConfigDir()
		if err != nil {
			return err
		}
		if op == "disable" {
			return runService(ctx, "uninstall")
		}
		pw, err := os.ReadFile(*pwFile)
		if err != nil {
			return err
		}
		if err := runService(ctx, "install", "--unattended"); err != nil {
			return err
		}
		// The service's own settings: point this process at them.
		if err := os.Setenv("REMOHOP_CONFIG_DIR", dir); err != nil {
			return err
		}
		if err := config.Set(hostd.SettingPassword, strings.TrimSpace(string(pw))); err != nil {
			return err
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if st, _ := hostd.ReadStatus(dir); st != nil && st.ID != "" {
				r.ID = st.ID
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return nil // installed; the ID shows once the service has registered
	}()
	if err != nil {
		r.Error = err.Error()
	}
	b, _ := json.Marshal(r)
	if werr := os.WriteFile(*result, b, 0o600); werr != nil {
		env.Errorf("desktop-unattended: %v", werr)
		return cli.ExitError
	}
	if err != nil {
		return cli.ExitError
	}
	return cli.ExitOK
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
