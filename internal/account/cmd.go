package account

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/signal"
)

const accountNote = `An account only groups your devices so that each can list the others and
see which are online. It never grants access to a device: connecting still
needs a password, pairing or key the host accepts. The one exception is a
host whose owner ran ` + "`remohop config account-trust on`" + ` there: it accepts the
account's other devices like keys authorised with ` + "`remohop authorize`" + `. It
decides who they are from the account's device list, which is signed by the
account's own devices; the rendezvous server stores it but cannot forge it,
so a rendezvous server cannot add itself to your account or log in to your
hosts. That is why a new device must be approved by an existing one, and why
both sides compare a fingerprint.`

const loginUsage = `Usage: remohop login EMAIL [--code CODE] [--account-fingerprint FP] [--no-wait | --wait DURATION] [--json]
       remohop login EMAIL --send-only

Log this device in to an account on the rendezvous server (accounts are
optional; the server's operator enables them with ` + "`remohop signal --accounts`" + `).

The server mails a login code (8 digits, valid 10 minutes, single use).
Type it at the prompt, or for scripts request it with --send-only and give
it with --code.

  - The first device to log in creates the account.
  - Any further device has to be approved by a device already in the
    account. Two comparisons make that safe against the rendezvous server:
    1. this command shows the ACCOUNT fingerprint and asks you to confirm
       it equals the one ` + "`remohop account`" + ` shows on your other device
       (scripts: --account-fingerprint FP);
    2. it prints THIS device's identity fingerprint; on your other device,
       run ` + "`remohop account approve FINGERPRINT`" + ` with exactly that value.
    Then this command finishes (it waits up to --wait for the approval).

` + accountNote + `

Flags:
  --code CODE                the login code from the mail (no prompt)
  --send-only                only ask the server to mail a code, then exit
  --account-fingerprint FP   the account fingerprint shown by an existing
                             device; required without a terminal when
                             joining an existing account
  --wait DURATION            how long to wait for approval (default 10m)
  --no-wait                  do not wait; ` + "`remohop account`" + ` shows later whether
                             the device was approved
  --json                     print the result as JSON

The global --signal flag (or the configured server) names the rendezvous
server; the account stays on that server and its key is pinned.

Examples:
  remohop login me@example.com
  remohop login me@example.com --send-only
  remohop login me@example.com --code 12345678 --account-fingerprint 3f9a:01bc:77d2:e410:9c55 --json
`

const logoutUsage = `Usage: remohop logout [--json]

Leave the account: this device is removed from the account's device list
(an entry it signs itself), its session on the rendezvous server ends, and
the account is forgotten here. If this machine is a host with account-trust
on, it stops accepting the account's devices at once.

If the server cannot be reached, the account is still forgotten here; the
device then stays in the account's list until another device runs
` + "`remohop account remove`" + `.

Examples:
  remohop logout
`

const accountUsage = `Usage: remohop account [--json] [--offline]
       remohop account approve [FINGERPRINT] [--json]
       remohop account reject FINGERPRINT
       remohop account remove FINGERPRINT|ID|NAME
       remohop account delete --yes
       remohop account sync

Show and manage this device's account (see ` + "`remohop login`" + `).

  (none)    the account's email, its fingerprint, this device, every device
            of the account with its ID, name, platform, whether it is online
            and when it was last seen, and devices waiting to be approved.
            --offline shows what was fetched last, without asking the server.
  approve   let a waiting device join. FINGERPRINT must be the complete
            identity fingerprint the new device printed (20 hex digits): a
            prefix is not accepted, because a rendezvous server can make
            keys that match a short one. Without FINGERPRINT, each waiting
            device is shown and you are asked (terminal only).
  reject    drop a waiting device's request
  remove    take a device out of the account; it can no longer list the
            account, and hosts with account-trust stop accepting it as soon
            as they fetch the change (about a minute)
  delete    delete the account on the rendezvous server, for every device
  sync      fetch the account now and update the keys account-trust
            authorises on this host (` + "`remohop host`" + ` does this every minute)

` + accountNote + `

Hosts: ` + "`remohop config account-trust on`" + ` (default off) makes this machine, when it
runs ` + "`remohop host`" + `, accept logins from the account's other devices; their keys
appear in ` + "`remohop host paired`" + ` as "account:NAME". ` + "`remohop host revoke`" + ` of one keeps
that device out until account-trust is turned off and on again. The
host's ask-paired setting applies to them as to any authorised key. A host
that cannot reach the rendezvous server keeps the last verified device list
for up to 7 days, then trusts nobody from the account until it can check
again.

Examples:
  remohop account
  remohop account --json
  remohop account approve 3f9a:01bc:77d2:e410:9c55
  remohop account remove old-laptop
  remohop account delete --yes
`

