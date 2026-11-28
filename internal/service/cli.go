package service

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/unattended"
)

const usage = `Usage: remohop service install [--user|--system] [--signal ADDR|--no-signal]
                           [--name NAME] [--run-as ACCOUNT] [--linger]
                           [--exe PATH] [--config-dir DIR] [--unattended]
       remohop service install --role signal [--listen ADDR] [--alt ADDR] [--relay
                           [--relay-ports LO-HI]] [--panel ADDR] [--name NAME]
                           [--exe PATH] [-- MORE-REMOHOP-SIGNAL-FLAGS]
       remohop service uninstall
       remohop service start|stop|restart
       remohop service status [--json]
       remohop service logs [-f] [-n N]
       remohop service print [--user|--system] [--signal ADDR]

Run the host (` + "`remohop host`" + `) as an operating-system service, so this machine
is controllable from boot or login without a terminal left open, and comes
back by itself after a crash. With --role signal, run a rendezvous server
(` + "`remohop signal`" + `) as a hardened system service instead.

The other subcommands take --role signal (or --name) to address that
service rather than the host.

  install    write the service definition, enable it and start it now
  uninstall  stop it and remove the definition
  status     installed? enabled? running? (exit 0 only when running)
  logs       what the host printed (-f follows, -n sets the line count, 50)
  print      show the unit/plist that install would write, without
             installing anything (also works where service installs do not,
             e.g. in a container, to adapt for another init system)

Flags for install:
  --user      per-user service (default unless root)
  --system    system-wide service (default as root; needs root)
  --signal    rendezvous server for the host (default: global --signal flag,
              REMOHOP_SIGNAL, then ` + "`remohop config get signal`" + ` at start)
  --no-signal run the host with --no-signal: no rendezvous server, reachable
              by address only (remohop ADDR:PORT ...)
  --name      service name (default "remohop"; lets several instances coexist)
  --run-as    Linux --system: run as this account instead of root
  --linger    Linux --user: also run "loginctl enable-linger" so the host
              starts at boot without anyone logging in
  --exe       binary to run (default: this binary; must be absolute)
  --config-dir  state directory (REMOHOP_CONFIG_DIR) of the instance, instead of
              the default for its scope; it holds the instance's credentials
  --unattended  Windows: unattended access. Clients of the service (with its
              password, pairings or authorised keys, as always) see and
              control the console session: the user's desktop, the sign-in
              screen, the lock screen and UAC prompts, as LocalSystem. Off
              unless given; reinstall without it to turn it off. See
              "Unattended access" below and docs/security.md.

Rendezvous server (--role signal; Linux with systemd only, needs root):
  --listen ADDR       UDP address (default :21116)
  --alt ADDR          NAT-detection address (default: the next port)
  --relay             also relay sessions that cannot connect directly
  --relay-ports LO-HI the ports relayed sessions may use (recommended with
                      --relay: the firewall then needs only that range)
  --panel ADDR        also serve the operator's web panel on this TCP address
                      (127.0.0.1:8810, say; ` + "`remohop help signal panel`" + `). Its
                      settings (REMOHOP_PANEL_PASSWORD, REMOHOP_PANEL_CF_*) go
                      in /etc/remohop/NAME-panel.env, read when the service
                      starts; keep that file mode 600
  -- FLAGS            anything further is passed to ` + "`remohop signal`" + ` as is (for
                      example its account and relay-limit flags)
  The binary must live outside /home, /root, /tmp and /var/tmp, which the
  unit's sandbox hides: sudo install -m 755 remohop /usr/local/bin/remohop first.
  The unit /etc/systemd/system/remohop-signal.service runs the server as a
  transient unprivileged user (DynamicUser) with its key and ID table in
  /var/lib/remohop-signal, a read-only system, no capabilities, and
  MemoryMax=512M (the server sizes its connection limit to that). Open the
  ports with the commands ` + "`remohop signal --print-firewall [same flags]`" + ` prints, in
  the machine's firewall and the cloud provider's. The server key, to pin in
  clients: sudo remohop signal --show-key --data /var/lib/remohop-signal

The service has its OWN identity and password, separate from a ` + "`remohop host`" + ` you
start by hand. After installing, read its ID and password with ` + "`remohop status`" + `.
Set a fixed password with ` + "`remohop passwd`" + ` (it asks twice and keeps the
password out of shell history; the running host picks it up without a
restart). The password is deliberately never put in the service definition.

Platforms
  A system service (--system on Linux and macOS, every Windows service)
  runs its binary with root or LocalSystem rights, so whoever can change
  that file gets them. On Linux and macOS install refuses a binary that
  another account can change (e.g. ~/.local/bin/remohop or a Homebrew
  prefix) and shows how to copy it where only root can write.

  Linux (systemd)   --user: ~/.config/systemd/user/remohop.service.
                    --system: /etc/systemd/system/remohop.service with state in
                    /var/lib/remohop. That is a different instance from
                    your own: reach it with
                        sudo env REMOHOP_CONFIG_DIR=/var/lib/remohop remohop status
                    A system service has no desktop session; use --user when
                    remote desktop or clipboard access to your screen matters.
                    Spawned shell jobs survive a service restart.
                    Without systemd (containers, OpenRC) install fails with
                    an explanation; use ` + "`remohop service print`" + ` to adapt the unit.
  macOS (launchd)   --user: LaunchAgent ~/Library/LaunchAgents/com.remohop.host.plist,
                    started at login. --system: LaunchDaemon in
                    /Library/LaunchDaemons, started at boot, state in
                    /Library/Application Support/remohop. Logs are files
                    (private; moved to .log.1 past 10 MiB when started with
                    this command); stop unloads the job until the next
                    login/boot or start.
  Windows           a service named RemoHop (automatic start, restarted on
                    failure); install from an elevated prompt. It runs as
                    LocalSystem in session 0, which cannot see the interactive
                    desktop: shell, files and forwards work, screenshots and
                    remote desktop of the logged-in screen do not. A binary
                    outside Program Files is copied to
                    %ProgramFiles%\RemoHop\remohop.exe, which only
                    administrators can change, and the service runs that copy;
                    run ` + "`remohop service install`" + ` again after an upgrade to
                    update and restart the service. The instance's
                    credentials (identity key, password, pairings) live in
                    %ProgramData%\RemoHop, owned by the Administrators and
                    open to SYSTEM and Administrators only (install refuses a
                    directory there that someone else made); its log is
                    service.log there, rotated at 10 MiB. Read them from an
                    elevated prompt:
                        set REMOHOP_CONFIG_DIR=%ProgramData%\RemoHop & remohop status

Unattended access (Windows, --unattended)
  The service starts a helper (remohop itself, as LocalSystem) in the session
  on the physical console when a client asks for the screen or input, and
  ends it when idle. The helper follows the desktop that has the input, so
  clients see and control the sign-in and lock screens and UAC prompts on
  the secure desktop, and the session that is on the console after a log
  off, fast user switching or a remote desktop connection.
  REMOHOP_UNATTENDED_SESSION=active in the service's environment shows a
  session with a user logged in (e.g. a remote desktop one) rather than an
  empty console. Ctrl+Alt+Del (the viewer's key menu, or
  remohop input key ctrl+alt+delete) works when the policy allows services to
  send it:
      reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System
          /v SoftwareSASGeneration /t REG_DWORD /d 1 /f
  The desktop service needs ffmpeg the service can find (machine PATH, or
  REMOHOP_FFMPEG in the service's environment); keep it, like remohop.exe, where
  only administrators can write (e.g. under Program Files). Or install a
  verified copy for the service, from an elevated prompt:
      set REMOHOP_CONFIG_DIR=%ProgramData%\RemoHop & remohop ffmpeg install

Examples:
  remohop service install                      # as yourself, starts now
  remohop service install --user --linger      # and at boot (Linux)
  sudo remohop service install --system --signal rendezvous.example.net
  remohop service install --unattended         # Windows, elevated prompt
  sudo remohop service install --role signal --relay --relay-ports 40000-40099
  remohop service status --role signal
  remohop service status --json
  remohop service logs -f
  remohop service uninstall`

