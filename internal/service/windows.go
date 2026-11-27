//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/readori/RemoHop/internal/unattended"
)

// winService manages a service in the Service Control Manager. A Windows
// service runs in session 0, isolated from the interactive desktop, so
// screenshots and remote desktop of the logged-in user's screen do not work
// through it; shell, files, forwards and the like do. Installed with
// --unattended, it reaches the console session through a helper process
// there (internal/unattended).
type winService struct{}

func newWindows() backend { return winService{} }

func (winService) svcName(o Options) string {
	if o.Name == "" || o.Name == DefaultName {
		return "RemoHop"
	}
	return o.Name
}

func (w winService) Print(o Options) (string, error) {
	exe, err := o.exePath()
	if err != nil {
		return "", err
	}
	copyNote := ""
	if !adminOnlyLocation(exe) {
		copyNote = fmt.Sprintf("binary: %s is copied to %s, which only administrators can change\n",
			exe, filepath.Join(serviceExeDir(), serviceExeName(w.svcName(o))))
		exe = filepath.Join(serviceExeDir(), serviceExeName(w.svcName(o)))
	}
	text := fmt.Sprintf("service %q, start type automatic (delayed), restart on failure after 5s\n%scommand: %q %v\nconfig dir: %s\n",
		w.svcName(o), copyNote, exe, w.runArgs(o), w.dataDir(o))
	if o.Unattended {
		text += "unattended access: clients see and control the console session (sign-in, lock and UAC screens included)\n"
	}
	return text, nil
}

func (w winService) Install(ctx context.Context, o Options) (*Installed, error) {
	if o.Scope == ScopeUser {
		return nil, errors.New("Windows services are always system-wide; drop --user")
	}
	if o.RunAs != "" || o.Linger {
		return nil, errors.New("--run-as and --linger do not apply on Windows")
	}
	src, err := o.exePath()
	if err != nil {
		return nil, err
	}
	m, err := mgr.Connect()
	if err != nil {
		return nil, fmt.Errorf("cannot reach the service manager (run from an elevated prompt): %w", err)
	}
	defer m.Disconnect()
	name := w.svcName(o)
	// Installing over an existing service is the upgrade step: stop it so
	// that its binary can be replaced, then change it in place (which keeps
	// settings made outside RemoHop, such as its environment).
	existing, err := m.OpenService(name)
	if err == nil {
		defer existing.Close()
		if err := stopAndWait(ctx, existing); err != nil {
			return nil, fmt.Errorf("cannot stop the running service %q: %w", name, err)
		}
	} else {
		existing = nil
	}
	// The service runs as LocalSystem: whoever can replace its binary gets
	// LocalSystem. A binary outside Program Files (install.ps1 and Scoop
	// install under the user's profile) is copied there first.
	exe, copied, err := installBinary(src, name)
	if err != nil {
		return nil, err
	}
	if err := prepareStateDir(w.dataDir(o)); err != nil {
		return nil, err
	}
	cfg := mgr.Config{
		DisplayName:      "RemoHop host",
		Description:      "Lets authorised RemoHop clients control this machine (remohop host).",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}
	var s *mgr.Service
	if existing != nil {
		cur, err := existing.Config()
		if err != nil {
			return nil, err
		}
		cur.BinaryPathName = commandLine(exe, w.runArgs(o))
		cur.DisplayName, cur.Description = cfg.DisplayName, cfg.Description
		cur.StartType, cur.DelayedAutoStart = cfg.StartType, cfg.DelayedAutoStart
		if err := existing.UpdateConfig(cur); err != nil {
			return nil, err
		}
		s = existing
	} else {
		if s, err = m.CreateService(name, exe, cfg, w.runArgs(o)...); err != nil {
			return nil, err
		}
		defer s.Close()
	}
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 86400); err != nil {
		return nil, err
	}
	// The host exits with an error status rather than crashing, which
	// recovery actions ignore unless asked.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return nil, err
	}
	if err := s.Start(); err != nil {
		return nil, fmt.Errorf("installed, but could not start: %w", err)
	}
	notes := []string{
		"the service runs in session 0 and cannot capture the interactive desktop (no screenshots or remote desktop of the logged-in screen); --unattended changes that",
	}
	if o.Unattended {
		notes = unattendedNotes(exe)
	}
	if copied {
		notes = append(notes, "the service runs "+exe+", a copy only administrators can change; after installing a new remohop, run `remohop service install` (elevated) again to update the service")
	}
	if existing != nil {
		notes = append(notes, "the existing service was updated in place and restarted")
	}
	notes = append(notes,
		"what the host prints goes to "+serviceLogPath(w.dataDir(o)),
		"the state directory holds this instance's credentials; only SYSTEM and Administrators can open it")
	return &Installed{Backend: "Windows service", Name: name, Scope: ScopeSystem, ConfigDir: w.dataDir(o), Notes: notes}, nil
}

