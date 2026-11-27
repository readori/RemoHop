package hostd

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"golang.org/x/term"
)

// Config keys.
const (
	SettingPassword = "host-password"
	SettingListen   = "host-listen"
)

const hostUsage = `Usage: remohop host [--signal ADDR] [--password PW] [--listen ADDR:PORT]
                [--no-signal] [--no-pairing]
       remohop host status [--json] [--show-password]
       remohop host approve [--json] | remohop host approve N | remohop host deny N
       remohop host otp [new]
       remohop host passwd [--clear]
       remohop host authorize "KEY-LINE" [--name NAME]
       remohop host paired [--json]
       remohop host revoke NAME|ID|IDENTITY...
       remohop host grant NAME --allow LIST [--for DUR] ...   (see remohop host grant --help)
       remohop host grants | panic | resume | audit

Make this machine controllable. ` + "`remohop host`" + ` runs in the foreground: it
registers with the rendezvous server, prints the machine's 9-digit ID and a
password, and serves authenticated clients until interrupted. No account is
needed; whoever has the ID and the password can connect. Connections are
direct, peer to peer, unless both sides are behind NATs that cannot be
punched and the rendezvous server relays (only if its operator enabled
that with ` + "`remohop signal --relay`" + `; still end-to-end encrypted).

Flags:
  --signal ADDR       rendezvous server (default: the global --signal flag,
                      REMOHOP_SIGNAL, then ` + "`remohop config get signal`" + `)
  --no-signal         do not register anywhere; the host is then reachable
                      only by address (remohop ADDR:PORT ...), e.g. on a LAN
  --password PW       fixed password for this run (at least 8 characters).
                      Prefer ` + "`remohop host passwd`" + `, which keeps it off the
                      command line
  --listen ADDR:PORT  UDP address to bind (default: any free port; or
                      ` + "`remohop config set host-listen`" + `)
  --no-pairing        refuse ` + "`remohop <ID> pair`" + `: clients need the password every time

The password is checked with a PAKE, so it is never sent and cannot be
attacked offline; repeated wrong guesses lock the guesser out. Clients that
ran ` + "`remohop <ID> pair`" + ` hold a token instead and keep working when the
temporary password changes.

What connected clients may do is yours to decide, also while the host runs:
  remohop config feature NAME on|off   shell, file, forward, clip, screen, input, info,
                                   vpn, serial, usb
  remohop config ask on|off            every password login needs your approval
  remohop config ask-paired on|off     ... paired clients and authorised keys too
  remohop config otp on|off            the temporary password works once

Subcommands (they talk to the running host; same user only):
  status     ID, password mode, rendezvous state, sessions (same as ` + "`remohop status`" + `)
  sessions   list live sessions (--json for scripts); authorizer-admitted ones
             show "via NAME" and their grant
  approve    list connections waiting for approval; ` + "`approve N`" + ` / ` + "`deny N`" + ` answer
             one. With a terminal, ` + "`remohop host`" + ` itself asks y/n instead.
  otp        print the current temporary password; ` + "`otp new`" + ` replaces it
  passwd     set the fixed password (asked twice, not echoed); --clear removes
             it. Takes effect at once, also on a running host.
  authorize  let a client log in with its identity key alone, no password:
             pass the line that ` + "`remohop key export`" + ` printed on the client
  paired     list paired clients and authorised keys
  revoke     remove pairings, authorised keys or grants by name or ID, or every
             pairing and key of one identity by its fingerprint; removing an
             authorised key also removes the pairings its sessions minted.
             Open sessions that used them are closed (grants: at once)
  grant      give an AI agent (or anyone) limited, expiring access: allowed
             services, file roots, commands, approval of each action,
             recording. ` + "`remohop host grant --help`" + ` explains; docs/agents.md too
  grants     list grants; ` + "`panic`" + ` suspends them all and ends their sessions,
             ` + "`resume`" + ` lifts that; ` + "`audit --grant NAME`" + ` shows what one did

Examples:
  remohop host
  remohop host --no-signal --listen :21118        # LAN only: remohop 192.168.1.5:21118 shell
  remohop config ask on && remohop host approve
  remohop host authorize "remohop-key Yk3...Q laptop"
  remohop host revoke laptop
`