func init() {
	cli.Register(&cli.Command{Name: "login", Summary: "log this device in to an account on the rendezvous server", Usage: loginUsage, Run: runLogin})
	cli.Register(&cli.Command{Name: "logout", Summary: "leave the account", Usage: logoutUsage, Run: runLogout})
	cli.Register(&cli.Command{Name: "account", Summary: "show the account and its devices; approve or remove devices", Usage: accountUsage, Run: runAccount})
}

func newFlags(env *cli.Env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, usage) }
	return fs
}

func printJSON(env *cli.Env, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(env.Stdout, string(b))
}

// parseInterspersed parses flags that may come after positional arguments.
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

// terminal returns stdin when it is a terminal.
func terminal(env *cli.Env) (*os.File, bool) {
	f, ok := env.Stdin.(*os.File)
	return f, ok && term.IsTerminal(int(f.Fd()))
}

func readLine(ctx context.Context, in *bufio.Reader) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := in.ReadString('\n')
		ch <- result{strings.TrimSpace(s), err}
	}()
	select {
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		return r.s, nil
	case <-ctx.Done():
		return "", errors.New("interrupted")
	}
}

func yes(answer string) bool {
	a := strings.ToLower(answer)
	return a == "y" || a == "yes"
}

func optsFromEnv(env *cli.Env) Options { return Options{Signal: env.Globals.Signal} }

func exitFor(err error) int {
	if signal.CodeOf(err) != "" {
		return cli.ExitConnect
	}
	return cli.ExitError
}