func (w winService) open(o Options) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach the service manager (run from an elevated prompt): %w", err)
	}
	s, err := m.OpenService(w.svcName(o))
	if err != nil {
		m.Disconnect()
		return nil, nil, errNotInstalled
	}
	return m, s, nil
}

// openQuery opens the service with only the rights to read its state and
// configuration, which every user has: mgr.Connect and OpenService ask for
// full access, which only an administrator gets, so for anyone else (the
// desktop app, `remohop service status` in a normal prompt) an installed
// service looked absent.
func (w winService) openQuery(o Options) (*mgr.Mgr, *mgr.Service, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach the service manager: %w", err)
	}
	m := &mgr.Mgr{Handle: h}
	name := w.svcName(o)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	sh, err := windows.OpenService(h, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		m.Disconnect()
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, errNotInstalled
		}
		return nil, nil, fmt.Errorf("cannot read the service %q: %w", name, err)
	}
	return m, &mgr.Service{Name: name, Handle: sh}, nil
}

func (w winService) Uninstall(ctx context.Context, o Options) (bool, error) {
	m, s, err := w.open(o)
	if err != nil {
		if errors.Is(err, errNotInstalled) {
			return false, nil
		}
		return false, err
	}
	defer m.Disconnect()
	defer s.Close()
	var exe string
	if cfg, err := s.Config(); err == nil {
		if args, err := windows.DecomposeCommandLine(cfg.BinaryPathName); err == nil && len(args) > 0 {
			exe = args[0]
		}
	}
	if err := stopAndWait(ctx, s); err != nil {
		return true, err
	}
	if err := s.Delete(); err != nil {
		return true, err
	}
	if exe != "" {
		removeInstalledBinary(exe)
	}
	return true, nil
}

