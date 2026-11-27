package hostd

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/signal"
)

// SavedPeers, if set, reports how many hosts this machine has saved as a
// client, for `remohop status`. internal/dial, which owns that store, sets it.
var SavedPeers func() (int, error)

func printJSON(env *cli.Env, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(env.Stdout, string(b))
}

// runStatus is `remohop status` and, with strict set, `remohop host status`, which
// differs only in failing when no host is running.
func runStatus(env *cli.Env, args []string, strict bool) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, statusUsage) }
	asJSON := fs.Bool("json", false, "")
	show := fs.Bool("show-password", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("status: %v", err)
		return cli.ExitError
	}
	// The control socket and the status file are both readable only by
	// the user the host runs as, so getting an answer means the caller is
	// that user.
	st, err := LiveStatus(dir)
	if err != nil {
		env.Errorf("status: %v", err)
		return cli.ExitError
	}
	if st != nil && !*show {
		st.Password = ""
	}
	peers := -1
	if SavedPeers != nil {
		if n, err := SavedPeers(); err == nil {
			peers = n
		}
	}
	code := cli.ExitOK
	if st == nil && strict {
		code = cli.ExitError
	}
	if *asJSON {
		out := struct {
			Running bool `json:"running"`
			*Status
			SavedPeers *int `json:"saved_peers,omitempty"`
		}{Running: st != nil, Status: st}
		if peers >= 0 {
			out.SavedPeers = &peers
		}
		printJSON(env, out)
		return code
	}
	w := env.Stdout
	if st == nil {
		if strict {
			env.Errorf("host: not running (start it with `remohop host`)")
		} else {
			fmt.Fprintln(w, "host: not running (start it with `remohop host`)")
		}
	} else {
		fmt.Fprintf(w, "host: running, pid %d, since %s\n", st.PID, st.Started.Local().Format("2006-01-02 15:04:05"))
		if st.Name != "" {
			fmt.Fprintf(w, "  Name:        %s\n", cli.Sanitize(st.Name))
		}
		switch {
		case st.ID != "" && st.Online && st.Transport == "websocket":
			fmt.Fprintf(w, "  ID:          %s  (registered with %s over its websocket bridge; clients come through the relay)\n", signal.FormatID(st.ID), st.Signal)
		case st.ID != "" && st.Online:
			fmt.Fprintf(w, "  ID:          %s  (registered with %s)\n", signal.FormatID(st.ID), st.Signal)
		case st.ID != "":
			fmt.Fprintf(w, "  ID:          %s  (rendezvous server %s currently unreachable)\n", signal.FormatID(st.ID), st.Signal)
		case st.Signal != "":
			fmt.Fprintf(w, "  ID:          none yet (rendezvous server %s unreachable)\n", st.Signal)
		default:
			fmt.Fprintln(w, "  ID:          none (running without a rendezvous server)")
		}
		pw := "password: " + st.PasswordMode
		if st.Password != "" {
			pw = passwordLine(st.Password, st.PasswordMode)
		} else if st.PasswordMode != "fixed" {
			pw += "  (--show-password to print it)"
		}
		fmt.Fprintf(w, "  Password:    %s\n", pw)
		fmt.Fprintf(w, "  Listening:   %s\n", st.Listen)
		for _, d := range st.Direct {
			fmt.Fprintf(w, "  Direct:      %s\n", d)
		}
		fmt.Fprintf(w, "  Identity:    %s\n", st.Identity)
		if st.Signal != "" {
			fmt.Fprintf(w, "  NAT:         %s\n", st.NAT)
		}
		if line := portMapLine(st.PortMap); line != "" {
			fmt.Fprintln(w, line)
		}
		off := "none"
		if len(st.FeaturesOff) > 0 {
			off = strings.Join(st.FeaturesOff, ", ")
		}
		fmt.Fprintf(w, "  Not allowed: %s\n", off)
		fmt.Fprintf(w, "  Ask:         %s", st.Ask)
		if st.Pending > 0 {
			fmt.Fprintf(w, "  (%d waiting: `remohop host approve`)", st.Pending)
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  Sessions:    %d\n", len(st.Clients))
		if len(st.Clients) > 0 {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "    PEER\tDEVICE\tFROM\tPATH\tAUTH\tSINCE")
			for _, c := range st.Clients {
				device := c.Device
				if device == "" {
					device = "-"
				}
				authCol := authColumn(string(c.Auth), c.Authorizer, c.Grant)
				fmt.Fprintf(tw, "    %s\t%s\t%s\t%s\t%s\t%s\n", cli.Sanitize(c.Peer), cli.Sanitize(device), cli.Sanitize(c.From), cli.Sanitize(c.Path), authCol, c.Since.Local().Format("15:04:05"))
			}
			tw.Flush()
		}
	}
	if peers >= 0 {
		fmt.Fprintf(w, "client: %d saved host(s) (`remohop peers`)\n", peers)
	}
	return code
}