func init() {
	cli.Register(&cli.Command{
		Name:    "service",
		Summary: "run the host as a system service (starts at boot)",
		Usage:   usage,
		Run:     run,
	})
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(env.Stderr, usage)
		return cli.ExitUsage
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install", "print":
		return runInstall(ctx, env, sub, rest)
	case "uninstall":
		return runUninstall(ctx, env, rest)
	case "start", "stop", "restart":
		return runControl(ctx, env, sub, rest)
	case "status":
		return runStatus(ctx, env, rest)
	case "logs":
		return runLogs(ctx, env, rest)
	case "run":
		return runService(ctx, env, rest)
	case "session-helper":
		return runSessionHelper(ctx, env, rest)
	}
	env.Errorf("service: unknown subcommand %q (install, uninstall, start, stop, restart, status, logs, print)", sub)
	return cli.ExitUsage
}

// scopeFlags adds --user/--system and returns a resolver.
func scopeFlags(fs *flag.FlagSet) func() (Scope, error) {
	user := fs.Bool("user", false, "")
	system := fs.Bool("system", false, "")
	return func() (Scope, error) {
		switch {
		case *user && *system:
			return 0, errors.New("--user and --system are exclusive")
		case *user:
			return ScopeUser, nil
		case *system:
			return ScopeSystem, nil
		}
		return ScopeAuto, nil
	}
}

