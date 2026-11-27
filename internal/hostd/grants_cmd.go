package hostd

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/grant"
)

const grantUsage = `Usage: remohop host grant NAME --allow LIST [--for DURATION] [--cmd COMMAND]...
                      [--approve each|first|none] [--sessions N] [--record]
                      [--key "KEY-LINE"]
       remohop host grants [--json]
       remohop host revoke NAME
       remohop host panic [--all]
       remohop host resume [NAME...]
       remohop host audit [--grant NAME] [N] [--json]

Hand this machine to an AI agent (or anyone) with less than full access. A
grant is a credential with limits that the host enforces: what may be used,
which files, which commands, until when, how many sessions at once, and
whether you approve each action. ` + "`remohop host grant`" + ` prints a code; on the agent's
machine ` + "`remohop peers redeem TARGET CODE`" + ` stores it, and from then on ` + "`remohop mcp`" + `
(and every remohop command) there works within the grant. The first machine to
redeem the code is bound to it; nobody else can use it afterwards.

Flags:
  --allow LIST     what the grant allows, comma-separated (repeatable):
                     shell              commands, jobs and interactive terminals
                     shell:run-only     commands and jobs, no terminal
                     shell:cmd=PROGRAM  only commands starting with PROGRAM
                                        (e.g. shell:cmd=git, "shell:cmd=go test")
                     files              read and write any file
                     files:read         read any file
                     files:read:PATH    read below PATH only
                     files:write:PATH   read and write below PATH only
                     screenshot         screenshots only
                     screen             screenshots and desktop viewing
                     desktop            screen plus keyboard and mouse
                     input, clip, info, forward, vpn, serial, usb
  --for DURATION   how long the grant is valid (default 1h; e.g. 30m, 2h, 24h)
  --cmd COMMAND    allow this command (program and optional leading
                   arguments); repeatable; implies shell:run-only. Commands
                   never run at, below or above a files:write root: a
                   writable root where git, go, make or npm runs means
                   arbitrary code
  --approve MODE   each: you approve every command and file change
                   first: you approve the first one of each session
                   none: nothing is asked (default)
  --sessions N     concurrent sessions allowed (default 2)
  --record         record command and terminal output (asciicast v2) in
                   the config directory's recordings/NAME/
  --key KEY-LINE   bind the code to this client identity (what
                   ` + "`remohop key export`" + ` printed there) instead of the first redeemer

The host's feature switches (` + "`remohop config feature`" + `) still apply on top.

Subcommands:
  grants   list grants: state, expiry, limits, live sessions
  revoke   delete a grant; its live sessions end at once
  panic    suspend every grant, those an authorizer (RemoHop Teams) gives
           included, and end every granted session (--all: end every
           session, full-access ones too). It lasts across restarts of the
           host. Undo with resume.
  resume   lift the suspension of the named grants, or of all (and then of
           the grants authorizers give, too)
  audit    show audit log entries (of one grant with --grant) and its
           recordings; N limits the entries shown (default 100)

Examples:
  remohop host grant claude-session --for 2h --allow shell:run-only,files:read:~/project,screenshot
  remohop host grant ci-bot --for 8h --cmd "go test" --cmd "git status" --allow files:read:~/src,files:write:~/ci-reports --approve first
  remohop host revoke claude-session
`

