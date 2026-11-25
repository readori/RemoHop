// Package service installs `remohop host` as an operating-system service that
// starts at boot and is restarted when it fails: a systemd unit on Linux, a
// launchd job on macOS, and a Windows service on Windows. It backs the
// `remohop service` command.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/readori/RemoHop/internal/snapenv"
)

// DefaultName is the service name when --name is not given. Platforms map it
// to their own convention (the Windows service is "RemoHop", the launchd label
// com.remohop.host).
const DefaultName = "remohop"

// Scope says whose service this is.
type Scope int

const (
	// ScopeAuto picks the scope: system for root, user otherwise; for the
	// management commands, whichever one is installed.
	ScopeAuto Scope = iota
	// ScopeUser runs the host as the installing user, from that user's login
	// or boot (with linger on Linux).
	ScopeUser
	// ScopeSystem runs the host system-wide, started by the init system.
	ScopeSystem
)

func (s Scope) String() string {
	switch s {
	case ScopeUser:
		return "user"
	case ScopeSystem:
		return "system"
	}
	return "auto"
}

// Options describe the service to create or manage.
type Options struct {
	Scope  Scope
	Name   string // service name; empty means DefaultName
	Signal string // --signal for the host; empty: host default
	// NoSignal runs the host with --no-signal: reachable by address only.
	NoSignal bool
	// ConfigDir overrides the instance's REMOHOP_CONFIG_DIR; empty means the
	// scope's default (the user's own, or the system state directory).
	ConfigDir string
	RunAs     string // systemd system scope: run as this account instead of root
	Exe       string // binary to run; empty means this executable
	Linger    bool   // systemd user scope: also run `loginctl enable-linger`
	// Role is what the service runs: RoleHost (the default, `remohop host`)
	// or RoleSignal (`remohop signal`, a rendezvous server).
	Role string
	// SignalArgs are the `remohop signal` flags of a RoleSignal service,
	// apart from --data, which the service sets.
	SignalArgs []string
	// Unattended (Windows only) gives remote users of the service the
	// interactive console session, the sign-in screen, the lock screen
	// and UAC prompts included (internal/unattended). Off unless asked.
	Unattended bool
}

// Service roles.
const (
	RoleHost   = "host"
	RoleSignal = "signal"
	// DefaultSignalName is the service name of a rendezvous server.
	DefaultSignalName = "remohop-signal"
)

// Installed describes a completed install, for the instructions the CLI
// prints.
type Installed struct {
	Backend   string
	Name      string
	Scope     Scope
	Path      string   // unit / plist file; empty on Windows
	ConfigDir string   // REMOHOP_CONFIG_DIR of the instance; empty = the user's default
	RunAs     string   // account the system service runs as, if not root
	Notes     []string // extra advice, e.g. about linger
}

// Status is the state of the installed service.
type Status struct {
	Backend   string `json:"backend"`
	Name      string `json:"name"`
	Scope     string `json:"scope,omitempty"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	State     string `json:"state,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Since     string `json:"since,omitempty"`
	Path      string `json:"path,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"`
	// Unattended: a Windows service installed with --unattended.
	Unattended bool `json:"unattended,omitempty"`
	// Exe is the binary the service definition starts, when it could be
	// read; ExeMissing says that file does not exist (removed by an upgrade
	// or uninstall), so the service cannot start.
	Exe        string `json:"exe,omitempty"`
	ExeMissing bool   `json:"exe_missing,omitempty"`
}

// setExe records the service's binary and whether it exists.
func (st *Status) setExe(exe string) {
	if exe == "" {
		return
	}
	st.Exe = exe
	if _, err := os.Stat(exe); err != nil {
		st.ExeMissing = true
	}
}

// backend is one platform's service manager.
type backend interface {
	// Print returns the unit/plist the install would write.
	Print(o Options) (string, error)
	Install(ctx context.Context, o Options) (*Installed, error)
	Uninstall(ctx context.Context, o Options) (removed bool, err error)
	// Control is "start", "stop" or "restart".
	Control(ctx context.Context, o Options, action string) error
	Status(ctx context.Context, o Options) (*Status, error)
	// Logs writes the last n lines, then follows if asked, until ctx ends.
	Logs(ctx context.Context, o Options, n int, follow bool, w io.Writer) error
}