func runLogin(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "login", loginUsage)
	code := fs.String("code", "", "")
	sendOnly := fs.Bool("send-only", false, "")
	accountFP := fs.String("account-fingerprint", "", "")
	wait := fs.Duration("wait", 10*time.Minute, "")
	noWait := fs.Bool("no-wait", false, "")
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return cli.ExitUsage
	}
	if len(pos) != 1 {
		env.Errorf("usage: remohop login EMAIL (see `remohop help login`)")
		return cli.ExitUsage
	}
	email := pos[0]
	opts := optsFromEnv(env)
	_, interactive := terminal(env)
	in := bufio.NewReader(env.Stdin)

	if *code == "" {
		if err := RequestCode(ctx, opts, email); err != nil {
			env.Errorf("login: %v", err)
			return exitFor(err)
		}
		fmt.Fprintf(env.Stderr, "A login code was sent to %s.\n", cli.Sanitize(email))
		if *sendOnly {
			return cli.ExitOK
		}
		if !interactive {
			env.Errorf("login: no terminal to type the code at: run `remohop login %s --code CODE`", email)
			return cli.ExitUsage
		}
		fmt.Fprint(env.Stderr, "Login code: ")
		c, err := readLine(ctx, in)
		if err != nil {
			env.Errorf("login: %v", err)
			return cli.ExitError
		}
		*code = c
	} else if *sendOnly {
		env.Errorf("login: --send-only and --code exclude each other")
		return cli.ExitUsage
	}
	req := LoginRequest{Email: email, Code: *code, AccountFingerprint: *accountFP}
	if *accountFP == "" && interactive {
		req.ConfirmAccount = func(fp string) (bool, error) {
			fmt.Fprintf(env.Stderr, "\nThis address already has an account. Its fingerprint, as the rendezvous server presents it:\n\n"+
				"    %s\n\nRun `remohop account` on one of your devices already in the account and compare.\n"+
				"Do the two fingerprints match exactly? [y/N] ", fp)
			a, err := readLine(ctx, in)
			return yes(a), err
		}
	}
	res, err := Login(ctx, opts, req)
	if err != nil {
		env.Errorf("login: %v", err)
		return exitFor(err)
	}
	if res.Status == "pending" {
		if !*asJSON {
			fmt.Fprintf(env.Stdout, "Logged in as %s; this device is waiting to be approved.\n", cli.Sanitize(res.Email))
			fmt.Fprintf(env.Stdout, "On a device already in the account, run:\n\n    remohop account approve %s\n\n", res.Device)
			fmt.Fprintln(env.Stdout, "Only approve that exact fingerprint: it is this device's identity.")
		}
		if !*noWait && *wait > 0 {
			if !*asJSON {
				fmt.Fprintf(env.Stderr, "Waiting for the approval (up to %s; Ctrl-C stops waiting, `remohop account` checks later)...\n", *wait)
			}
			wctx, cancel := context.WithTimeout(ctx, *wait)
			st, err := WaitForApproval(wctx, Options{Dir: opts.Dir}, 2*time.Second)
			cancel()
			switch {
			case err == nil && st.Status == StatusMember:
				res.Status = "member"
				if !*asJSON {
					fmt.Fprintln(env.Stdout, "Approved: this device is now in the account.")
				}
			case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
				if !*asJSON {
					fmt.Fprintln(env.Stderr, "Not approved yet; `remohop account` shows when it is.")
				}
			default:
				env.Errorf("login: %v", err)
				return exitFor(err)
			}
		}
	}
	if *asJSON {
		printJSON(env, res)
		return cli.ExitOK
	}
	switch res.Status {
	case "created":
		fmt.Fprintf(env.Stdout, "Logged in as %s. This device created the account.\n", cli.Sanitize(res.Email))
	case "member":
		fmt.Fprintf(env.Stdout, "Logged in as %s.\n", cli.Sanitize(res.Email))
	}
	if res.Status != "pending" {
		fmt.Fprintf(env.Stdout, "Account fingerprint: %s  (other devices compare this when they log in)\n", res.Account)
		fmt.Fprintf(env.Stdout, "This device:         %s\n", res.Device)
		fmt.Fprintln(env.Stdout, "Your devices: remohop devices. To let them connect here without a password: remohop config account-trust on")
	}
	return cli.ExitOK
}

