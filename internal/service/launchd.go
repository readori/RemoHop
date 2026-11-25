package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// launchd manages a LaunchAgent (per user, in the GUI session) or a
// LaunchDaemon (system) with launchctl's modern bootstrap/bootout verbs.
type launchd struct {
	home      string
	euid      int
	daemonDir string // where LaunchDaemons live
	// checkExe vets the binary of a system daemon (nil: no check, for
	// tests that use paths which do not exist).
	checkExe func(exe string, o Options) error
	// trustedDir reports whether a directory may be on the daemon's PATH
	// (nil: all of them).
	trustedDir func(dir string) bool
	// logDir overrides the directory of the system daemon's log (tests).
	logDir string
}

func newLaunchd() *launchd {
	home, _ := os.UserHomeDir()
	return &launchd{home: home, euid: os.Geteuid(), daemonDir: "/Library/LaunchDaemons",
		checkExe: func(exe string, o Options) error {
			real, err := filepath.EvalSymlinks(exe)
			if err != nil {
				real = exe
			}
			return checkSystemExe(exe, o, fmt.Sprintf("    sudo mkdir -p /Library/RemoHop && sudo install -m 755 %s /Library/RemoHop/remohop\n"+
				"    sudo /Library/RemoHop/remohop service install --system ...   (the same flags as before; repeat both after each upgrade)", shellQuote(real)))
		},
		trustedDir: func(dir string) bool { return untrustedExe(dir, map[uint32]bool{0: true}) == "" },
	}
}

func (b *launchd) label(o Options) string {
	if o.name() == DefaultName {
		return "com.remohop.host"
	}
	return "com.remohop." + o.name()
}

func (b *launchd) plistPath(s Scope, o Options) string {
	if s == ScopeSystem {
		return path.Join(b.daemonDir, b.label(o)+".plist")
	}
	return path.Join(b.home, "Library", "LaunchAgents", b.label(o)+".plist")
}

func (b *launchd) domain(s Scope) string {
	if s == ScopeSystem {
		return "system"
	}
	return "gui/" + strconv.Itoa(b.euid)
}

func (b *launchd) logPath(s Scope, o Options) string {
	if s == ScopeSystem {
		dir := "/var/log"
		if b.logDir != "" {
			dir = b.logDir
		}
		return path.Join(dir, o.name()+".log")
	}
	return path.Join(b.home, "Library", "Logs", "remohop", o.name()+".log")
}

// maxLaunchdLog is the size above which the log is rotated when RemoHop
// (re)starts the job; launchd itself never rotates StandardOutPath.
const maxLaunchdLog = 10 << 20

// prepareLog creates the job's log file before launchd opens it, so that it
// is private (launchd would create it world-readable, and the host prints
// its ID, addresses and, on a terminal-less start, how to reach it), and
// moves a log that has grown past maxLaunchdLog to NAME.log.1.
func prepareLog(p string) error {
	if fi, err := os.Lstat(p); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		if fi.Size() > maxLaunchdLog {
			if err := os.Rename(p, p+".1"); err != nil {
				return err
			}
			os.Chmod(p+".1", 0o600)
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Chmod(0o600)
}

// daemonPath is the PATH of the job. A system daemon runs as root, so a
// directory that someone else can write (Homebrew's prefix belongs to the
// user who installed it) would let them plant a program the host runs.
func (b *launchd) daemonPath(s Scope) string {
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	if s != ScopeSystem {
		return strings.Join(dirs, ":")
	}
	sys := []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}
	for _, d := range dirs[:2] {
		if b.trustedDir == nil || b.trustedDir(d) {
			sys = append(sys, d)
		}
	}
	return strings.Join(sys, ":")
}

func supportDir(o Options) string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return "/Library/Application Support/" + o.name()
}

func (b *launchd) resolveInstall(o Options) (Scope, error) {
	s := o.Scope
	if s == ScopeAuto {
		s = ScopeUser
		if b.euid == 0 {
			s = ScopeSystem
		}
	}
	switch {
	case s == ScopeSystem && b.euid != 0:
		return 0, errors.New("--system needs root: run `sudo remohop service install --system`")
	case s == ScopeUser && b.euid == 0:
		return 0, errors.New("a per-user agent belongs to a logged-in user: run `remohop service install --user` without sudo, or use --system")
	}
	return s, nil
}

