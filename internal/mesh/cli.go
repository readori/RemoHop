package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
)

const usage = `Usage: remohop net up [--exit] [--route CIDR]... [--use-exit NAME|--no-exit-node]
                  [--accept-routes|--no-accept-routes] [--dns auto|hosts|off] [--reset]
       remohop net down
       remohop net status [--json]
       remohop net peers [--json]
       remohop net policy [--json]
       remohop net policy set FILE
       remohop net policy reset

A private network of the devices of your account (` + "`remohop login`" + `): each gets a
stable address derived from its identity key (10.78.x.y and an IPv6
unique-local address) and a name, like laptop.remohop. Traffic between two
devices goes over a RemoHop session between them, opened when it is first
needed: hole punched where possible, else through the rendezvous server's
relay or websocket bridge; end-to-end encrypted either way.

The mesh runs inside the host daemon (` + "`remohop host`" + ` or the host service), as root
or with CAP_NET_ADMIN on Linux, as root on macOS, as Administrator (the
service) on Windows. ` + "`remohop net up`" + ` switches it on in the
configuration; the running host brings it up within seconds and on every
start after that. Every device that should be reachable runs the host.

  up       join the mesh (and keep joining it at every host start)
  down     leave it: interface, routes, firewall rules and DNS go away
  status   this device's mesh address, name, interface, DNS and policy
  peers    the other devices: address, name, online, link and its path
  policy   show the access policy; ` + "`set FILE`" + ` signs and installs a new one for
           the whole account, ` + "`reset`" + ` goes back to "everything allowed"

Flags for up (each changes its setting; the others stay as they are):
  --exit             offer this device as an exit node (Linux; forwards and
                     masquerades for the mesh, IPv4)
  --route CIDR       route this network for the mesh (subnet router; Linux).
                     Repeatable; replaces the routes set before
  --use-exit NAME    send all of this device's traffic through mesh device
                     NAME, which must offer --exit; --no-exit-node stops it
  --accept-routes    use the networks other devices route for the mesh
  --dns MODE         auto (default): .remohop goes to the mesh's resolver
                     through systemd-resolved, /etc/resolver (macOS) or an
                     NRPT rule (Windows); hosts: a marked block in the hosts
                     file; off: nothing (resolver answers on
                     this device's mesh address, port 53)
  --reset            clear all of the above before applying the flags

Access: every device of the account may reach every port of every other
until a policy says otherwise. A policy is a JSON file like

  {"version": 2, "rules": [
    {"from": ["laptop"], "to": ["nas"], "ports": ["22/tcp", "445/tcp", "icmp"]},
    {"from": ["*"], "to": ["printer"], "ports": ["631/tcp"]}]}

It is enforced by each receiving device (replies to its own connections
always pass), signed by the device that set it, and handed from device to
device. "prefix4" in it moves the whole mesh to another private network.

Not part of the mesh: devices outside your account, single sign-on, an
admin console, sharing a device with other people. See docs/net.md.

Examples:
  sudo remohop net up
  remohop net peers
  ping nas.remohop
  sudo remohop net up --route 192.168.1.0/24 --exit      # on the office machine
  sudo remohop net up --accept-routes --use-exit office  # on the laptop
  remohop net policy set policy.json
  sudo remohop net down`

func init() {
	cli.Register(&cli.Command{Name: "net", Summary: "mesh network of this account's devices (laptop.remohop)", Usage: usage, Run: runNet})
}

func runNet(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, usage)
		return cli.ExitUsage
	}
	switch args[0] {
	case "up":
		return runUp(ctx, env, args[1:])
	case "down":
		return runDown(ctx, env, args[1:])
	case "status":
		return runStatus(env, args[1:], false)
	case "peers":
		return runStatus(env, args[1:], true)
	case "policy":
		return runPolicy(env, args[1:])
	}
	env.Errorf("net: unknown subcommand %q (up, down, status, peers, policy)", args[0])
	return cli.ExitUsage
}

type routeList []string

func (l *routeList) String() string     { return strings.Join(*l, ",") }
func (l *routeList) Set(v string) error { *l = append(*l, v); return nil }