func stopAndWait(ctx context.Context, s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st, err = s.Control(svc.Stop); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the service to stop")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

func (w winService) Control(ctx context.Context, o Options, action string) error {
	m, s, err := w.open(o)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	switch action {
	case "stop":
		return stopAndWait(ctx, s)
	case "start":
		return s.Start()
	case "restart":
		if err := stopAndWait(ctx, s); err != nil {
			return err
		}
		return s.Start()
	}
	return fmt.Errorf("unknown action %q", action)
}

func (w winService) Status(ctx context.Context, o Options) (*Status, error) {
	name := w.svcName(o)
	st := &Status{Backend: "Windows service", Name: name, Scope: "system"}
	m, s, err := w.openQuery(o)
	if err != nil {
		if errors.Is(err, errNotInstalled) {
			st.Scope = ""
			return st, nil
		}
		return nil, err
	}
	defer m.Disconnect()
	defer s.Close()
	st.Installed, st.ConfigDir = true, installedDataDir(s)
	st.Unattended = installedUnattended(s)
	if cfg, err := s.Config(); err == nil {
		if args, err := windows.DecomposeCommandLine(cfg.BinaryPathName); err == nil && len(args) > 0 {
			st.setExe(args[0])
		}
	}
	q, err := s.Query()
	if err != nil {
		return nil, err
	}
	names := map[svc.State]string{
		svc.Stopped: "stopped", svc.StartPending: "starting", svc.StopPending: "stopping",
		svc.Running: "running", svc.ContinuePending: "continuing", svc.PausePending: "pausing", svc.Paused: "paused",
	}
	st.State = names[q.State]
	st.Running = q.State == svc.Running
	st.PID = int(q.ProcessId)
	if cfg, err := s.Config(); err == nil {
		st.Enabled = cfg.StartType == mgr.StartAutomatic
	}
	return st, nil
}

func (w winService) Logs(ctx context.Context, o Options, n int, follow bool, out io.Writer) error {
	m, s, err := w.open(o)
	if err != nil {
		return err
	}
	dir := installedDataDir(s)
	s.Close()
	m.Disconnect()
	return tailFile(ctx, serviceLogPath(dir), n, follow, out)
}

// runArgs is the command line (after the binary) the SCM starts.
func (winService) runArgs(o Options) []string {
	args := append([]string{"service", "run"}, o.hostArgs()[1:]...)
	if o.ConfigDir != "" {
		args = append(args, "--config-dir", o.ConfigDir)
	}
	if o.Unattended {
		args = append(args, "--unattended")
	}
	return args
}

// unattendedNotes is the advice printed after installing with --unattended.
func unattendedNotes(exe string) []string {
	notes := []string{
		"unattended access is ON: whoever logs in to this service (its password, pairings, authorised keys) sees and controls the console session as it is, " +
			"including the sign-in screen, the lock screen and UAC prompts; keep the password strong or use `remohop config ask on`",
	}
	if ok, why := sasPolicyNote(); !ok {
		notes = append(notes, "Ctrl+Alt+Del from clients: "+why)
	}
	return notes
}

// adminOnlyLocation reports whether path is under Program Files or the
// Windows directory, which only administrators can change by default.
func adminOnlyLocation(path string) bool {
	p := strings.ToLower(filepath.Clean(path))
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "SystemRoot"} {
		if d := os.Getenv(env); d != "" && strings.HasPrefix(p, strings.ToLower(filepath.Clean(d))+`\`) {
			return true
		}
	}
	return false
}

// installedUnattended reports whether an installed service was set up with
// --unattended.
func installedUnattended(s *mgr.Service) bool {
	if cfg, err := s.Config(); err == nil {
		if args, err := windows.DecomposeCommandLine(cfg.BinaryPathName); err == nil {
			for _, a := range args {
				if a == "--unattended" {
					return true
				}
			}
		}
	}
	return false
}

func (winService) dataDir(o Options) string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return windowsDataDir()
}

// installedDataDir recovers the state directory of an installed service
// from its registered command line.
func installedDataDir(s *mgr.Service) string {
	if cfg, err := s.Config(); err == nil {
		if args, err := windows.DecomposeCommandLine(cfg.BinaryPathName); err == nil {
			for i, a := range args {
				if a == "--config-dir" && i+1 < len(args) {
					return args[i+1]
				}
			}
		}
	}
	return windowsDataDir()
}

// runAsService is `remohop service run`: the process the SCM starts.
func runAsService(ctx context.Context, name string, args []string, unattendedAccess bool, log io.Writer) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !isSvc {
		return errors.New("`remohop service run` is started by the Windows service manager; use `remohop host` in a terminal")
	}
	return svc.Run(name, &handler{args: args, log: log, unattended: unattendedAccess})
}

type handler struct {
	args       []string
	log        io.Writer
	unattended bool
}

// Execute implements svc.Handler: it runs the host, and turns a stop or
// shutdown request into cancellation of the host's context.
func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if h.unattended {
		// Before the host serves anyone: capture and input then go to
		// the console session from the first request on.
		if stop, err := unattended.Enable(h.log); err != nil {
			fmt.Fprintf(h.log, "remohop: unattended access is not available: %v\n", err)
		} else {
			defer stop()
		}
	}
	done := make(chan error, 1)
	go func() { done <- RunHost(ctx, h.args, h.log) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			if err != nil {
				fmt.Fprintf(h.log, "remohop: host stopped: %v\n", err)
				return true, 1 // service-specific code: recovery actions restart it
			}
			return false, 0
		}
	}
}

// openServiceLog checks and locks down the state directory before the host
// reads or writes any key material there (adoptStateDir), then opens the
// log there, rotated by size.
func openServiceLog(dir string) (io.WriteCloser, error) {
	note, err := adoptStateDir(dir)
	if err != nil {
		return nil, err
	}
	l, err := openRotatingLog(serviceLogPath(dir), maxServiceLog)
	if err != nil {
		return nil, err
	}
	if note != "" {
		fmt.Fprintln(l, note)
	}
	return l, nil
}

// sasPolicyNote reports whether the policy lets the service send
// Ctrl+Alt+Del, and if not how to allow it.
func sasPolicyNote() (bool, string) {
	if unattended.SASPolicyAllows() {
		return true, ""
	}
	return false, unattended.SASPolicyHint
}