func (b *launchd) detect(o Options) (Scope, bool) {
	switch o.Scope {
	case ScopeUser, ScopeSystem:
		_, err := os.Stat(b.plistPath(o.Scope, o))
		return o.Scope, err == nil
	}
	for _, s := range []Scope{ScopeUser, ScopeSystem} {
		if _, err := os.Stat(b.plistPath(s, o)); err == nil {
			return s, true
		}
	}
	return ScopeAuto, false
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func (b *launchd) text(s Scope, o Options, exe string) string {
	var p strings.Builder
	str := func(indent, v string) { p.WriteString(indent + "<string>" + xmlEscape(v) + "</string>\n") }
	key := func(indent, k string) { p.WriteString(indent + "<key>" + k + "</key>\n") }

	p.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("\t", "Label")
	str("\t", b.label(o))
	key("\t", "ProgramArguments")
	p.WriteString("\t<array>\n")
	str("\t\t", exe)
	for _, a := range o.hostArgs() {
		str("\t\t", a)
	}
	p.WriteString("\t</array>\n")
	p.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	p.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	p.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n")
	// Shell jobs run in their own sessions; do not kill them with the host.
	p.WriteString("\t<key>AbandonProcessGroup</key>\n\t<true/>\n")
	key("\t", "StandardOutPath")
	str("\t", b.logPath(s, o))
	key("\t", "StandardErrorPath")
	str("\t", b.logPath(s, o))
	key("\t", "EnvironmentVariables")
	p.WriteString("\t<dict>\n")
	key("\t\t", "PATH")
	str("\t\t", b.daemonPath(s))
	if s == ScopeSystem || o.ConfigDir != "" {
		key("\t\t", "REMOHOP_CONFIG_DIR")
		str("\t\t", supportDir(o))
	}
	if s == ScopeSystem {
		key("\t\t", "HOME")
		str("\t\t", "/var/root")
	}
	p.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return p.String()
}

func (b *launchd) Print(o Options) (string, error) {
	s := o.Scope
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

func (b *launchd) ctl(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (b *launchd) loaded(ctx context.Context, s Scope, o Options) bool {
	_, err := b.ctl(ctx, "print", b.domain(s)+"/"+b.label(o))
	return err == nil
}

// unloadWait bounds how long unload waits for launchd to let go of a job.
var unloadWait = 15 * time.Second

// unload boots the job out if it is loaded and waits until launchd has
// removed it: bootout returns while the host is still shutting down, and a
// bootstrap of the same label before it is gone fails ("Bootstrap failed:
// 5: Input/output error").
func (b *launchd) unload(ctx context.Context, s Scope, o Options) error {
	if !b.loaded(ctx, s, o) {
		return nil
	}
	target := b.domain(s) + "/" + b.label(o)
	_, bootErr := b.ctl(ctx, "bootout", target)
	deadline := time.Now().Add(unloadWait)
	for b.loaded(ctx, s, o) {
		if time.Now().After(deadline) {
			if bootErr != nil {
				return bootErr
			}
			return fmt.Errorf("launchd still has %s loaded %v after bootout", target, unloadWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}

func (b *launchd) Install(ctx context.Context, o Options) (*Installed, error) {
	s, err := b.resolveInstall(o)
	if err != nil {
		return nil, err
	}
	if o.RunAs != "" || o.Linger {
		return nil, errors.New("--run-as and --linger do not apply on macOS")
	}
	exe, err := o.exePath()
	if err != nil {
		return nil, err
	}
	if s == ScopeSystem && b.checkExe != nil {
		if err := b.checkExe(exe, o); err != nil {
			return nil, err
		}
	}
	if s == ScopeUser {
		if _, err := b.ctl(ctx, "print", b.domain(s)); err != nil {
			return nil, fmt.Errorf("there is no GUI session for this user (%s); a LaunchAgent needs one: log in at the Mac's screen, or use sudo with --system", b.domain(s))
		}
	}
	plist := b.plistPath(s, o)
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return nil, err
	}
	logDir := filepath.Dir(b.logPath(s, o))
	if s == ScopeUser {
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, err
		}
		os.Chmod(logDir, 0o700)
	} else if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	if err := prepareLog(b.logPath(s, o)); err != nil {
		return nil, err
	}
	if s == ScopeSystem {
		if err := os.MkdirAll(supportDir(o), 0o700); err != nil {
			return nil, err
		}
	}
	// launchd refuses a daemon plist that is writable by others.
	if err := writeFileAtomic(plist, []byte(b.text(s, o, exe)), 0o644); err != nil {
		return nil, err
	}
	target := b.domain(s) + "/" + b.label(o)
	// Unload a previous version first so the new plist takes effect.
	if err := b.unload(ctx, s, o); err != nil {
		return nil, err
	}
	for _, step := range [][]string{
		// enable first: a label that was ever disabled cannot be bootstrapped.
		{"enable", target},
		{"bootstrap", b.domain(s), plist},
		{"kickstart", "-k", target},
	} {
		if _, err := b.ctl(ctx, step...); err != nil {
			return nil, fmt.Errorf("%w\n(the plist was written to %s)", err, plist)
		}
	}
	inst := &Installed{Backend: "launchd", Name: o.name(), Scope: s, Path: plist, ConfigDir: o.ConfigDir}
	if s == ScopeSystem {
		inst.ConfigDir = supportDir(o)
	} else {
		inst.Notes = append(inst.Notes, "a LaunchAgent starts when you log in; use --system for a host that runs at boot")
	}
	return inst, nil
}

func (b *launchd) Uninstall(ctx context.Context, o Options) (bool, error) {
	s, found := b.detect(o)
	if !found {
		return false, nil
	}
	if s == ScopeSystem && b.euid != 0 {
		return false, errors.New("the service is installed system-wide: run `sudo remohop service uninstall`")
	}
	if err := b.unload(ctx, s, o); err != nil {
		return true, err
	}
	return true, os.Remove(b.plistPath(s, o))
}

func (b *launchd) Control(ctx context.Context, o Options, action string) error {
	s, found := b.detect(o)
	if !found {
		return errNotInstalled
	}
	if s == ScopeSystem && b.euid != 0 {
		return fmt.Errorf("the service is installed system-wide: run `sudo remohop service %s`", action)
	}
	target := b.domain(s) + "/" + b.label(o)
	var err error
	switch action {
	case "stop":
		// bootout, not kill: KeepAlive would immediately respawn the host.
		// It is loaded again at the next login or boot, or by `start`.
		err = b.unload(ctx, s, o)
	case "start", "restart":
		// Rotated here too; launchd keeps the file open while the job runs,
		// so a rotation takes effect with the restart below.
		if err := prepareLog(b.logPath(s, o)); err != nil {
			return err
		}
		if !b.loaded(ctx, s, o) {
			b.ctl(ctx, "enable", target)
			if _, err = b.ctl(ctx, "bootstrap", b.domain(s), b.plistPath(s, o)); err != nil {
				return err
			}
		}
		_, err = b.ctl(ctx, "kickstart", "-k", target)
	default:
		err = fmt.Errorf("unknown action %q", action)
	}
	return err
}

func (b *launchd) Status(ctx context.Context, o Options) (*Status, error) {
	st := &Status{Backend: "launchd", Name: b.label(o)}
	s, found := b.detect(o)
	if !found {
		return st, nil
	}
	st.Installed, st.Scope, st.Path = true, s.String(), b.plistPath(s, o)
	if s == ScopeSystem {
		st.ConfigDir = supportDir(o)
	}
	out, err := b.ctl(ctx, "print", b.domain(s)+"/"+b.label(o))
	if err != nil {
		st.State = "not loaded"
		return st, nil
	}
	st.Enabled = true
	st.State, st.PID = parsePrint(string(out))
	st.Running = st.State == "running"
	return st, nil
}

// parsePrint extracts "state = ..." and "pid = ..." from `launchctl print`.
func parsePrint(out string) (state string, pid int) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch k {
		case "state":
			if state == "" {
				state = v
			}
		case "pid":
			if pid == 0 {
				pid, _ = strconv.Atoi(v)
			}
		}
	}
	return state, pid
}

func (b *launchd) Logs(ctx context.Context, o Options, n int, follow bool, w io.Writer) error {
	s, found := b.detect(o)
	if !found {
		return errNotInstalled
	}
	return tailFile(ctx, b.logPath(s, o), n, follow, w)
}