// newBackend returns the manager for the running OS; tests replace it.
var newBackend = func() (backend, error) {
	switch runtime.GOOS {
	case "linux":
		return newSystemd(), nil
	case "darwin":
		return newLaunchd(), nil
	case "windows":
		return newWindows(), nil
	}
	return nil, fmt.Errorf("services are not supported on %s", runtime.GOOS)
}

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)
	runAsRE  = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	signalRE = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9._:\[\]%/-]{0,299}$`)
)

// validate rejects values that would end up inside a unit file, plist or
// command line. They are all quoted or escaped as well; this keeps the
// accepted set boring.
func (o Options) validate() error {
	switch o.Role {
	case "", RoleHost:
		if len(o.SignalArgs) > 0 {
			return errors.New("rendezvous-server flags need --role signal")
		}
	case RoleSignal:
		if o.Unattended {
			return errors.New("--unattended is for --role host")
		}
		if o.Signal != "" || o.NoSignal || o.RunAs != "" || o.Linger || o.ConfigDir != "" {
			return errors.New("--signal, --no-signal, --run-as, --linger and --config-dir are for --role host; " +
				"a rendezvous server runs as its own unprivileged user with its state in /var/lib/" + o.name())
		}
		if len(o.SignalArgs) > 40 {
			return errors.New("too many remohop signal arguments")
		}
		for _, a := range o.SignalArgs {
			if len(a) > 300 || strings.ContainsFunc(a, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				return fmt.Errorf("invalid remohop signal argument %q", a)
			}
			if a == "--data" || strings.HasPrefix(a, "--data=") {
				return errors.New("--data is set by the service (/var/lib/" + o.name() + ")")
			}
		}
	default:
		return fmt.Errorf("unknown --role %q (host or signal)", o.Role)
	}
	if o.Name != "" && !nameRE.MatchString(o.Name) {
		return fmt.Errorf("invalid service name %q (letters, digits, '.', '_', '-'; at most 40)", o.Name)
	}
	if o.Signal != "" && !signalRE.MatchString(o.Signal) {
		return fmt.Errorf("invalid --signal address %q", o.Signal)
	}
	if o.RunAs != "" && !runAsRE.MatchString(o.RunAs) {
		return fmt.Errorf("invalid --run-as account %q", o.RunAs)
	}
	if o.NoSignal && o.Signal != "" {
		return fmt.Errorf("--signal and --no-signal are exclusive")
	}
	for flag, p := range map[string]string{"--exe": o.Exe, "--config-dir": o.ConfigDir} {
		if p == "" {
			continue
		}
		// Either OS's notion of absolute: the unit and plist generators
		// are also used (print, tests) on machines they do not target.
		if !filepath.IsAbs(p) && !path.IsAbs(p) {
			return fmt.Errorf("%s must be an absolute path, got %q", flag, p)
		}
		if strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return fmt.Errorf("%s contains control characters", flag)
		}
	}
	return nil
}

func (o Options) name() string {
	switch {
	case o.Name != "":
		return o.Name
	case o.Role == RoleSignal:
		return DefaultSignalName
	}
	return DefaultName
}

// hostArgs are the arguments after the binary. The password is never one of
// them: it would be readable from the unit file and `ps`. The instance uses
// `remohop config set host-password` like a foreground host does.
func (o Options) hostArgs() []string {
	a := []string{"host"}
	switch {
	case o.NoSignal:
		a = append(a, "--no-signal")
	case o.Signal != "":
		a = append(a, "--signal", o.Signal)
	}
	return a
}

// exePath is the binary the service runs: the path this program was
// started by, when it names this very file, so that a package manager's
// stable symlink (Homebrew's bin/remohop, Nix profiles) is what the service
// definition holds and survives upgrades, rather than the versioned file it
// points at today (which os.Executable returns on Linux). From the snap,
// the revision directory is replaced by /snap/<name>/current, which follows
// refreshes.
func (o Options) exePath() (string, error) {
	if o.Exe != "" {
		return o.Exe, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot locate this binary: %w (use --exe)", err)
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	if inv := invokedAs(exe, arg0()); inv != "" {
		exe = inv
	}
	return snapenv.StableExe(exe), nil
}

// arg0 is os.Args[0]; tests replace it.
var arg0 = func() string { return os.Args[0] }

// invokedAs returns the absolute form of the path the program was started
// by (looked up on PATH when it has no directory) if that path leads to the
// same file as exe, else "".
func invokedAs(exe, a0 string) string {
	if a0 == "" {
		return ""
	}
	p := a0
	if !strings.ContainsRune(p, '/') && !strings.ContainsRune(p, filepath.Separator) {
		lp, err := exec.LookPath(p)
		if err != nil {
			return ""
		}
		p = lp
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	a, err1 := os.Stat(p)
	b, err2 := os.Stat(exe)
	if err1 != nil || err2 != nil || !os.SameFile(a, b) {
		return ""
	}
	return p
}

func isRoot() bool { return os.Geteuid() == 0 }
