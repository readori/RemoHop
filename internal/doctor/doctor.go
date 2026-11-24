// Package doctor is `remohop doctor`: one report of this machine's network
// situation as RemoHop sees it, Tailscale and the mesh network included, with
// what each finding means for connections.
package doctor

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/mesh"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/tailnet"
)

const usage = `Usage: remohop doctor [--json] [--offline]

Explain this machine's network situation as RemoHop sees it: its identity,
the host daemon, the rendezvous server (reachable? which NAT type?), the
addresses it offers to peers, a running Tailscale and what it changes for
RemoHop, the mesh network (` + "`remohop net`" + `) and VPN tunnels. Ends with advice.

  --offline   do not contact the rendezvous server
  --json      machine-readable report

Tailscale is detected through its local API, its CLI (` + "`tailscale status --json`" + `)
or its network interface; REMOHOP_TAILSCALE=off makes RemoHop ignore it.`

func init() {
	cli.Register(&cli.Command{Name: "doctor", Summary: "explain the network situation (rendezvous, NAT, Tailscale, mesh)", Usage: usage, Run: run})
}

// Report is what remohop doctor finds.
type Report struct {
	Identity  string          `json:"identity"`
	ConfigDir string          `json:"config_dir"`
	Host      *HostReport     `json:"host,omitempty"`
	Signal    SignalReport    `json:"signal"`
	Addresses []string        `json:"addresses"`
	Tailscale *tailnet.Status `json:"tailscale"`
	Net       *mesh.Status    `json:"net,omitempty"`
	NetOn     bool            `json:"net_on"`
	VPN       int             `json:"vpn_tunnels"`
	Advice    []string        `json:"advice,omitempty"`
}

// HostReport is the running host daemon.
type HostReport struct {
	ID        string `json:"id,omitempty"`
	Online    bool   `json:"online"`
	NAT       string `json:"nat,omitempty"`
	Transport string `json:"transport,omitempty"`
	Sessions  int    `json:"sessions"`
}