func runApprove(env *cli.Env, allow bool, args []string) int {
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) == 0 && allow || asJSON {
		rep, err := controlCall(dir, controlRequest{Op: "approvals"})
		if err != nil {
			env.Errorf("host: %v", err)
			return cli.ExitError
		}
		if asJSON {
			if rep.Approvals == nil {
				rep.Approvals = []Approval{}
			}
			printJSON(env, rep.Approvals)
			return cli.ExitOK
		}
		if len(rep.Approvals) == 0 {
			fmt.Fprintln(env.Stdout, "no connection is waiting for approval")
			return cli.ExitOK
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "N\tFROM\tIDENTITY\tNAME\tDEVICE\tLOGIN\tWAITING")
		for _, a := range rep.Approvals {
			if a.Action != "" {
				continue
			}
			name, device := a.Name, a.Device
			if name == "" {
				name = "-"
			}
			if device == "" {
				device = "-"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\tsince %s\n", a.N, a.From, cli.Sanitize(a.Identity), cli.Sanitize(name), cli.Sanitize(device), cli.Sanitize(string(a.Auth)), a.Since.Local().Format("15:04:05"))
		}
		tw.Flush()
		for _, a := range rep.Approvals {
			if a.Action != "" {
				fmt.Fprintf(env.Stdout, "%d  grant %s wants to %s  (since %s)\n", a.N, cli.Sanitize(a.Grant), cli.OneLine(a.Action), a.Since.Local().Format("15:04:05"))
			}
		}
		fmt.Fprintln(env.Stdout, "answer with `remohop host approve N` or `remohop host deny N`")
		return cli.ExitOK
	}
	n, err := strconv.Atoi(strings.Join(args, ""))
	if len(args) != 1 || err != nil || n <= 0 {
		env.Errorf("usage: remohop host approve [--json] | remohop host approve N | remohop host deny N")
		return cli.ExitUsage
	}
	rep, err := controlCall(dir, controlRequest{Op: "decide", N: n, Allow: allow})
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	verb := "denied"
	if allow {
		verb = "approved"
	}
	if len(rep.Approvals) == 1 && rep.Approvals[0].N == n {
		fmt.Fprintf(env.Stdout, "%s %s\n", verb, describeApproval(rep.Approvals[0]))
	} else {
		fmt.Fprintf(env.Stdout, "%s request %d\n", verb, n)
	}
	return cli.ExitOK
}

