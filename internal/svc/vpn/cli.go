package vpn

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
)

const usage = `Usage: remohop <ID> vpn up [--route CIDR]... [--exit [--bypass CIDR]...] [-b]
       remohop <ID> vpn down
       remohop vpn [--json]

Join this machine and a host with a virtual network link carried inside the
encrypted session. Each side gets a tunnel interface; this machine is
10.77.0.2 and the host 10.77.0.1 (another 10.77.x.y/30 if that is taken on
either machine, which is then reported).

  up      create the tunnel and keep it until interrupted, ` + "`vpn down`" + `, or the
          session ends
  down    stop this machine's tunnel to that host
  (none)  list this machine's tunnels

Flags for up:
  --route CIDR   also reach this network on the host's side through the
                 tunnel, e.g. the host's LAN: --route 192.168.1.0/24. The
                 host forwards for it and hides this machine behind its own
                 address. Repeatable.
  --exit         send ALL traffic of this machine through the host, as if it
                 were there. The local network, the session's own path to
                 the host and --bypass networks stay off the tunnel. DNS
                 goes through the tunnel too where systemd-resolved is in
                 use; elsewhere it keeps using the current resolvers.
  --bypass CIDR  with --exit: keep this network on its current route.
                 Repeatable.
  -b, --background   detach; ` + "`remohop <ID> vpn down`" + ` or ` + "`remohop stop`" + ` ends it

Both machines need the right to create network interfaces: on Linux run the
command as root (sudo) or grant the binary CAP_NET_ADMIN and CAP_NET_RAW; on
Windows run it as Administrator. IPv4 only.

Platforms: Linux on both ends is the tested combination. Windows can be the
controlling machine (and a host reached itself, without --route or --exit);
it needs the Wintun driver, which remohop does not include or download: get
Wintun 0.14.1 from https://www.wintun.net, check the zip's SHA-256
(07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51), and put
wintun\bin\<arch>\wintun.dll next to remohop.exe or into the remohop config
directory. A macOS client (utun) is tested on a hosted macOS VM, not on a
physical Mac; --exit from macOS has not been run on a real network, and a
macOS host cannot forward (--route, --exit).

On the host, the "vpn" feature must be on (` + "`remohop config feature vpn on`" + `, the
default). Routes to networks the host is not directly attached to, and
--exit, additionally need ` + "`remohop config vpn-exit on`" + ` there, which is off by
default.

Everything the tunnel sets up (interface, routes, firewall rules) is removed
when it ends. If remohop is killed outright, the next remohop command run with the
same privilege removes what was left.

Examples:
  sudo remohop 123456789 vpn up
  sudo remohop office vpn up --route 192.168.1.0/24 -b
  sudo remohop office vpn up --exit --bypass 10.0.0.0/8
  sudo remohop office vpn down
  remohop vpn`

func init() {
	cli.Register(&cli.Command{
		Name:      "vpn",
		Summary:   "IP tunnel to a host and its networks (remohop <ID> vpn up)",
		Usage:     usage,
		Run:       runList,
		RemoteRun: runRemote,
	})
}

func tunnels() ([]bg.Info, error) {
	all, err := bg.List()
	var out []bg.Info
	for _, in := range all {
		if in.Kind == "vpn" {
			out = append(out, in)
		}
	}
	return out, err
}

func runList(_ context.Context, env *cli.Env, args []string) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		if args[0] == "up" || args[0] == "down" {
			env.Errorf("vpn %s needs a target: remohop <ID> vpn %s", args[0], args[0])
		} else {
			env.Errorf("usage: remohop vpn [--json]  |  remohop <ID> vpn up|down")
		}
		return cli.ExitUsage
	}
	list, err := tunnels()
	if err != nil {
		env.Errorf("vpn: %v", err)
		return cli.ExitError
	}
	_, stale := Recover()
	if asJSON {
		if list == nil {
			list = []bg.Info{}
		}
		b, _ := json.MarshalIndent(list, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no VPN tunnels")
	} else {
		fmt.Fprintf(env.Stdout, "%-8s %-12s %-10s %s\n", "PID", "TARGET", "AGE", "TUNNEL")
		for _, in := range list {
			fmt.Fprintf(env.Stdout, "%-8d %-12s %-10s %s\n", in.PID, cli.Sanitize(in.Target), time.Since(in.Started).Round(time.Second), cli.Sanitize(in.Detail))
		}
	}
	if stale > 0 {
		env.Errorf("vpn: %d tunnel(s) of a killed remohop left settings behind; run any remohop command as root to remove them", stale)
	}
	return cli.ExitOK
}