// runGrantCmd dispatches the grant subcommands of `remohop host`; ok is false
// when args[0] is not one of them.
func runGrantCmd(env *cli.Env, args []string) (code int, ok bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "grant":
		return runGrant(env, args[1:]), true
	case "grants":
		return runGrants(env, args[1:]), true
	case "panic":
		return runPanic(env, args[1:]), true
	case "resume":
		return runResume(env, args[1:]), true
	case "audit":
		return runAuditCmd(env, args[1:]), true
	}
	return 0, false
}

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// parseInterspersed parses flags that may come before or after positional
// arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func runGrant(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host grant", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, grantUsage) }
	var allow, cmds listFlag
	fs.Var(&allow, "allow", "")
	fs.Var(&cmds, "cmd", "")
	name := fs.String("name", "", "")
	dur := fs.Duration("for", time.Hour, "")
	approve := fs.String("approve", grant.ApproveNone, "")
	sessions := fs.Int("sessions", grant.DefaultMaxSessions, "")
	record := fs.Bool("record", false, "")
	keyLine := fs.String("key", "", "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cli.ExitOK
		}
		return cli.ExitUsage
	}
	switch {
	case len(pos) == 1 && *name == "":
		*name = pos[0]
	case len(pos) > 1 || (len(pos) == 1 && *name != pos[0]):
		env.Errorf("host grant: give the grant's name once (unexpected %q)", strings.Join(pos, " "))
		return cli.ExitUsage
	}
	if *name == "" {
		env.Errorf("host grant: name the grant (remohop host grant NAME --allow ...)")
		return cli.ExitUsage
	}
	if *sessions < 1 {
		env.Errorf("host grant: --sessions must be at least 1")
		return cli.ExitUsage
	}
	req := GrantRequest{Name: *name, For: *dur, Allow: allow, Commands: cmds, Approve: *approve, MaxSessions: *sessions, Record: *record}
	if *keyLine != "" {
		key, _, err := auth.ParseKeyLine(*keyLine)
		if err != nil {
			env.Errorf("host grant: --key: %v", err)
			return cli.ExitUsage
		}
		req.ClientKey = key
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host grant: %v", err)
		return cli.ExitError
	}
	_, existed, _ := OpenGrantStore(dir).Get(*name)
	g, code, err := IssueGrant(dir, req)
	if err != nil {
		env.Errorf("host grant: %v", err)
		return cli.ExitUsage
	}
	if existed {
		controlCall(dir, controlRequest{Op: "grants"})
	}
	rules, _ := g.Rules()
	w := env.Stdout
	fmt.Fprintf(w, "granted %s until %s (%s)\n", g.Name, g.Expires.Local().Format("2006-01-02 15:04"), dur.String())
	for _, s := range rules.Summary() {
		fmt.Fprintf(w, "  allows   %s\n", s)
	}
	fmt.Fprintf(w, "  approve  %s; at most %d session(s); recording %s\n", g.Approve, g.MaxSessions, onOff(g.Record))
	if existed {
		fmt.Fprintln(w, "  (replaces the earlier grant of that name; its code no longer works)")
	}
	warnConfigDir(env, rules, dir)
	target := "TARGET"
	if st, _ := LiveStatus(dir); st != nil {
		switch {
		case st.ID != "":
			target = st.ID
		case len(st.Direct) > 0:
			target = st.Direct[0]
		case st.Listen != "":
			target = st.Listen
		}
	}
	bound := "the first machine to redeem it is bound to it"
	if req.ClientKey != nil {
		bound = "it works only from identity " + auth.Fingerprint(req.ClientKey)
	}
	fmt.Fprintf(w, "\nCode (shown once; %s):\n  %s\n", bound, code)
	fmt.Fprintf(w, "\nOn the agent's machine:\n  remohop peers redeem %s %s\n  claude mcp add remohop -- remohop mcp --target %s\n", target, code, target)
	if target == "TARGET" {
		fmt.Fprintln(w, "(TARGET is this host's 9-digit ID or ADDR:PORT; start `remohop host` to get one)")
	}
	fmt.Fprintf(w, "\nEnd it early with `remohop host revoke %s`, or everything at once with `remohop host panic`.\n", g.Name)
	return cli.ExitOK
}