const logUsage = `Usage: remohop log [N] [--json]

Show the last N (default 50) entries of this machine's host audit log:
logins accepted and refused (with the reason), approval decisions, the first
use of each feature in a session, and session ends. The log is kept in the
config directory, readable by you only: audit.jsonl (rotated at 1 MB, four
old files kept) and, apart so that floods cannot push the records out,
audit-refused.jsonl for refused logins and grant refusals. Past 10 refusals
per source (or granted session) a minute, and 100 in all, the rest are
counted and written as one entry with "suppressed" (their number).
`

const statusUsage = `Usage: remohop status [--json] [--show-password]

Show what this machine is doing as a RemoHop host: whether the host daemon is
running, its ID, password mode, rendezvous state, NAT type, features that
are switched off, approval mode and live sessions with their path (lan,
direct, punched or relayed), plus how many hosts this machine has saved as
a client.

  --show-password   include the temporary or one-time password. A fixed
                    password is never shown.
  --json            machine-readable output
`

func init() {
	config.RegisterSetting(config.Setting{
		Key:    SettingPassword,
		Help:   "fixed password for `remohop host` (default: a new random password on every start)",
		Secret: true,
		Validate: func(v string) error {
			return auth.CheckPassword(auth.NormalizePassword(v))
		},
	})
	config.RegisterSetting(config.Setting{
		Key:  SettingListen,
		Help: "UDP address `remohop host` binds, ADDR:PORT (default: any free port)",
	})
	for _, f := range perm.Features() {
		config.RegisterSetting(config.Setting{
			Key:      FeatureSetting(f),
			Help:     "allow connected clients to use " + featureHelp(f),
			Validate: config.ValidateOnOff,
			Default:  "on",
		})
	}
	config.RegisterSetting(config.Setting{Key: SettingAsk, Default: "off", Validate: config.ValidateOnOff,
		Help: "password logins need approval at the host (`remohop host approve`)"})
	config.RegisterSetting(config.Setting{Key: SettingAskPaired, Default: "off", Validate: config.ValidateOnOff,
		Help: "with ask on: paired clients and authorised keys need approval too"})
	config.RegisterSetting(config.Setting{Key: SettingOTP, Default: "off", Validate: config.ValidateOnOff,
		Help: "replace the host's temporary password after every login with it"})
	cli.Register(&cli.Command{Name: "status", Summary: "show this machine's host daemon, sessions and saved hosts", Usage: statusUsage,
		Run: func(_ context.Context, env *cli.Env, args []string) int { return runStatus(env, args, false) }})
	cli.Register(&cli.Command{Name: "log", Summary: "show the host audit log", Usage: logUsage, Run: runLog})
	// Short forms of the host's own commands.
	cli.Register(&cli.Command{Name: "otp", Summary: "print (or with `new`, replace) the running host's temporary password",
		Usage: "Usage: remohop otp [new]\n\nSame as `remohop host otp`: print the running host's temporary password, or replace it.\n",
		Run:   func(_ context.Context, env *cli.Env, args []string) int { return runOTP(env, args) }})
	cli.Register(&cli.Command{Name: "passwd", Summary: "set or clear this host's fixed password",
		Usage: "Usage: remohop passwd [--clear]\n\nSame as `remohop host passwd`: set the fixed password (asked twice, not echoed), or remove it.\nA running host, including a service instance, accepts the change at once.\n",
		Run:   func(_ context.Context, env *cli.Env, args []string) int { return runPasswd(env, args) }})
	cli.Register(&cli.Command{Name: "authorize", Summary: "let a client's key log in to this host without a password",
		Usage: "Usage: remohop authorize \"KEY-LINE\" [--name NAME]\n\nSame as `remohop host authorize`: KEY-LINE is what `remohop key export` printed on the client.\n",
		Run:   func(_ context.Context, env *cli.Env, args []string) int { return runAuthorize(env, args) }})
	cli.Register(&cli.Command{
		Name:    "host",
		Summary: "make this machine controllable (host daemon)",
		Usage:   hostUsage,
		Run:     runHost,
	})
}

