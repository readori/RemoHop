package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// systemd manages a unit with systemctl. It shells out rather than talking
// D-Bus: the same commands are what the user would run to inspect it.
type systemd struct {
	home      string      // the user's home directory
	systemDir string      // where system units live
	euid      int         // effective uid
	booted    func() bool // is systemd the running init?
}

func newSystemd() *systemd {
	home, _ := os.UserHomeDir()
	return &systemd{
		home:      home,
		systemDir: "/etc/systemd/system",
		euid:      os.Geteuid(),
		booted: func() bool {
			// The same test as sd_booted(3).
			fi, err := os.Stat("/run/systemd/system")
			return err == nil && fi.IsDir()
		},
	}
}

const errNoSystemd = `systemd is not running on this machine (a container, WSL without systemd, or another init such as OpenRC or runit).
Run "remohop host" (or "remohop signal") under your own supervisor instead; "remohop service print" shows a unit file you can adapt`

func (b *systemd) check() error {
	if _, err := exec.LookPath("systemctl"); err != nil || !b.booted() {
		return errors.New(errNoSystemd)
	}
	return nil
}

func (b *systemd) unit(o Options) string { return o.name() + ".service" }

func (b *systemd) unitPath(s Scope, o Options) string {
	if s == ScopeSystem {
		return path.Join(b.systemDir, b.unit(o))
	}
	return path.Join(b.home, ".config", "systemd", "user", b.unit(o))
}

// stateDir is the system instance's REMOHOP_CONFIG_DIR. systemd creates it from
// StateDirectory=, which is relative to /var/lib.
func stateDir(o Options) string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return "/var/lib/" + o.name()
}

// resolve picks the scope for install: an explicit one, else system for root
// and user for everybody else.
func (b *systemd) resolveInstall(o Options) (Scope, error) {
	s := o.Scope
	if o.Role == RoleSignal {
		if s == ScopeUser {
			return 0, errors.New("a rendezvous server is a system service: install it with sudo, without --user")
		}
		s = ScopeSystem
	}
	if s == ScopeAuto {
		s = ScopeUser
		if b.euid == 0 {
			s = ScopeSystem
		}
	}
	if s == ScopeSystem && b.euid != 0 {
		return 0, errors.New("--system needs root: run `sudo remohop service install --system`")
	}
	return s, nil
}

// detect finds which scope the service is installed in, for the commands
// that act on an existing one.
func (b *systemd) detect(o Options) (Scope, bool) {
	switch o.Scope {
	case ScopeUser, ScopeSystem:
		_, err := os.Stat(b.unitPath(o.Scope, o))
		return o.Scope, err == nil
	}
	for _, s := range []Scope{ScopeUser, ScopeSystem} {
		if _, err := os.Stat(b.unitPath(s, o)); err == nil {
			return s, true
		}
	}
	return ScopeAuto, false
}

// quote renders one ExecStart= word per systemd.syntax(7): double quotes keep
// spaces together, and '%' and '$' would otherwise be expanded.
func quote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\%$;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

func (b *systemd) text(s Scope, o Options, exe string) string {
	if o.Role == RoleSignal {
		return signalText(o, exe)
	}
	words := []string{quote(exe)}
	for _, a := range o.hostArgs() {
		words = append(words, quote(a))
	}
	var u strings.Builder
	u.WriteString("# Written by `remohop service install`. Customise with `systemctl edit " + b.unit(o) + "`.\n")
	u.WriteString("[Unit]\nDescription=RemoHop host (remote control)\nDocumentation=https://github.com/readori/RemoHop\n")
	if s == ScopeSystem {
		// network-online.target does not exist in a user manager.
		u.WriteString("After=network-online.target\nWants=network-online.target\n")
	}
	u.WriteString("\n[Service]\nType=simple\n")
	u.WriteString("ExecStart=" + strings.Join(words, " ") + "\n")
	// The host reconnects to the rendezvous server by itself; restart only
	// when it dies. A clean `systemctl stop` stays stopped.
	u.WriteString("Restart=on-failure\nRestartSec=5\n")
	// Shell jobs (`remohop <ID> shell -b`) run in their own sessions so they can
	// outlive a host restart. The default KillMode would kill them with it.
	u.WriteString("KillMode=process\n")
	switch {
	case o.ConfigDir != "":
		u.WriteString("Environment=" + quote("REMOHOP_CONFIG_DIR="+o.ConfigDir) + "\n")
	case s == ScopeSystem:
		// A dedicated state directory keeps the identity key and pairings
		// out of any one user's home. No sandboxing options on purpose:
		// the host exists to run shells, read and write files, mount
		// filesystems and reach X displays, and ProtectSystem, PrivateTmp,
		// NoNewPrivileges and friends would break exactly those.
		u.WriteString("Environment=REMOHOP_CONFIG_DIR=" + stateDir(o) + "\n")
		u.WriteString("StateDirectory=" + o.name() + "\nStateDirectoryMode=0700\n")
	}
	if s == ScopeSystem {
		if o.RunAs != "" {
			u.WriteString("User=" + o.RunAs + "\n")
		} else {
			u.WriteString("Environment=HOME=/root\n")
		}
	}
	u.WriteString("\n[Install]\n")
	if s == ScopeSystem {
		u.WriteString("WantedBy=multi-user.target\n")
	} else {
		u.WriteString("WantedBy=default.target\n")
	}
	return u.String()
}

func (b *systemd) Print(o Options) (string, error) {
	s := o.Scope
	if o.Role == RoleSignal {
		s = ScopeSystem
	}
	if s == ScopeAuto {
		s = ScopeUser
		if b.euid == 0 {
			s = ScopeSystem
		}
	}
	exe, err := o.exePath()
	if err != nil {
		return "", err
	}
	return b.text(s, o, exe), nil
}