// warnConfigDir tells whoever issues a grant that its file roots contain
// the host's config directory. The host refuses every granted access to
// it (grant.Protect), but the owner may have meant to share less.
func warnConfigDir(env *cli.Env, rules *grant.Rules, dir string) {
	if rules == nil {
		return
	}
	dirs := []string{dir}
	if d, err := config.Dir(); err == nil && d != dir {
		dirs = append(dirs, d)
	}
	roots := rules.RootsContaining(dirs)
	if rules.ReadAll || rules.WriteAll {
		roots = []string{"every file (files or files:read)"}
	}
	if len(roots) == 0 {
		return
	}
	fmt.Fprintf(env.Stderr, "remohop: warning: the grant's file access (%s) contains RemoHop's configuration directory %s.\n"+
		"  The host refuses the grant everything in it, and changes to the directories above it, because it holds\n"+
		"  the grants, the pairings, the identity key and the temporary password. Consider a narrower root.\n",
		cli.Sanitize(strings.Join(roots, ", ")), cli.Sanitize(dir))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// GrantStatus is a grant as listed by `remohop host grants --json`.
type GrantStatus struct {
	Name        string      `json:"name"`
	State       grant.State `json:"state"`
	Created     time.Time   `json:"created"`
	Expires     time.Time   `json:"expires"`
	Allow       []string    `json:"allow"`
	Commands    []string    `json:"commands,omitempty"`
	Summary     []string    `json:"summary"`
	Approve     string      `json:"approve"`
	MaxSessions int         `json:"max_sessions"`
	Record      bool        `json:"record,omitempty"`
	// Identity is the fingerprint of the client the credential is bound
	// to; empty until it is redeemed.
	Identity string `json:"identity,omitempty"`
	// Sessions is the number of live sessions, when a host is running.
	Sessions int `json:"sessions"`
}

func runGrants(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host grants", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		env.Errorf("usage: remohop host grants [--json]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	gs, err := OpenGrantStore(dir).List()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	toks, _ := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)).List()
	live := map[string]int{}
	if st, _ := LiveStatus(dir); st != nil {
		for _, c := range st.Clients {
			if c.Grant != "" {
				live[c.Grant]++
			}
		}
	}
	now := time.Now()
	rows := make([]GrantStatus, 0, len(gs))
	for _, g := range gs {
		r := GrantStatus{Name: g.Name, State: g.StateAt(now), Created: g.Created, Expires: g.Expires, Allow: g.Allow,
			Commands: g.Commands, Approve: g.Approve, MaxSessions: g.MaxSessions, Record: g.Record, Sessions: live[g.Name]}
		if rules, err := g.Rules(); err == nil {
			r.Summary = rules.Summary()
		}
		for _, t := range toks {
			if t.ID == g.TokenID && len(t.ClientKey) > 0 {
				r.Identity = auth.Fingerprint(t.ClientKey)
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		printJSON(env, rows)
		return cli.ExitOK
	}
	if GrantsHeld(dir) {
		fmt.Fprintln(env.Stdout, "on hold after `remohop host panic`: every grant given by an authorizer (RemoHop Teams); `remohop host resume` lifts the hold")
	}
	if len(rows) == 0 {
		fmt.Fprintln(env.Stdout, "no grants (issue one with `remohop host grant NAME --allow ...`)")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATE\tEXPIRES\tSESSIONS\tAPPROVE\tBOUND TO\tALLOWS")
	for _, r := range rows {
		bound := r.Identity
		if bound == "" {
			bound = "(not redeemed)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%s\t%s\t%s\n", r.Name, r.State, r.Expires.Local().Format("2006-01-02 15:04"),
			r.Sessions, r.MaxSessions, r.Approve, bound, cli.Sanitize(strings.Join(r.Allow, ",")+cmdSuffix(r.Commands)))
	}
	tw.Flush()
	return cli.ExitOK
}

func cmdSuffix(cmds []string) string {
	if len(cmds) == 0 {
		return ""
	}
	return " cmd=" + strings.Join(cmds, "|")
}

// revokeGrantRef revokes the grant named ref, if there is one, and ends its
// sessions on a running host. handled is false when no grant has that name.
func revokeGrantRef(env *cli.Env, dir, ref string) (handled bool, code int) {
	if _, ok, _ := OpenGrantStore(dir).Get(ref); !ok {
		return false, 0
	}
	// The grant's live sessions, counted before it goes: the running host's
	// own periodic check may end them before the "grants" request below
	// does, and then that reply counts none.
	live := 0
	if rep, err := controlCall(dir, controlRequest{Op: "status"}); err == nil && rep.Status != nil {
		for _, c := range rep.Status.Clients {
			if c.Grant == ref {
				live++
			}
		}
	}
	if _, err := RevokeGrant(dir, ref); err != nil {
		env.Errorf("host: %v", err)
		return true, cli.ExitError
	}
	msg := fmt.Sprintf("revoked grant %s", ref)
	closed := 0
	if rep, err := controlCall(dir, controlRequest{Op: "grants"}); err == nil {
		closed = rep.Closed
	}
	if closed = max(closed, live); closed > 0 {
		msg += fmt.Sprintf("; ended %d live session(s)", closed)
	}
	fmt.Fprintln(env.Stdout, msg)
	return true, cli.ExitOK
}

func runPanic(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host panic", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	all := fs.Bool("all", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		env.Errorf("usage: remohop host panic [--all]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	// Suspend first: a session that slips in between is refused by the
	// suspended grant, and one already in is ended below.
	changed, serr := SuspendGrants(dir, true)
	closed := 0
	rep, cerr := controlCall(dir, controlRequest{Op: "panic", All: *all})
	if cerr == nil {
		closed = rep.Closed
	}
	if serr != nil {
		env.Errorf("host panic: could not suspend the grants: %v", serr)
	}
	if cerr != nil && !errors.Is(cerr, errNotRunning) {
		env.Errorf("host panic: could not reach the running host: %v (sessions end within %s once it sees the suspended grants)", cerr, revokeCheck)
	}
	what := "granted"
	if *all {
		what = "all"
	}
	fmt.Fprintf(env.Stdout, "suspended %d grant(s); ended %d %s session(s)\n", len(changed), closed, what)
	fmt.Fprintln(env.Stdout, "grants given by an authorizer (RemoHop Teams) are on hold too, also across restarts")
	fmt.Fprintln(env.Stdout, "resume with `remohop host resume` (all, and the hold) or `remohop host resume NAME`")
	if serr != nil {
		return cli.ExitError
	}
	return cli.ExitOK
}

func runResume(env *cli.Env, args []string) int {
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	held := GrantsHeld(dir)
	changed, err := SuspendGrants(dir, false, args...)
	if err != nil {
		env.Errorf("host resume: %v", err)
		return cli.ExitError
	}
	if len(changed) > 0 {
		fmt.Fprintf(env.Stdout, "resumed %s\n", strings.Join(changed, ", "))
	}
	switch {
	case held && len(args) == 0:
		fmt.Fprintln(env.Stdout, "lifted the hold on grants given by authorizers (RemoHop Teams)")
	case held:
		fmt.Fprintln(env.Stdout, "grants given by authorizers (RemoHop Teams) stay on hold until `remohop host resume` without names")
	case len(changed) == 0:
		fmt.Fprintln(env.Stdout, "no suspended grant to resume")
	}
	return cli.ExitOK
}

func runAuditCmd(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host audit", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	name := fs.String("grant", "", "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs, args)
	n := 100
	if err == nil && len(pos) == 1 {
		n, err = strconv.Atoi(pos[0])
		if err == nil && n <= 0 {
			err = errors.New("N must be positive")
		}
	}
	if err != nil || len(pos) > 1 {
		env.Errorf("usage: remohop host audit [--grant NAME] [N] [--json]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host audit: %v", err)
		return cli.ExitError
	}
	entries, err := GrantAudit(dir, *name, n)
	if err != nil {
		env.Errorf("host audit: %v", err)
		return cli.ExitError
	}
	var casts []string
	if *name != "" {
		casts, _ = Recordings(dir, *name)
	}
	if *asJSON {
		if entries == nil {
			entries = []AuditEntry{}
		}
		printJSON(env, map[string]any{"entries": entries, "recordings": casts})
		return cli.ExitOK
	}
	if len(entries) == 0 {
		fmt.Fprintln(env.Stdout, "no audit entries")
	}
	for _, e := range entries {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%q", k, e.Fields[k])
		}
		fmt.Fprintf(env.Stdout, "%s %-15s%s\n", e.Time.Local().Format("2006-01-02 15:04:05"), cli.Sanitize(e.Event), cli.Sanitize(b.String()))
	}
	if len(casts) > 0 {
		fmt.Fprintf(env.Stdout, "\nrecordings (play with `asciinema play FILE`):\n")
		for _, c := range casts {
			fmt.Fprintf(env.Stdout, "  %s\n", c)
		}
	}
	return cli.ExitOK
}

// GrantAudit returns the last n audit entries of the grant called name
// (every entry when name is empty), oldest first.
func GrantAudit(dir, name string, n int) ([]AuditEntry, error) {
	all, err := ReadAudit(dir, 0)
	if err != nil {
		return nil, err
	}
	var out []AuditEntry
	for _, e := range all {
		if name == "" || e.Fields["grant"] == name {
			out = append(out, e)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// Recordings lists the recording files of a grant, oldest first.
func Recordings(dir, name string) ([]string, error) {
	if grant.ValidName(name) != nil {
		return nil, nil
	}
	ents, err := os.ReadDir(filepath.Join(dir, RecordingsDir, name))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".cast") {
			out = append(out, filepath.Join(dir, RecordingsDir, name, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}