func runHost(ctx context.Context, env *cli.Env, args []string) int {
	if code, ok := runGrantCmd(env, args); ok {
		return code
	}
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return runStatus(env, args[1:], true)
		case "sessions":
			return runSessions(env, args[1:])
		case "approve", "deny":
			return runApprove(env, args[0] == "approve", args[1:])
		case "otp":
			return runOTP(env, args[1:])
		case "passwd":
			return runPasswd(env, args[1:])
		case "authorize":
			return runAuthorize(env, args[1:])
		case "paired":
			return runPaired(env, args[1:])
		case "revoke":
			return runRevoke(env, args[1:])
		}
	}
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, hostUsage) }
	sig := fs.String("signal", env.Globals.Signal, "")
	noSignal := fs.Bool("no-signal", false, "")
	password := fs.String("password", "", "")
	listen := fs.String("listen", "", "")
	noPairing := fs.Bool("no-pairing", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("host: unexpected argument %q (subcommands: status, approve, deny, otp, passwd, authorize, paired, revoke, grant, grants, panic, resume, audit)", fs.Arg(0))
		return cli.ExitUsage
	}

	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	settings, err := config.All()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	if *listen == "" {
		*listen = settings[SettingListen]
	}

	applyTermKeep(settings)
	cfg := Config{Dir: dir, Listen: *listen, Password: *password, NoPairing: *noPairing, Control: true}
	// With a terminal, the person at the host answers approval requests
	// right here; without one they use `remohop host approve`.
	tty, interactive := env.Stdin.(*os.File)
	interactive = interactive && term.IsTerminal(int(tty.Fd()))
	var shown atomic.Int64 // the request the terminal asked about last
	cfg.OnApproval = func(a Approval) {
		shown.Store(int64(a.N))
		who := "identity " + a.Identity
		if a.Name != "" {
			who = fmt.Sprintf("%q, %s", cli.Sanitize(a.Name), who)
		}
		if a.Device != "" {
			who = fmt.Sprintf("calls itself %q, %s", cli.Sanitize(a.Device), who)
		}
		if a.Action != "" {
			// The action is the client's text: shown on one line, with
			// line breaks and other control characters escaped.
			if interactive {
				fmt.Fprintf(env.Stderr, "\nRequest #%d: grant %s (%s) wants to %s\n%s",
					a.N, a.Grant, who, cli.OneLine(a.Action), allowPrompt(a.N, askTimeout(cfg)))
			} else {
				fmt.Fprintf(env.Stderr, "remohop: request #%d: grant %s wants to %s: `remohop host approve %d` or `remohop host deny %d`\n",
					a.N, a.Grant, cli.OneLine(a.Action), a.N, a.N)
			}
			return
		}
		if interactive {
			fmt.Fprintf(env.Stderr, "\nConnection request #%d from %s (%s, %s login).\n%s",
				a.N, a.From, who, a.Auth, allowPrompt(a.N, askTimeout(cfg)))
		} else {
			fmt.Fprintf(env.Stderr, "remohop: connection request #%d from %s (%s) is waiting: `remohop host approve %d` or `remohop host deny %d`\n",
				a.N, a.From, who, a.N, a.N)
		}
	}
	if !*noSignal {
		cfg.Signal, cfg.SignalOptions, err = signal.Resolve(*sig)
		if err != nil {
			env.Errorf("host: %v\n     (or use --no-signal to serve direct connections only)", err)
			return cli.ExitUsage
		}
		if cfg.Signal == "" {
			// Only the websocket bridge is configured.
			cfg.Signal = signal.Display("", cfg.SignalOptions)
		} else if _, _, err := signal.SplitAddr(cfg.Signal); err != nil {
			env.Errorf("host: %v", err)
			return cli.ExitUsage
		}
	}
	level := slog.LevelInfo
	if env.Globals.Verbose {
		level = slog.LevelDebug
	}
	cfg.Logger = slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level}))

	// Two hosts on one identity would evict each other from the
	// rendezvous server in a loop. The lock closes the window between
	// checking for a running host and recording this one.
	statusPath := filepath.Join(dir, statusFile)
	unlock, err := config.Lock(statusPath)
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	if st, _ := ReadStatus(dir); st != nil {
		unlock()
		env.Errorf("host: already running (pid %d); see `remohop host status`", st.PID)
		return cli.ExitError
	}
	h, err := New(cfg)
	if err != nil {
		unlock()
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	record := func() {
		st := h.Status()
		if err := writeStatus(dir, st); err != nil {
			cfg.Logger.Warn("could not write the status file", "err", err)
		}
	}
	record()
	unlock()
	defer os.Remove(statusPath)
	h.OnChange(record)

	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	if interactive {
		go answerApprovals(env, h, tty, func() int { return int(shown.Load()) })
	}

	if cfg.Signal != "" {
		select {
		case <-h.Registered():
		case <-time.After(8 * time.Second):
			env.Errorf("host: still trying to reach the rendezvous server %s; the ID will be printed once registered", cfg.Signal)
		case err := <-done:
			return hostExit(env, err)
		}
	}
	printReady(env, h, cfg)
	if cfg.Signal != "" && h.ID() == "" {
		go func() {
			select {
			case <-h.Registered():
				fmt.Fprintf(env.Stdout, "  ID:        %s\n", signal.FormatID(h.ID()))
			case <-ctx.Done():
			}
		}()
	}
	return hostExit(env, <-done)
}