func runLogout(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "logout", logoutUsage)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return cli.ExitUsage
	}
	st, err := LoadOpts(Options{})
	if err != nil {
		env.Errorf("logout: %v", err)
		return cli.ExitError
	}
	err = Logout(ctx, Options{})
	if *asJSON {
		out := map[string]any{"logged_out": true, "email": st.Email}
		if err != nil {
			out["warning"] = err.Error()
		}
		printJSON(env, out)
	} else {
		fmt.Fprintf(env.Stdout, "Logged out of %s.\n", cli.Sanitize(st.Email))
	}
	if err != nil {
		env.Errorf("logout: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

// Summary is `remohop account --json`.
type Summary struct {
	Email   string `json:"email"`
	Server  string `json:"server"`
	Account string `json:"account"`
	// Status is this device's: member, pending or logged_out.
	Status       string        `json:"status"`
	ThisDevice   string        `json:"this_device"`
	AccountTrust bool          `json:"account_trust"`
	Devices      []Device      `json:"devices"`
	Pending      []JoinRequest `json:"pending,omitempty"`
	// Error is set when the server could not be asked; Devices is then
	// what was fetched last.
	Error string `json:"error,omitempty"`
}

func runAccount(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "approve":
			return runApprove(ctx, env, args[1:])
		case "reject", "remove":
			if len(args) != 2 {
				env.Errorf("usage: remohop account %s FINGERPRINT", args[0])
				return cli.ExitUsage
			}
			var err error
			if args[0] == "reject" {
				err = Reject(ctx, Options{}, args[1])
			} else {
				var d *Device
				if d, err = Remove(ctx, Options{}, args[1]); err == nil {
					fmt.Fprintf(env.Stdout, "Removed %s (identity %s) from the account.\n", cli.Sanitize(deviceLabel(*d)), d.Identity)
				}
			}
			if err != nil {
				env.Errorf("account %s: %v", args[0], err)
				return exitFor(err)
			}
			return cli.ExitOK
		case "delete":
			fs := newFlags(env, "account delete", accountUsage)
			sure := fs.Bool("yes", false, "")
			if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
				return cli.ExitUsage
			}
			if !*sure {
				env.Errorf("account delete: this deletes the account for every device; confirm with --yes")
				return cli.ExitUsage
			}
			if err := Delete(ctx, Options{}); err != nil {
				env.Errorf("account delete: %v", err)
				return exitFor(err)
			}
			fmt.Fprintln(env.Stdout, "Account deleted.")
			return cli.ExitOK
		case "sync":
			if len(args) != 1 {
				return cli.ExitUsage
			}
			if _, err := Refresh(ctx, Options{}); err != nil {
				env.Errorf("account sync: %v", err)
			}
			dir, err := Options{}.dir()
			if err != nil {
				env.Errorf("account sync: %v", err)
				return cli.ExitError
			}
			added, removed, err := SyncTrust(dir)
			if err != nil {
				env.Errorf("account sync: %v", err)
				return cli.ExitError
			}
			fmt.Fprintf(env.Stdout, "account-trust: %d key(s) added, %d removed\n", added, removed)
			return cli.ExitOK
		default:
			env.Errorf("account: unknown subcommand %q (approve, reject, remove, delete, sync)", args[0])
			return cli.ExitUsage
		}
	}
	fs := newFlags(env, "account", accountUsage)
	asJSON := fs.Bool("json", false, "")
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return cli.ExitUsage
	}
	dir, err := Options{}.dir()
	if err != nil {
		env.Errorf("account: %v", err)
		return cli.ExitError
	}
	st, err := Load(dir)
	if err != nil {
		env.Errorf("account: %v", err)
		return cli.ExitError
	}
	sum := Summary{Email: st.Email, Server: signal.Display(st.Server, signal.Options{WS: st.ServerWS}), Account: st.Fingerprint(), ThisDevice: auth.Fingerprint(mustIdentity(dir)),
		AccountTrust: TrustEnabled(dir)}
	if !*offline {
		if st.Status == StatusPending {
			st, err = Refresh(ctx, Options{})
		}
		if err == nil && st.Status == StatusMember {
			sum.Devices, st, err = Devices(ctx, Options{})
			if err == nil {
				sum.Pending, err = Pending(ctx, Options{})
			}
		}
		if err != nil {
			sum.Error = err.Error()
		}
	}
	if sum.Devices == nil {
		sum.Devices, _, _ = CachedDevices(dir)
	}
	if cur, err := Load(dir); err == nil {
		st = cur
	}
	sum.Status = st.Status
	if sum.Devices == nil {
		sum.Devices = []Device{}
	}
	if *asJSON {
		printJSON(env, sum)
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "Account:      %s on %s\n", cli.Sanitize(sum.Email), cli.Sanitize(sum.Server))
	if sum.Account != "" {
		fmt.Fprintf(env.Stdout, "Fingerprint:  %s\n", sum.Account)
	}
	switch sum.Status {
	case StatusMember:
		fmt.Fprintf(env.Stdout, "This device:  %s (in the account)\n", sum.ThisDevice)
	case StatusPending:
		fmt.Fprintf(env.Stdout, "This device:  %s, WAITING for approval: on another device run `remohop account approve %s`\n", sum.ThisDevice, sum.ThisDevice)
	default:
		fmt.Fprintf(env.Stdout, "This device:  %s, LOGGED OUT by the server (removed, or the account was deleted): `remohop login` again\n", sum.ThisDevice)
	}
	trust := "off (an account grants no access here; `remohop config account-trust on` to accept the account's devices)"
	if sum.AccountTrust {
		trust = "on (when hosting, the account's other devices may log in here)"
	}
	fmt.Fprintf(env.Stdout, "Account-trust: %s\n", trust)
	if len(sum.Devices) > 0 {
		fmt.Fprintln(env.Stdout)
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPLATFORM\tPRESENCE\tLAST SEEN\tIDENTITY")
		for _, d := range sum.Devices {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", dash(signal.FormatID(d.ID)), cli.Sanitize(dash(deviceName(d))), cli.Sanitize(dash(d.Platform)),
				presence(d, *offline || sum.Error != ""), lastSeen(d.LastSeen), d.Identity)
		}
		tw.Flush()
	}
	if len(sum.Pending) > 0 {
		fmt.Fprintln(env.Stdout, "\nWaiting to join (approve only a fingerprint the new device shows you):")
		for _, p := range sum.Pending {
			fmt.Fprintf(env.Stdout, "  %s  %s  %s  from %s, %s ago\n", p.Identity, cli.Sanitize(dash(p.Name)), cli.Sanitize(dash(p.Platform)),
				cli.Sanitize(dash(p.From)), time.Since(p.At).Round(time.Second))
		}
		fmt.Fprintln(env.Stdout, "  remohop account approve FINGERPRINT   or   remohop account reject FINGERPRINT")
	}
	if sum.Error != "" {
		env.Errorf("account: could not ask the rendezvous server (%s); showing what was fetched last", sum.Error)
		return cli.ExitError
	}
	return cli.ExitOK
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func deviceName(d Device) string {
	if d.This {
		return d.Name + " (this device)"
	}
	return d.Name
}