func runOTP(env *cli.Env, args []string) int {
	op := "otp"
	switch {
	case len(args) == 1 && args[0] == "new":
		op = "otp-new"
	case len(args) != 0:
		env.Errorf("usage: remohop host otp [new]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	rep, err := controlCall(dir, controlRequest{Op: op})
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	fmt.Fprintln(env.Stdout, rep.Password)
	return cli.ExitOK
}

// readSecret reads one line without echo from a terminal, or plainly from
// anything else (so the command can be scripted).
func readSecret(env *cli.Env, in *bufio.Reader, prompt string) (string, error) {
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(env.Stderr, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(env.Stderr)
		return string(b), err
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func runPasswd(env *cli.Env, args []string) int {
	if len(args) == 1 && args[0] == "--clear" {
		if err := config.Set(SettingPassword, ""); err != nil {
			env.Errorf("host: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, "fixed password removed from the configuration")
		// A host started with --password keeps that password until it is
		// restarted without the flag; say so rather than claim otherwise.
		if dir, derr := config.Dir(); derr == nil {
			if st, serr := LiveStatus(dir); serr == nil && st != nil && strings.HasPrefix(st.PasswordMode, "fixed") {
				fmt.Fprintln(env.Stdout, "the running host still uses a fixed password (it was started with --password); restart it without that flag to use a temporary one")
				return cli.ExitOK
			}
		}
		fmt.Fprintln(env.Stdout, "the host uses its temporary password")
		return cli.ExitOK
	}
	if len(args) != 0 {
		env.Errorf("usage: remohop host passwd [--clear]")
		return cli.ExitUsage
	}
	in := bufio.NewReader(env.Stdin)
	first, err := readSecret(env, in, "New fixed password: ")
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	if err := auth.CheckPassword(auth.NormalizePassword(first)); err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	second, err := readSecret(env, in, "Again: ")
	if err != nil || second != first {
		env.Errorf("host: the two entries do not match; nothing changed")
		return cli.ExitError
	}
	if err := config.Set(SettingPassword, first); err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	fmt.Fprintln(env.Stdout, "fixed password set (a running host accepts it from now on)")
	return cli.ExitOK
}

func runAuthorize(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host authorize", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	name := fs.String("name", "", "")
	// Accept the flag before or after the key line.
	var line []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		line = append(line, fs.Arg(0))
		args = fs.Args()[1:]
	}
	key, label, err := auth.ParseKeyLine(strings.Join(line, " "))
	if err != nil {
		env.Errorf("host: %v\nusage: remohop host authorize \"KEY-LINE\" [--name NAME]", err)
		return cli.ExitUsage
	}
	if *name == "" {
		*name = label
	}
	if auth.CleanTokenName(*name) == "" {
		*name = "key " + auth.Fingerprint(key)
	}
	store, err := tokenStore()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	tok, err := store.Authorize(*name, key)
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	dir, _ := config.Dir()
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	target := "<ID or ADDR:PORT of this host>"
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
	host, _ := os.Hostname()
	fmt.Fprintf(env.Stdout, "authorised %q (identity %s) to log in with its key\n", cli.Sanitize(tok.Name), auth.Fingerprint(key))
	fmt.Fprintf(env.Stdout, "On that client, make this host known (once), then connect without a password:\n  remohop peers trust %s \"%s\"\n",
		target, auth.FormatKeyLine(id.Public(), host))
	return cli.ExitOK
}

func runLog(_ context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, logUsage) }
	asJSON := fs.Bool("json", false, "")
	n := 50
	// N may come before or after --json.
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		v, err := strconv.Atoi(fs.Arg(0))
		if err != nil || v <= 0 {
			env.Errorf("usage: remohop log [N] [--json]")
			return cli.ExitUsage
		}
		n, args = v, fs.Args()[1:]
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("log: %v", err)
		return cli.ExitError
	}
	entries, err := ReadAudit(dir, n)
	if err != nil {
		env.Errorf("log: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		if entries == nil {
			entries = []AuditEntry{}
		}
		printJSON(env, entries)
		return cli.ExitOK
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
		fmt.Fprintf(env.Stdout, "%s %-12s%s\n", e.Time.Local().Format("2006-01-02 15:04:05"), cli.Sanitize(e.Event), cli.Sanitize(b.String()))
	}
	return cli.ExitOK
}

// portMapLine is the `Router port:` status line; empty without a mapping.
func portMapLine(pm *PortMapStatus) string {
	switch {
	case pm == nil:
		return ""
	case pm.Offered:
		return fmt.Sprintf("  Router port: %s %s (offered to clients)", cli.Sanitize(pm.Protocol), cli.Sanitize(pm.External))
	}
	return fmt.Sprintf("  Router port: %s %s (not offered: the router is behind another NAT)", cli.Sanitize(pm.Protocol), cli.Sanitize(pm.External))
}

// authColumn renders how a session logged in; sessions an Authorizer admitted
// also name it and the grant.
func authColumn(auth, authorizer, grant string) string {
	col := cli.Sanitize(auth)
	if authorizer != "" {
		col += " via " + cli.Sanitize(authorizer)
		if grant != "" {
			col += " (" + cli.Sanitize(grant) + ")"
		}
	}
	return col
}

// runSessions lists the live sessions of the running host.
func runSessions(env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("host sessions", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		env.Errorf("usage: remohop host sessions [--json]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("host: %v", err)
		return cli.ExitError
	}
	ss, err := ControlSessions(dir)
	if err != nil {
		if errors.Is(err, errNotRunning) {
			env.Errorf("host: not running (start it with `remohop host`)")
		} else {
			env.Errorf("host sessions: %v", err)
		}
		return cli.ExitError
	}
	if *asJSON {
		if ss == nil {
			ss = []ControlSession{}
		}
		printJSON(env, ss)
		return cli.ExitOK
	}
	if len(ss) == 0 {
		fmt.Fprintln(env.Stdout, "no sessions")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPEER\tDEVICE\tFROM\tPATH\tAUTH\tSINCE")
	for _, c := range ss {
		device := c.Device
		if device == "" {
			device = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, cli.Sanitize(c.Peer), cli.Sanitize(device), cli.Sanitize(c.From), cli.Sanitize(c.Path),
			cmp.Or(strings.TrimSpace(authColumn("", c.Authorizer, c.Grant)), "-"), c.Since.Local().Format("15:04:05"))
	}
	tw.Flush()
	return cli.ExitOK
}