func hostExit(env *cli.Env, err error) int {
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

func askTimeout(cfg Config) time.Duration {
	if cfg.AskTimeout > 0 {
		return cfg.AskTimeout
	}
	return DefaultAskTimeout
}

// allowPrompt asks for the answer to request n at the terminal.
func allowPrompt(n int, timeout time.Duration) string {
	return fmt.Sprintf("Allow #%d? Type y or n and Enter; while other requests wait, y %d or n %d (refused in %s): ", n, n, n, timeout)
}

// answerApprovals reads the owner's answers from the terminal: "y N" or
// "n N" answers request N. A bare "y" or "n" answers only when exactly one
// request is waiting and it is the one the terminal asked about last;
// otherwise the owner is asked to name it, so an answer never lands on a
// request other than the one just read. Every answer is confirmed with the
// request's number and what it was about.
func answerApprovals(env *cli.Env, h *Host, tty *os.File, shown func() int) {
	sc := bufio.NewScanner(tty)
	for sc.Scan() {
		if line := approvalAnswer(h, sc.Text(), shown()); line != "" {
			fmt.Fprintln(env.Stderr, line)
		}
	}
}

// approvalAnswer applies one line typed at the host's terminal and returns
// what to tell the owner ("" for a line that is no answer).
func approvalAnswer(h *Host, text string, shown int) string {
	f := strings.Fields(strings.ToLower(text))
	if len(f) == 0 {
		return ""
	}
	var allow bool
	switch f[0] {
	case "y", "yes":
		allow = true
	case "n", "no":
	default:
		return ""
	}
	n := 0
	if len(f) > 1 {
		var err error
		if n, err = strconv.Atoi(f[1]); err != nil || n <= 0 {
			return fmt.Sprintf("remohop: %q is no request number; type y N or n N", f[1])
		}
	} else {
		pending := h.Pending()
		switch {
		case len(pending) == 0:
			return "remohop: no request is waiting"
		case len(pending) > 1:
			nums := make([]string, len(pending))
			for i, a := range pending {
				nums[i] = strconv.Itoa(a.N)
			}
			return fmt.Sprintf("remohop: %d requests are waiting (%s); type y N or n N for the one you mean (`remohop host approve` lists them)",
				len(pending), strings.Join(nums, ", "))
		case pending[0].N != shown:
			return fmt.Sprintf("remohop: the request asked about last is no longer waiting; request #%d is: type y %d or n %d",
				pending[0].N, pending[0].N, pending[0].N)
		}
		n = pending[0].N
	}
	a, err := h.DecideShown(n, allow)
	if err != nil {
		return "remohop: " + err.Error()
	}
	verb := "denied"
	if allow {
		verb = "approved"
	}
	return "remohop: " + verb + " " + describeApproval(a)
}

// describeApproval names a request for a confirmation: its number and
// what it asked for, on one line.
func describeApproval(a Approval) string {
	switch {
	case a.Action != "":
		return fmt.Sprintf("request #%d: grant %s wants to %s", a.N, cli.Sanitize(a.Grant), cli.OneLine(a.Action))
	case len(a.Access) > 0:
		return fmt.Sprintf("request #%d: session %d asks for %s", a.N, a.Session, featureList(a.Access))
	}
	return fmt.Sprintf("connection request #%d from %s (identity %s)", a.N, a.From, a.Identity)
}

func passwordLine(password, mode string) string {
	switch mode {
	case "fixed":
		return "(fixed password; not shown)"
	case "fixed+one-time":
		return password + "  (one-time, changes after each login; the fixed password works too)"
	case "one-time":
		return password + "  (one-time, changes after each login)"
	}
	return password + "  (temporary, changes on restart)"
}

// stdoutIsTerminal reports whether w is a terminal. The banner shows the
// password only then: a service's stdout is its log (the journal, a launchd
// log file, service.log), which others may read or paste into bug reports.
var stdoutIsTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// hiddenPasswordLine replaces the password in a banner that goes to a log.
func hiddenPasswordLine(mode string) string {
	if mode == "fixed" {
		return passwordLine("", mode)
	}
	return "(not shown: output is not a terminal; `remohop status --show-password` prints it)"
}

func printReady(env *cli.Env, h *Host, cfg Config) {
	w := env.Stdout
	fmt.Fprintln(w, "RemoHop host ready")
	if id := h.ID(); id != "" {
		fmt.Fprintf(w, "  ID:        %s\n", signal.FormatID(id))
	}
	password, mode := h.TempPassword()
	if stdoutIsTerminal(w) {
		fmt.Fprintf(w, "  Password:  %s\n", passwordLine(password, mode))
	} else {
		fmt.Fprintf(w, "  Password:  %s\n", hiddenPasswordLine(mode))
	}
	direct := h.DirectAddrs()
	for i, ap := range direct {
		label := "  Direct:   "
		if i > 0 {
			label = "            "
		}
		fmt.Fprintf(w, "%s %s\n", label, ap)
	}
	fmt.Fprintf(w, "  Identity:  %s\n", auth.Fingerprint(h.PublicKey()))
	st := h.Status()
	if len(st.FeaturesOff) > 0 {
		fmt.Fprintf(w, "  Not allowed: %s\n", strings.Join(st.FeaturesOff, ", "))
	}
	if st.Ask != "off" {
		fmt.Fprintf(w, "  Connections need your approval here (ask: %s)\n", st.Ask)
	}
	switch {
	case cfg.Signal == "" && len(direct) > 0:
		fmt.Fprintf(w, "No rendezvous server: connect with `remohop %s <command>` from a machine that can reach that address.\n", direct[0])
	case cfg.Signal == "":
		fmt.Fprintf(w, "No rendezvous server: connect with `remohop ADDR:%d <command>`.\n", portOf(h))
	case h.ID() != "":
		fmt.Fprintf(w, "Connect with `remohop %s <command>`.\n", h.ID())
	}
}

func portOf(h *Host) int {
	if ua, ok := h.Addr().(*net.UDPAddr); ok {
		return ua.Port
	}
	return 0
}

func tokenStore() (*auth.TokenStore, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)), nil
}