func deviceLabel(d Device) string {
	switch {
	case d.Name != "":
		return d.Name
	case d.ID != "":
		return signal.FormatID(d.ID)
	}
	return d.Identity
}

func presence(d Device, stale bool) string {
	switch {
	case stale:
		return "not checked"
	case d.Online:
		return "online"
	case d.ID == "":
		return "not a host"
	}
	return "offline"
}

func lastSeen(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func runApprove(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "account approve", accountUsage)
	asJSON := fs.Bool("json", false, "")
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) > 1 {
		return cli.ExitUsage
	}
	var fps []string
	if len(pos) == 1 {
		fps = pos
	} else {
		_, interactive := terminal(env)
		if !interactive {
			env.Errorf("account approve: give the new device's FINGERPRINT (no terminal to ask at)")
			return cli.ExitUsage
		}
		pending, err := Pending(ctx, Options{})
		if err != nil {
			env.Errorf("account approve: %v", err)
			return exitFor(err)
		}
		if len(pending) == 0 {
			fmt.Fprintln(env.Stdout, "No device is waiting to join.")
			return cli.ExitOK
		}
		in := bufio.NewReader(env.Stdin)
		for _, p := range pending {
			fmt.Fprintf(env.Stderr, "\nDevice %s (%s), logged in from %s %s ago\n  identity: %s\n"+
				"Approve it? Only if the new device shows exactly this identity. [y/N] ",
				cli.Sanitize(dash(p.Name)), cli.Sanitize(dash(p.Platform)), cli.Sanitize(dash(p.From)), time.Since(p.At).Round(time.Second), p.Identity)
			a, err := readLine(ctx, in)
			if err != nil {
				env.Errorf("account approve: %v", err)
				return cli.ExitError
			}
			if yes(a) {
				fps = append(fps, p.Identity)
			}
		}
	}
	var done []*JoinRequest
	for _, fp := range fps {
		jr, err := Approve(ctx, Options{}, fp)
		if err != nil {
			env.Errorf("account approve: %v", err)
			return exitFor(err)
		}
		done = append(done, jr)
		if !*asJSON {
			fmt.Fprintf(env.Stdout, "Approved %s (identity %s).\n", cli.Sanitize(dash(jr.Name)), jr.Identity)
		}
	}
	if *asJSON {
		if done == nil {
			done = []*JoinRequest{}
		}
		printJSON(env, done)
	}
	return cli.ExitOK
}