func newFlags(env *cli.Env, name string) *flag.FlagSet {
	fs := cli.NewFlagSet(env, "service "+name, usage)
	return fs
}

func parse(env *cli.Env, fs *flag.FlagSet, args []string, maxArgs int) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK, false
		}
		return cli.ExitUsage, false
	}
	if fs.NArg() > maxArgs {
		env.Errorf("service %s: unexpected argument %q", strings.TrimPrefix(fs.Name(), "service "), fs.Arg(0))
		return cli.ExitUsage, false
	}
	return 0, true
}

func runInstall(ctx context.Context, env *cli.Env, sub string, args []string) int {
	fs := newFlags(env, sub)
	scope := scopeFlags(fs)
	var o Options
	fs.StringVar(&o.Signal, "signal", env.Globals.Signal, "")
	fs.StringVar(&o.Name, "name", "", "")
	fs.StringVar(&o.RunAs, "run-as", "", "")
	fs.StringVar(&o.Exe, "exe", "", "")
	fs.BoolVar(&o.Linger, "linger", false, "")
	fs.BoolVar(&o.NoSignal, "no-signal", false, "")
	fs.StringVar(&o.ConfigDir, "config-dir", "", "")
	fs.StringVar(&o.Role, "role", RoleHost, "")
	fs.BoolVar(&o.Unattended, "unattended", false, "")
	listen := fs.String("listen", "", "")
	alt := fs.String("alt", "", "")
	relay := fs.Bool("relay", false, "")
	relayPorts := fs.String("relay-ports", "", "")
	panel := fs.String("panel", "", "")
	if code, ok := parse(env, fs, args, 40); !ok {
		return code
	}
	if o.Role == RoleSignal {
		if !flagSet(fs, "signal") {
			o.Signal = "" // REMOHOP_SIGNAL means nothing to a rendezvous server
		}
		for _, f := range [][2]string{{"--listen", *listen}, {"--alt", *alt}, {"--relay-ports", *relayPorts}, {"--panel", *panel}} {
			if f[1] != "" {
				o.SignalArgs = append(o.SignalArgs, f[0], f[1])
			}
		}
		if *relay {
			o.SignalArgs = append(o.SignalArgs, "--relay")
		}
		o.SignalArgs = append(o.SignalArgs, fs.Args()...)
	} else if *listen != "" || *alt != "" || *relay || *relayPorts != "" || *panel != "" || fs.NArg() > 0 {
		env.Errorf("service: --listen, --alt, --relay, --relay-ports, --panel and extra arguments need --role signal")
		return cli.ExitUsage
	}
	if o.NoSignal && !flagSet(fs, "signal") {
		o.Signal = "" // an inherited REMOHOP_SIGNAL does not contradict --no-signal
	}
	var err error
	if o.Scope, err = scope(); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitUsage
	}
	if err := o.validate(); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitUsage
	}
	if o.Unattended && runtime.GOOS != "windows" {
		env.Errorf("service: --unattended is for the Windows service; elsewhere the host runs in the user's own session (--user)")
		return cli.ExitUsage
	}
	b, err := newBackend()
	if o.Role == RoleSignal {
		// A rendezvous server is a Linux/systemd service only; its unit
		// can still be printed anywhere.
		if sub != "print" && runtime.GOOS != "linux" {
			env.Errorf("service: running the rendezvous server as a service is supported on Linux with systemd only; elsewhere run `remohop signal` under your own supervisor")
			return cli.ExitError
		}
		if sub == "print" {
			b, err = newSystemd(), nil
		}
	}
	if err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	if sub == "print" {
		text, err := b.Print(o)
		if err != nil {
			env.Errorf("service: %v", err)
			return cli.ExitError
		}
		fmt.Fprint(env.Stdout, text)
		return cli.ExitOK
	}
	inst, err := b.Install(ctx, o)
	if err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	if o.Role == RoleSignal {
		printSignalInstalled(env.Stdout, inst, o)
		return cli.ExitOK
	}
	printInstalled(env.Stdout, inst)
	return cli.ExitOK
}

