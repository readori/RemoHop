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
)

// launchd manages a LaunchAgent (per user, in the GUI session) or a
// LaunchDaemon (system) with launchctl's modern bootstrap/bootout verbs.
type launchd struct {
	home      string
	euid      int
	daemonDir string // where LaunchDaemons live
}

func newLaunchd() *launchd {
	home, _ := os.UserHomeDir()
	return &launchd{home: home, euid: os.Geteuid(), daemonDir: "/Library/LaunchDaemons"}
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
		return "/var/log/" + o.name() + ".log"
	}
	return path.Join(b.home, "Library", "Logs", "remohop", o.name()+".log")
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
	str("\t\t", "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin")
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
	plist := b.plistPath(s, o)
	for _, d := range []string{filepath.Dir(plist), filepath.Dir(b.logPath(s, o))} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
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
	if b.loaded(ctx, s, o) {
		b.ctl(ctx, "bootout", target)
	}
	for _, step := range [][]string{
		{"bootstrap", b.domain(s), plist},
		{"enable", target},
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
	if b.loaded(ctx, s, o) {
		if _, err := b.ctl(ctx, "bootout", b.domain(s)+"/"+b.label(o)); err != nil {
			return true, err
		}
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
		if b.loaded(ctx, s, o) {
			_, err = b.ctl(ctx, "bootout", target)
		}
	case "start", "restart":
		if !b.loaded(ctx, s, o) {
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