// SignalReport is the rendezvous server.
type SignalReport struct {
	Server     string `json:"server,omitempty"`
	Pinned     bool   `json:"pinned"`
	Bridge     string `json:"bridge,omitempty"`
	Checked    bool   `json:"checked"`
	Reachable  bool   `json:"reachable"`
	NAT        string `json:"nat,omitempty"`
	Error      string `json:"error,omitempty"`
	RelayLocal bool   `json:"relay_allowed"`
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprintln(env.Stderr, usage) }
	asJSON := fs.Bool("json", false, "")
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	r, err := Collect(ctx, env.Globals.Signal, *offline)
	if err != nil {
		env.Errorf("doctor: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		b, _ := json.MarshalIndent(r, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	Print(env.Stdout, r)
	return cli.ExitOK
}

// Collect gathers the report.
func Collect(ctx context.Context, signalOverride string, offline bool) (*Report, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	r := &Report{ConfigDir: dir}
	if id, err := auth.LoadIdentity(dir); err == nil {
		r.Identity = auth.Fingerprint(id.Public())
	}
	if hs, _ := hostd.ReadStatus(dir); hs != nil {
		r.Host = &HostReport{ID: hs.ID, Online: hs.Online, NAT: hs.NAT, Transport: hs.Transport, Sessions: hs.Sessions}
	}
	r.Signal.RelayLocal = signal.RelayAllowed(dir)
	addr, sopts, rerr := signal.Resolve(signalOverride)
	if rerr == nil {
		r.Signal.Server, r.Signal.Pinned, r.Signal.Bridge = addr, sopts.Pin != "", sopts.WS
	} else {
		r.Signal.Error = rerr.Error()
	}
	for _, a := range nat.InterfaceAddrs() {
		s := a.String()
		if tailnet.InRange(a) && containsAddr(tailnet.LocalAddrs(), a) {
			s += " (tailnet)"
		}
		r.Addresses = append(r.Addresses, s)
		if len(r.Addresses) == nat.MaxCandidates {
			break
		}
	}
	tailnet.ResetCache()
	r.Tailscale = tailnet.Detect(ctx)
	if !offline && addr != "" && rerr == nil {
		r.Signal.Checked = true
		sock, err := nat.Listen("")
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
			c, err := signal.Dial(cctx, sock, addr, sopts)
			cancel()
			if err == nil {
				r.Signal.Reachable, r.Signal.NAT = true, string(c.NATType())
				c.Close()
			} else {
				r.Signal.Error = err.Error()
			}
			sock.Close()
		} else {
			r.Signal.Error = err.Error()
		}
	}
	values, _ := config.AllIn(dir)
	r.NetOn = config.Bool(values[mesh.SettingNet], false)
	r.Net, _ = mesh.ReadStatus(dir)
	if list, err := bg.List(); err == nil {
		for _, in := range list {
			if in.Kind == "vpn" {
				r.VPN++
			}
		}
	}
	r.Advice = advise(r)
	return r, nil
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// advise turns findings into advice.
func advise(r *Report) []string {
	var out []string
	switch {
	case r.Signal.Server == "" && r.Signal.Bridge == "":
		out = append(out, "No rendezvous server is configured: only direct addresses (remohop HOST:PORT) work. Set one with `remohop config set signal HOST:PORT`.")
	case r.Signal.Checked && !r.Signal.Reachable && r.Signal.Bridge == "":
		out = append(out, "The rendezvous server does not answer over UDP. If this network allows only HTTPS, configure its websocket bridge (`remohop config set signal-ws wss://...`).")
	case r.Signal.Checked && !r.Signal.Reachable:
		out = append(out, "The rendezvous server does not answer over UDP; connections will use its websocket bridge and therefore its relay.")
	}
	if !r.Signal.Pinned && r.Signal.Server != "" {
		out = append(out, "The rendezvous server's key is not pinned (`remohop signal --show-key` on the server, then `remohop config set signal-pin KEY`). Sessions stay end-to-end authenticated either way.")
	}
	if r.Signal.NAT == string(nat.TypeSymmetric) {
		if r.Tailscale.Running {
			out = append(out, "This machine is behind a symmetric NAT, which cannot be hole-punched. Peers on the same tailnet still connect directly over their tailnet addresses.")
		} else {
			out = append(out, "This machine is behind a symmetric NAT, which cannot be hole-punched: sessions to other NATed machines need the rendezvous server's relay (or a shared tailnet).")
		}
	}
	ts := r.Tailscale
	if ts.Running {
		out = append(out, "Tailscale is running. RemoHop offers its tailnet address as an extra candidate and prefers it when the other machine is on the same tailnet; the RemoHop session over it is still RemoHop's own, end-to-end encrypted and authenticated (`remohop <ID> ping` shows \"tailnet path\").")
		if ts.ExitNode != "" {
			out = append(out, fmt.Sprintf("Tailscale sends this machine's traffic through exit node %s: RemoHop sessions to machines outside the tailnet go that way too, and hole punching sees the exit node's NAT.", ts.ExitNode))
		}
		if r.Net != nil && r.Net.Up && ts.MagicDNS {
			out = append(out, "Tailscale's MagicDNS and the mesh's names do not collide: the mesh answers only .remohop names.")
		}
	}
	if ts.Source == tailnet.SourceInterface {
		out = append(out, "A Tailscale interface was found, but neither its local API nor the tailscale command answered: any 100.64.0.0/10 address a peer offers counts as a tailnet address.")
	}
	if r.NetOn && r.Net == nil && r.Host == nil {
		out = append(out, "The mesh (remohop net) is switched on, but the host daemon is not running, so it is down: start `remohop host` or the host service.")
	}
	if r.Net != nil && !r.Net.Up && r.Net.Error != "" {
		out = append(out, "The mesh cannot come up: "+r.Net.Error)
	}
	return out
}

// Print writes the report for people.
func Print(w io.Writer, r *Report) {
	sec := func(title string) { fmt.Fprintf(w, "\n%s\n", title) }
	kv := func(k, v string) { fmt.Fprintf(w, "  %-14s %s\n", k+":", cli.Sanitize(v)) }
	fmt.Fprintln(w, "RemoHop doctor")
	sec("This machine")
	kv("identity", r.Identity)
	kv("config", r.ConfigDir)
	if r.Host != nil {
		h := "running"
		if r.Host.ID != "" {
			h += ", ID " + signal.FormatID(r.Host.ID)
		}
		if r.Host.Online {
			h += ", registered"
			if r.Host.Transport == "websocket" {
				h += " through the websocket bridge"
			}
		} else {
			h += ", not registered right now"
		}
		if r.Host.NAT != "" {
			h += ", NAT " + r.Host.NAT
		}
		kv("host", fmt.Sprintf("%s, %d session(s)", h, r.Host.Sessions))
	} else {
		kv("host", "not running (only needed to be reached; `remohop host`)")
	}

	sec("Rendezvous server")
	switch {
	case r.Signal.Server != "":
		pin := "key not pinned"
		if r.Signal.Pinned {
			pin = "key pinned"
		}
		kv("server", r.Signal.Server+" ("+pin+")")
	case r.Signal.Bridge == "":
		kv("server", "none configured")
	}
	if r.Signal.Bridge != "" {
		kv("bridge", r.Signal.Bridge)
	}
	if r.Signal.Checked {
		if r.Signal.Reachable {
			kv("over UDP", "reachable; NAT type "+r.Signal.NAT+" ("+natMeaning(r.Signal.NAT)+")")
		} else {
			kv("over UDP", "no answer: "+r.Signal.Error)
		}
	} else if r.Signal.Error != "" {
		kv("problem", r.Signal.Error)
	}
	relay := "accepted if the server offers one"
	if !r.Signal.RelayLocal {
		relay = "refused on this machine (relay setting or REMOHOP_RELAY=0)"
	}
	kv("relay", relay)

	sec("Addresses offered to peers")
	if len(r.Addresses) == 0 {
		fmt.Fprintln(w, "  (none besides the public address the rendezvous server sees)")
	}
	for _, a := range r.Addresses {
		fmt.Fprintf(w, "  %s\n", a)
	}

	sec("Tailscale")
	ts := r.Tailscale
	switch {
	case ts.Source == tailnet.SourceDisabled:
		kv("state", "ignored (REMOHOP_TAILSCALE=off)")
	case !ts.Running && ts.Source == tailnet.SourceNone:
		kv("state", "not found")
	case !ts.Running:
		kv("state", "installed but not running ("+ts.BackendState+")")
	default:
		state := "running"
		if ts.Version != "" {
			state += ", version " + ts.Version
		}
		kv("state", state+" (found via "+sourceName(ts.Source)+")")
		if ts.Tailnet != "" {
			kv("tailnet", ts.Tailnet)
		}
		var ips []string
		for _, a := range ts.Self.IPs {
			ips = append(ips, a.String())
		}
		kv("this machine", strings.Join(ips, ", ")+ifaceNote(ts.Interface))
		if ts.HasPeers() {
			online := 0
			for _, p := range ts.Peers {
				if p.Online {
					online++
				}
			}
			kv("peers", fmt.Sprintf("%d (%d online)", len(ts.Peers), online))
		}
		if ts.MagicDNS {
			kv("MagicDNS", "on ("+ts.MagicDNSSuffix+")")
		}
		if ts.ExitNode != "" {
			kv("exit node", ts.ExitNode)
		}
	}
	if ts.Note != "" {
		kv("note", ts.Note)
	}

	sec("Mesh (remohop net)")
	switch {
	case r.Net != nil && r.Net.Up:
		kv("state", fmt.Sprintf("up as %s (%s) on %s, %d peer(s)", r.Net.FQDN, r.Net.Addr4, r.Net.Device, len(r.Net.Peers)))
		kv("network", r.Net.Prefix4+" (beside Tailscale's 100.64.0.0/10)")
	case r.Net != nil:
		kv("state", "on, but down: "+r.Net.Error)
	case r.NetOn:
		kv("state", "on, waiting for the host daemon")
	default:
		kv("state", "off (`remohop net up`)")
	}
	if r.VPN > 0 {
		sec("VPN")
		kv("tunnels", fmt.Sprintf("%d (`remohop vpn`)", r.VPN))
	}
	if len(r.Advice) > 0 {
		sec("Advice")
		for _, a := range r.Advice {
			fmt.Fprintf(w, "  - %s\n", a)
		}
	}
}

func natMeaning(t string) string {
	switch nat.Type(t) {
	case nat.TypeNone:
		return "no NAT: directly reachable"
	case nat.TypeCone:
		return "hole punching works with most peers"
	case nat.TypeSymmetric:
		return "hole punching fails against other NATs; relay or tailnet needed"
	}
	return "could not be determined"
}

func sourceName(s string) string {
	switch s {
	case tailnet.SourceEnv:
		return "REMOHOP_TAILSCALE_STATUS"
	case tailnet.SourceLocalAPI:
		return "its local API"
	case tailnet.SourceCLI:
		return "`tailscale status --json`"
	case tailnet.SourceInterface:
		return "its network interface only"
	}
	return s
}

func ifaceNote(name string) string {
	if name == "" {
		return ""
	}
	return " on " + name
}