// ctl runs systemctl for scope.
func (b *systemd) ctl(ctx context.Context, s Scope, args ...string) ([]byte, error) {
	if s == ScopeUser {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if s == ScopeUser && strings.Contains(msg, "Failed to connect to") {
			msg += "\n(there is no systemd user session for this login; log in normally instead of using su/sudo, or use --system)"
		}
		return out, fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}

func (b *systemd) Install(ctx context.Context, o Options) (*Installed, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	s, err := b.resolveInstall(o)
	if err != nil {
		return nil, err
	}
	if o.RunAs != "" && s != ScopeSystem {
		return nil, errors.New("--run-as only applies to --system")
	}
	if o.Linger && s != ScopeUser {
		return nil, errors.New("--linger only applies to --user")
	}
	exe, err := o.exePath()
	if err != nil {
		return nil, err
	}
	if o.Role == RoleSignal {
		if why := hiddenFromSignal(exe); why != "" {
			return nil, errors.New(why)
		}
	}
	unitPath := b.unitPath(s, o)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(unitPath, []byte(b.text(s, o, exe)), 0o644); err != nil {
		return nil, err
	}
	for _, step := range [][]string{
		{"daemon-reload"},
		{"enable", b.unit(o)},
		// restart, not start: an update must replace a running instance.
		{"restart", b.unit(o)},
	} {
		if _, err := b.ctl(ctx, s, step...); err != nil {
			return nil, fmt.Errorf("%w\n(the unit file was written to %s)", err, unitPath)
		}
	}

	inst := &Installed{Backend: "systemd", Name: o.name(), Scope: s, Path: unitPath, RunAs: o.RunAs, ConfigDir: o.ConfigDir}
	if s == ScopeSystem {
		inst.ConfigDir = stateDir(o)
		return inst, nil
	}
	if o.Linger {
		if out, err := exec.CommandContext(ctx, "loginctl", "enable-linger").CombinedOutput(); err != nil {
			inst.Notes = append(inst.Notes, fmt.Sprintf("could not enable linger (%v: %s); run: sudo loginctl enable-linger $USER", err, strings.TrimSpace(string(out))))
		} else {
			inst.Notes = append(inst.Notes, "linger enabled: the host starts at boot, before anyone logs in")
		}
	} else if !b.lingerEnabled(ctx) {
		inst.Notes = append(inst.Notes, "a user service only runs while you are logged in; to start it at boot without a login, run: sudo loginctl enable-linger $USER   (or reinstall with --linger)")
	}
	return inst, nil
}

// lingerEnabled reports whether the user's linger flag is on. When it cannot
// tell, it answers false so the advice is shown.
func (b *systemd) lingerEnabled(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "loginctl", "show-user", strconv.Itoa(b.euid), "-p", "Linger").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
}

func (b *systemd) Uninstall(ctx context.Context, o Options) (bool, error) {
	s, found := b.detect(o)
	if !found {
		return false, nil
	}
	if err := b.check(); err != nil {
		return false, err
	}
	if s == ScopeSystem && b.euid != 0 {
		return false, errors.New("the service is installed system-wide: run `sudo remohop service uninstall`")
	}
	// A unit that is already stopped or not enabled is not an error here.
	b.ctl(ctx, s, "disable", "--now", b.unit(o))
	if err := os.Remove(b.unitPath(s, o)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return true, err
	}
	_, err := b.ctl(ctx, s, "daemon-reload")
	return true, err
}

func (b *systemd) Control(ctx context.Context, o Options, action string) error {
	s, found := b.detect(o)
	if !found {
		return errNotInstalled
	}
	if err := b.check(); err != nil {
		return err
	}
	if s == ScopeSystem && b.euid != 0 {
		return fmt.Errorf("the service is installed system-wide: run `sudo remohop service %s`", action)
	}
	_, err := b.ctl(ctx, s, action, b.unit(o))
	return err
}

var errNotInstalled = errors.New("the service is not installed (run `remohop service install`)")

func (b *systemd) Status(ctx context.Context, o Options) (*Status, error) {
	st := &Status{Backend: "systemd", Name: o.name()}
	s, found := b.detect(o)
	if !found {
		return st, nil
	}
	if err := b.check(); err != nil {
		return nil, err
	}
	st.Installed, st.Scope, st.Path = true, s.String(), b.unitPath(s, o)
	if s == ScopeSystem {
		st.ConfigDir = stateDir(o)
	}
	out, err := b.ctl(ctx, s, "show", b.unit(o),
		"-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "UnitFileState", "-p", "ActiveEnterTimestamp")
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			props[k] = v
		}
	}
	st.State = props["ActiveState"]
	if sub := props["SubState"]; sub != "" && sub != st.State {
		st.State += " (" + sub + ")"
	}
	st.Running = props["ActiveState"] == "active"
	st.Enabled = props["UnitFileState"] == "enabled"
	if st.Running {
		st.PID, _ = strconv.Atoi(props["MainPID"])
		st.Since = props["ActiveEnterTimestamp"]
	}
	return st, nil
}

func (b *systemd) Logs(ctx context.Context, o Options, n int, follow bool, w io.Writer) error {
	s, found := b.detect(o)
	if !found {
		return errNotInstalled
	}
	args := []string{"-u", b.unit(o), "-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso"}
	if s == ScopeUser {
		args = append([]string{"--user"}, args...)
	}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); pw.Close(); done <- err }()
	copySanitized(w, pr)
	err := <-done
	if ctx.Err() != nil {
		return nil // interrupted while following: not a failure
	}
	return err
}

// writeFileAtomic replaces path in one rename so a crash cannot leave a
// half-written unit.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