func printSignalInstalled(w io.Writer, i *Installed, o Options) {
	fmt.Fprintf(w, "Installed and started the rendezvous server %q (systemd system service).\n", i.Name)
	fmt.Fprintf(w, "  definition: %s\n  state:      %s (server key and ID table)\n", i.Path, stateDir(o))
	fmt.Fprintln(w, "Next:")
	args := strings.Join(o.SignalArgs, " ")
	fmt.Fprintf(w, "  remohop signal --print-firewall %s   open its ports (machine firewall and cloud security list)\n", args)
	fmt.Fprintf(w, "  sudo remohop signal --show-key --data %s   the key and pin line for clients\n", stateDir(o))
	fmt.Fprintln(w, "  remohop service status --role signal    remohop service logs --role signal -f")
	if p := o.panelListen(); p != "" {
		fmt.Fprintf(w, "  ssh -L %s SERVER, then open http://%s/   the web panel (settings: %s)\n", sshForward(p), p, o.panelEnvFile())
	}
}

func printInstalled(w io.Writer, i *Installed) {
	fmt.Fprintf(w, "Installed and started the %s service %q (%s scope).\n", i.Backend, i.Name, i.Scope)
	if i.Path != "" {
		fmt.Fprintf(w, "  definition: %s\n", i.Path)
	}
	remohop := "remohop"
	if i.ConfigDir != "" && runtime.GOOS == "windows" {
		fmt.Fprintln(w, "  This service has its own identity and password, stored in "+i.ConfigDir+".")
		fmt.Fprintln(w, "  From an elevated prompt, set REMOHOP_CONFIG_DIR to that directory before the remohop commands below.")
	}
	if i.ConfigDir != "" && runtime.GOOS != "windows" {
		prefix := "env"
		if i.Scope == ScopeSystem {
			prefix = "sudo env"
			if i.RunAs != "" {
				prefix = "sudo -u " + i.RunAs + " env"
			}
		}
		remohop = fmt.Sprintf("%s REMOHOP_CONFIG_DIR=%s remohop", prefix, shellQuote(i.ConfigDir))
		fmt.Fprintln(w, "  This service has its own identity and password, stored in "+i.ConfigDir+".")
		fmt.Fprintln(w, "  Address it by prefixing remohop commands as shown below.")
	}
	fmt.Fprintln(w, "Next:")
	fmt.Fprintf(w, "  %s status                       show its ID and password\n", remohop)
	fmt.Fprintf(w, "  %s passwd                       set a fixed password (no restart needed)\n", remohop)
	fmt.Fprintln(w, "  remohop service logs -f               watch it")
	for _, n := range i.Notes {
		fmt.Fprintf(w, "Note: %s\n", n)
	}
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " \t'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func runUninstall(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "uninstall")
	scope := scopeFlags(fs)
	name := fs.String("name", "", "")
	role := fs.String("role", RoleHost, "")
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	o, b, code := target(env, scope, *name, *role)
	if b == nil {
		return code
	}
	removed, err := b.Uninstall(ctx, o)
	switch {
	case errors.Is(err, errPackagedUnit):
		fmt.Fprintf(env.Stdout, "service stopped and disabled (%v)\n", err)
	case err != nil:
		env.Errorf("service: %v", err)
		return cli.ExitError
	case !removed:
		fmt.Fprintln(env.Stdout, "the service was not installed")
	default:
		fmt.Fprintln(env.Stdout, "service stopped and removed")
	}
	return cli.ExitOK
}

// target builds the options and backend for the commands that act on an
// existing service.
func target(env *cli.Env, scope func() (Scope, error), name, role string) (Options, backend, int) {
	o := Options{Name: name, Role: role}
	var err error
	if o.Scope, err = scope(); err == nil {
		err = o.validate()
	}
	if err != nil {
		env.Errorf("service: %v", err)
		return o, nil, cli.ExitUsage
	}
	b, err := newBackend()
	if err != nil {
		env.Errorf("service: %v", err)
		return o, nil, cli.ExitError
	}
	return o, b, 0
}

func runControl(ctx context.Context, env *cli.Env, action string, args []string) int {
	fs := newFlags(env, action)
	scope := scopeFlags(fs)
	name := fs.String("name", "", "")
	role := fs.String("role", RoleHost, "")
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	o, b, code := target(env, scope, *name, *role)
	if b == nil {
		return code
	}
	if err := b.Control(ctx, o, action); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	done := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[action]
	fmt.Fprintf(env.Stdout, "service %s\n", done)
	return cli.ExitOK
}