func runPaired(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host paired", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		env.Errorf("usage: remohop host paired [--json]")
		return cli.ExitUsage
	}
	store, err := tokenStore()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	toks, err := store.List()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		// Never the secrets: this output is meant to be passed around.
		type row struct {
			ID       string    `json:"token_id"`
			Kind     string    `json:"kind"`
			Name     string    `json:"name"`
			Identity string    `json:"identity"`
			Created  time.Time `json:"created"`
			// MintedBy is the authorised key whose session minted
			// this pairing; the pairing goes when that key goes.
			MintedBy string `json:"minted_by,omitempty"`
		}
		rows := make([]row, 0, len(toks))
		for _, t := range toks {
			kind := "pairing"
			if len(t.Secret) == 0 {
				kind = "key"
			}
			rows = append(rows, row{t.ID, kind, t.Name, auth.Fingerprint(t.ClientKey), t.Created, parentName(toks, t)})
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	if len(toks) == 0 {
		fmt.Fprintln(env.Stdout, "no paired clients")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tKIND\tIDENTITY\tADDED")
	for _, t := range toks {
		kind := "pairing"
		if len(t.Secret) == 0 {
			kind = "key"
		}
		if p := parentName(toks, t); p != "" {
			kind += " (via " + cli.Sanitize(p) + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", cli.Sanitize(t.Name), cli.Sanitize(t.ID), kind, auth.Fingerprint(t.ClientKey), t.Created.Local().Format("2006-01-02 15:04"))
	}
	tw.Flush()
	return cli.ExitOK
}

// parentName names the authorised key that minted pairing t, if any.
func parentName(toks []auth.Token, t auth.Token) string {
	if t.Parent == "" {
		return ""
	}
	for _, p := range toks {
		if p.ID == t.Parent {
			if p.Name != "" {
				return p.Name
			}
			return p.ID
		}
	}
	return t.Parent
}

func runRevoke(env *cli.Env, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		env.Errorf("usage: remohop host revoke NAME|TOKEN-ID|IDENTITY...")
		return cli.ExitUsage
	}
	store, err := tokenStore()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	code := cli.ExitOK
	dir, _ := config.Dir()
	for _, ref := range args {
		if handled, c := revokeGrantRef(env, dir, ref); handled {
			code = max(code, c)
			continue
		}
		n, err := store.Revoke(ref)
		switch {
		case err != nil:
			env.Errorf("host: %v", err)
			return cli.ExitError
		case n == 0:
			env.Errorf("host: no pairing named %q (see `remohop host paired`)", ref)
			code = cli.ExitError
		default:
			fmt.Fprintf(env.Stdout, "revoked %q (%d pairing(s))\n", cli.Sanitize(ref), n)
		}
	}
	return code
}

// featureHelp describes a feature for `remohop config`. It is written out rather
// than built from the services registered to the feature: settings are
// declared before every service package has registered itself.
func featureHelp(f perm.Feature) string {
	switch f {
	case perm.Shell:
		return "shells, commands and background jobs"
	case perm.File:
		return "files: listing, copying, sync, send and mounts"
	case perm.Forward:
		return "port forwarding (TCP and UDP) and Wake-on-LAN"
	case perm.Clip:
		return "the clipboard and its history"
	case perm.Screen:
		return "screenshots and viewing the desktop"
	case perm.Input:
		return "keyboard and mouse control"
	case perm.Info:
		return "system information"
	case perm.VPN:
		return "a VPN tunnel to this machine and its LANs (exit and wider routes also need vpn-exit)"
	case perm.Serial:
		return "this machine's serial ports"
	case perm.USB:
		return "this machine's USB devices over USB/IP (each must also be allowed with remohop usb allow)"
	}
	return "the " + string(f) + " feature"
}