type prefixList []netip.Prefix

func (l *prefixList) String() string { return fmt.Sprint(*l) }

func (l *prefixList) Set(v string) error {
	p, err := netip.ParsePrefix(v)
	if err != nil || !p.Addr().Is4() {
		return fmt.Errorf("%q is not an IPv4 network like 192.168.1.0/24", v)
	}
	if p != p.Masked() {
		return fmt.Errorf("%s has host bits set; did you mean %s?", p, p.Masked())
	}
	*l = append(*l, p)
	return nil
}

func runRemote(ctx context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("usage: remohop <ID> vpn up [--route CIDR]... [--exit] [-b]  |  remohop <ID> vpn down")
		return cli.ExitUsage
	}
	switch args[0] {
	case "down":
		return runDown(env, args[1:])
	case "up":
	default:
		env.Errorf("vpn: unknown subcommand %q (up, down)", args[0])
		return cli.ExitUsage
	}
	fs := flag.NewFlagSet("vpn up", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, usage) }
	var routes, bypass prefixList
	fs.Var(&routes, "route", "")
	fs.Var(&bypass, "bypass", "")
	exit := fs.Bool("exit", false, "")
	var background bool
	fs.BoolVar(&background, "b", false, "")
	fs.BoolVar(&background, "background", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("vpn up: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if len(bypass) > 0 && !*exit {
		env.Errorf("vpn up: --bypass only makes sense with --exit")
		return cli.ExitUsage
	}
	if _, err := parseRoutes(strings.Fields(strings.Trim(fmt.Sprint([]netip.Prefix(routes)), "[]"))); err != nil {
		env.Errorf("vpn up: %v", err)
		return cli.ExitUsage
	}
	// Fail on missing privilege before connecting, or detaching.
	if err := sys.privileged(); err != nil {
		env.Errorf("vpn up: %v", err)
		return cli.ExitError
	}
	if list, _ := tunnels(); !bg.InChild() {
		for _, in := range list {
			if in.Target == env.Target {
				env.Errorf("vpn up: a tunnel to %s is already up (pid %d); `remohop %s vpn down` stops it", env.Target, in.PID, env.Target)
				return cli.ExitError
			}
		}
	}
	if background && !bg.InChild() {
		msg, _, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("vpn up: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		return cli.ExitOK
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	t, err := Up(ctx, conn, Options{Routes: routes, Exit: *exit, Bypass: bypass})
	if err != nil {
		env.Errorf("vpn up: %v", err)
		return cli.ExitError
	}
	detail := fmt.Sprintf("%s %s -> %s", t.Device, t.Local, t.Peer)
	if len(routes) > 0 {
		detail += ", routes " + strings.Trim(fmt.Sprint([]netip.Prefix(routes)), "[]")
	}
	if *exit {
		detail += ", exit (all traffic)"
	}
	msg := "vpn up: " + detail + fmt.Sprintf(" (mtu %d, tcp mss %d)", t.MTU, t.MSS)
	for _, n := range t.Notes {
		msg += "\nnote: " + n
	}
	done := bg.Ready(bg.Info{Kind: "vpn", Target: env.Target, Detail: detail}, msg)
	defer done()
	if !bg.InChild() {
		fmt.Fprintln(env.Stdout, msg)
	}
	if err := t.Run(ctx); err != nil {
		env.Errorf("vpn: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

func runDown(env *cli.Env, args []string) int {
	if len(args) != 0 {
		env.Errorf("usage: remohop <ID> vpn down")
		return cli.ExitUsage
	}
	list, err := tunnels()
	if err != nil {
		env.Errorf("vpn down: %v", err)
		return cli.ExitError
	}
	stopped := 0
	for _, in := range list {
		if in.Target != env.Target {
			continue
		}
		if err := bg.Stop(in.PID); err != nil {
			env.Errorf("vpn down: stopping pid %d: %v", in.PID, err)
			return cli.ExitError
		}
		stopped++
		fmt.Fprintf(env.Stdout, "vpn down: stopped the tunnel to %s (pid %d)\n", env.Target, in.PID)
	}
	Recover()
	if stopped == 0 {
		env.Errorf("vpn down: no tunnel to %s is up (see `remohop vpn`)", env.Target)
		return cli.ExitError
	}
	return cli.ExitOK
}