func runStatus(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "status")
	scope := scopeFlags(fs)
	name := fs.String("name", "", "")
	role := fs.String("role", RoleHost, "")
	asJSON := fs.Bool("json", false, "")
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	o, b, code := target(env, scope, *name, *role)
	if b == nil {
		return code
	}
	st, err := b.Status(ctx, o)
	if err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		out, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
	} else {
		printStatus(env.Stdout, st, o.Role == RoleSignal)
	}
	if !st.Running {
		return cli.ExitError
	}
	return cli.ExitOK
}

func printStatus(w io.Writer, st *Status, signal bool) {
	install := "remohop service install"
	if signal {
		install += " --role signal"
	}
	if !st.Installed {
		fmt.Fprintf(w, "not installed (%s)\n", install)
		return
	}
	state := st.State
	if st.Running && st.PID > 0 {
		state += fmt.Sprintf(", pid %d", st.PID)
	}
	fmt.Fprintf(w, "%s: %s (%s scope, %s)\n", cli.Sanitize(st.Name), cli.Sanitize(state), st.Scope, st.Backend)
	if st.Since != "" {
		fmt.Fprintf(w, "  since:    %s\n", cli.Sanitize(st.Since))
	}
	fmt.Fprintf(w, "  at boot:  %s\n", map[bool]string{true: "enabled", false: "not enabled"}[st.Enabled])
	if st.Unattended {
		fmt.Fprintf(w, "  unattended access: on (clients see and control the console session, sign-in and UAC screens included)\n")
	}
	if st.Path != "" {
		fmt.Fprintf(w, "  file:     %s\n", st.Path)
	}
	if st.Exe != "" {
		fmt.Fprintf(w, "  runs:     %s\n", cli.Sanitize(st.Exe))
	}
	if st.ExeMissing {
		fmt.Fprintf(w, "  WARNING:  that binary does not exist, so the service cannot start; reinstall it (%s) or remove it (remohop service uninstall%s)\n", install, strings.TrimPrefix(install, "remohop service install"))
	}
	if signal {
		fmt.Fprintf(w, "  state:    %s  (server key: sudo remohop signal --show-key --data %s)\n", st.ConfigDir, st.ConfigDir)
	} else if st.ConfigDir != "" {
		fmt.Fprintf(w, "  state:    %s  (sudo env REMOHOP_CONFIG_DIR=... remohop status shows its ID and password)\n", st.ConfigDir)
	} else {
		fmt.Fprintf(w, "  ID and password: remohop status\n")
	}
}

func runLogs(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "logs")
	scope := scopeFlags(fs)
	name := fs.String("name", "", "")
	role := fs.String("role", RoleHost, "")
	follow := fs.Bool("f", false, "")
	n := fs.Int("n", 50, "")
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	if *n < 0 || *n > 100000 {
		env.Errorf("service logs: -n must be between 0 and 100000")
		return cli.ExitUsage
	}
	o, b, code := target(env, scope, *name, *role)
	if b == nil {
		return code
	}
	if err := b.Logs(ctx, o, *n, *follow, env.Stdout); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

// runService is `remohop service run`, the entry point the Windows service
// manager starts. It is not for people and not listed in the usage.
func runService(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "run")
	signal := fs.String("signal", "", "")
	noSignal := fs.Bool("no-signal", false, "")
	configDir := fs.String("config-dir", "", "")
	unattended := fs.Bool("unattended", false, "")
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	o := Options{Signal: *signal, NoSignal: *noSignal, ConfigDir: *configDir, Unattended: *unattended}
	if err := o.validate(); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitUsage
	}
	// Point the host at the service's state directory before it reads any.
	dir, err := setServiceEnv(o.ConfigDir)
	if err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	logf, err := openServiceLog(dir)
	if err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	defer logf.Close()
	if err := runAsService(ctx, "RemoHop", o.hostArgs()[1:], o.Unattended, logf); err != nil {
		env.Errorf("service: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

// runSessionHelper is `remohop service session-helper`, the process the Windows
// service with unattended access starts in the console session. It is not
// for people and not listed in the usage; started any other way it refuses
// to run (it must be LocalSystem and receive its configuration from the
// service on standard input).
func runSessionHelper(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "session-helper")
	fs.Uint("session", 0, "") // informational: shows in process lists
	if code, ok := parse(env, fs, args, 0); !ok {
		return code
	}
	if err := unattended.RunHelper(ctx, os.Stdin, env.Stderr); err != nil {
		env.Errorf("service session-helper: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}