func runUp(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("net up", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, usage) }
	exit := fs.Bool("exit", false, "")
	var routes routeList
	fs.Var(&routes, "route", "")
	useExit := fs.String("use-exit", "", "")
	noExitNode := fs.Bool("no-exit-node", false, "")
	accept := fs.Bool("accept-routes", false, "")
	noAccept := fs.Bool("no-accept-routes", false, "")
	dns := fs.String("dns", "", "")
	reset := fs.Bool("reset", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("net up: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	changes := map[string]string{}
	if *reset {
		for _, k := range []string{SettingExit, SettingRoutes, SettingUseExit, SettingAcceptRoutes, SettingDNS} {
			changes[k] = ""
		}
	}
	if set["exit"] {
		changes[SettingExit] = onOff(*exit)
	}
	if set["route"] {
		v := strings.Join(routes, ",")
		if _, err := parseRoutes(v); err != nil {
			env.Errorf("net up: %v", err)
			return cli.ExitUsage
		}
		changes[SettingRoutes] = v
	}
	if set["use-exit"] && set["no-exit-node"] {
		env.Errorf("net up: --use-exit and --no-exit-node contradict each other")
		return cli.ExitUsage
	}
	if set["use-exit"] {
		changes[SettingUseExit] = *useExit
	}
	if *noExitNode {
		changes[SettingUseExit] = ""
	}
	if set["accept-routes"] && set["no-accept-routes"] {
		env.Errorf("net up: --accept-routes and --no-accept-routes contradict each other")
		return cli.ExitUsage
	}
	if set["accept-routes"] {
		changes[SettingAcceptRoutes] = onOff(*accept)
	}
	if *noAccept {
		changes[SettingAcceptRoutes] = "off"
	}
	if set["dns"] {
		changes[SettingDNS] = *dns
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("net up: %v", err)
		return cli.ExitError
	}
	if st, err := account.Load(dir); err != nil || st.Status != account.StatusMember {
		env.Errorf("net up: %v", errNoAccount)
		return cli.ExitError
	}
	for k, v := range changes {
		if err := config.Set(k, v); err != nil {
			env.Errorf("net up: %v", err)
			return cli.ExitUsage
		}
	}
	if err := config.Set(SettingNet, "on"); err != nil {
		env.Errorf("net up: %v", err)
		return cli.ExitError
	}
	if hs, _ := hostd.ReadStatus(dir); hs == nil {
		fmt.Fprintln(env.Stdout, "net: switched on; it comes up with the host daemon, which is not running here.")
		fmt.Fprintln(env.Stdout, "     Start it with `sudo remohop host`, or once for good with `sudo remohop service install --system`.")
		return cli.ExitOK
	}
	st := waitStatus(ctx, dir, 15*time.Second, func(st *Status) bool {
		return st != nil && (st.Up || st.Error != "") && time.Since(st.Updated) < 10*time.Second && settingsShown(st, changes)
	})
	if st == nil || !st.Up {
		why := "the host did not bring it up in time; see `remohop net status`"
		if st != nil && st.Error != "" {
			why = st.Error
		}
		env.Errorf("net up: %s", why)
		return cli.ExitError
	}
	printStatus(env.Stdout, st)
	return cli.ExitOK
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// settingsShown reports whether a status reflects the settings just made
// (the host restarts the mesh when they change).
func settingsShown(st *Status, changes map[string]string) bool {
	if v, ok := changes[SettingExit]; ok && (v == "on") != st.Exit {
		return false
	}
	if v, ok := changes[SettingUseExit]; ok && v != st.UseExit {
		return false
	}
	if v, ok := changes[SettingAcceptRoutes]; ok && (v == "on") != st.AcceptRoutes {
		return false
	}
	if v, ok := changes[SettingRoutes]; ok {
		want, _ := parseRoutes(v)
		if len(want) != len(st.Routes) {
			return false
		}
	}
	if v, ok := changes[SettingDNS]; ok && v != "" && v != st.DNS {
		return false
	}
	return true
}

func waitStatus(ctx context.Context, dir string, d time.Duration, done func(*Status) bool) *Status {
	deadline := time.Now().Add(d)
	var st *Status
	for time.Now().Before(deadline) {
		st, _ = ReadStatus(dir)
		if done(st) {
			return st
		}
		select {
		case <-ctx.Done():
			return st
		case <-time.After(300 * time.Millisecond):
		}
	}
	return st
}

func runDown(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) != 0 {
		env.Errorf("usage: remohop net down")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("net down: %v", err)
		return cli.ExitError
	}
	if err := config.Set(SettingNet, "off"); err != nil {
		env.Errorf("net down: %v", err)
		return cli.ExitError
	}
	st := waitStatus(ctx, dir, 15*time.Second, func(st *Status) bool { return st == nil })
	if st == nil {
		// A hosts-file block left by a host that was killed (net-dns hosts).
		writeHosts(nil)
	}
	if st != nil {
		env.Errorf("net down: switched off, but the host has not taken the mesh down yet (pid %d)", st.PID)
		return cli.ExitError
	}
	fmt.Fprintln(env.Stdout, "net: down (and stays down at the next host start; `remohop net up` joins again)")
	return cli.ExitOK
}

func runStatus(env *cli.Env, args []string, peers bool) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		env.Errorf("usage: remohop net status|peers [--json]")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("net: %v", err)
		return cli.ExitError
	}
	st, err := ReadStatus(dir)
	if err != nil {
		env.Errorf("net: %v", err)
		return cli.ExitError
	}
	if asJSON {
		var v any = st
		if st == nil {
			v = map[string]any{"up": false}
		} else if peers {
			v = st.Peers
			if st.Peers == nil {
				v = []PeerStatus{}
			}
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	if st == nil {
		values, _ := config.AllIn(dir)
		if config.Bool(values[SettingNet], false) {
			if hs, _ := hostd.ReadStatus(dir); hs == nil {
				fmt.Fprintln(env.Stdout, "net: on, but the host daemon is not running here, so the mesh is down (start `remohop host`)")
				return cli.ExitError
			}
			fmt.Fprintln(env.Stdout, "net: on; the host is starting it")
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, "net: off (`remohop net up` joins the mesh)")
		return cli.ExitError
	}
	if !st.Up {
		fmt.Fprintf(env.Stdout, "net: on, but it cannot come up: %s\n", cli.Sanitize(st.Error))
		return cli.ExitError
	}
	if peers {
		printPeers(env.Stdout, st)
	} else {
		printStatus(env.Stdout, st)
	}
	return cli.ExitOK
}

func printStatus(w io.Writer, st *Status) {
	fmt.Fprintf(w, "net: up as %s (%s", st.FQDN, st.Addr4)
	if st.Addr6 != "" {
		fmt.Fprintf(w, ", %s", st.Addr6)
	}
	fmt.Fprintf(w, ") on %s\n", st.Device)
	fmt.Fprintf(w, "  mesh:     %s, %s\n", st.Prefix4, st.Prefix6)
	online, linked := 0, 0
	for _, p := range st.Peers {
		if p.Online {
			online++
		}
		if p.Link == "up" {
			linked++
		}
	}
	fmt.Fprintf(w, "  peers:    %d (%d online, %d linked now; `remohop net peers`)\n", len(st.Peers), online, linked)
	switch {
	case st.PolicyRules < 0:
		fmt.Fprintln(w, "  policy:   none: every device may reach every port of every other")
	default:
		fmt.Fprintf(w, "  policy:   version %d, %d rule(s), signed by %s\n", st.PolicyVersion, st.PolicyRules, st.PolicySigner)
	}
	if st.Exit {
		fmt.Fprintln(w, "  offers:   exit node")
	}
	if len(st.Routes) > 0 {
		fmt.Fprintf(w, "  routes:   %s (subnet router)\n", strings.Join(st.Routes, ", "))
	}
	if st.UseExit != "" {
		state := "waiting for its link"
		if st.ExitActive {
			state = "active"
		}
		fmt.Fprintf(w, "  exit via: %s (%s)\n", cli.Sanitize(st.UseExit), state)
	}
	for _, l := range st.Learned {
		fmt.Fprintf(w, "  using:    %s\n", l)
	}
	for _, n := range st.Notes {
		fmt.Fprintf(w, "  note:     %s\n", cli.Sanitize(n))
	}
}

func printPeers(w io.Writer, st *Status) {
	if len(st.Peers) == 0 {
		fmt.Fprintln(w, "no other devices in the mesh (log in on another device with `remohop login`)")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tONLINE\tLINK\tPATH\tTX/RX\tOFFERS")
	for _, p := range st.Peers {
		online := "no"
		if p.Online {
			online = "yes"
		}
		link := p.Link
		if link == "" {
			link = "-"
			if p.LastError != "" {
				link = "failed"
			}
		}
		path := p.Path
		if path == "" {
			path = "-"
		}
		var offers []string
		if p.Exit {
			offers = append(offers, "exit")
		}
		offers = append(offers, p.Routes...)
		o := strings.Join(offers, ",")
		if o == "" {
			o = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\t%s\n", cli.Sanitize(p.FQDN), p.Addr4, online, link, path, p.Tx, p.Rx, o)
	}
	tw.Flush()
	for _, p := range st.Peers {
		if p.Link == "" && p.LastError != "" {
			fmt.Fprintf(w, "%s: %s\n", cli.Sanitize(p.Name), cli.Sanitize(p.LastError))
		}
	}
}

func runPolicy(env *cli.Env, args []string) int {
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("net policy: %v", err)
		return cli.ExitError
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "--json") {
		sp, err := loadPolicy(dir)
		if err != nil {
			env.Errorf("net policy: %v", err)
			return cli.ExitError
		}
		if sp == nil {
			if len(args) == 1 {
				fmt.Fprintln(env.Stdout, "null")
			} else {
				fmt.Fprintln(env.Stdout, "no policy: every device of the mesh may reach every port of every other")
			}
			return cli.ExitOK
		}
		p, err := ParsePolicy(sp.Policy)
		if err != nil {
			env.Errorf("net policy: the stored policy is invalid: %v", err)
			return cli.ExitError
		}
		b, _ := json.MarshalIndent(p, "", "  ")
		if len(args) == 0 {
			fmt.Fprintf(env.Stdout, "# signed by %s\n", auth.Fingerprint(sp.Signer))
		}
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	var p *Policy
	switch {
	case args[0] == "set" && len(args) == 2:
		b, err := os.ReadFile(args[1])
		if err != nil {
			env.Errorf("net policy set: %v", err)
			return cli.ExitError
		}
		if p, err = ParsePolicy(b); err != nil {
			env.Errorf("net policy set: %v", err)
			return cli.ExitUsage
		}
	case args[0] == "reset" && len(args) == 1:
		p = &Policy{Rules: []Rule{{From: []string{"*"}, To: []string{"*"}}}}
	default:
		env.Errorf("usage: remohop net policy [--json] | remohop net policy set FILE | remohop net policy reset")
		return cli.ExitUsage
	}
	sp, err := setPolicy(dir, p)
	if err != nil {
		env.Errorf("net policy: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "net policy: version %d installed, signed by this device (%s); it reaches the other devices as they connect\n",
		p.Version, auth.Fingerprint(sp.Signer))
	return cli.ExitOK
}

// setPolicy signs p as the next version and stores it; the running host
// adopts it and hands it on.
func setPolicy(dir string, p *Policy) (*SignedPolicy, error) {
	st, err := account.Load(dir)
	if err != nil || st.Status != account.StatusMember {
		return nil, errNoAccount
	}
	ls, err := st.Ledger()
	if err != nil {
		return nil, err
	}
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return nil, err
	}
	if !ls.IsMember(id.Public()) {
		return nil, errNoAccount
	}
	if cur, _ := loadPolicy(dir); cur != nil {
		if old, err := ParsePolicy(cur.Policy); err == nil && p.Version <= old.Version {
			p.Version = old.Version + 1
		}
	}
	if p.Version <= 0 {
		p.Version = 1
	}
	sp, err := SignPolicy(id, st.AccountID, p)
	if err != nil {
		return nil, err
	}
	if _, err := sp.Verify(st.AccountID, ls.IsMember); err != nil {
		return nil, errors.New("internal error: the new policy does not verify: " + err.Error())
	}
	return sp, savePolicy(dir, sp)
}
