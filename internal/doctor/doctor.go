// Package doctor is `remohop doctor`: one report of this machine's network
// situation as RemoHop sees it, Tailscale and the mesh network included, with
// what each finding means for connections.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/ffmpeg"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/mesh"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/svc/desktop"
	"github.com/readori/RemoHop/internal/tailnet"
)

const usage = `Usage: remohop doctor [--json] [--offline] [TARGET]
       remohop TARGET doctor [--json] [--offline]

Explain this machine's network situation as RemoHop sees it: its identity,
the host daemon, the rendezvous server (reachable? which NAT type?), the
addresses it offers to peers, whether helpers could see its screen (ffmpeg
and a display), a running Tailscale and what it changes for RemoHop, the
mesh network (` + "`remohop net`" + `) and VPN tunnels. Ends with advice.

With a TARGET (a 9-digit ID, a saved name or HOST:PORT) it also looks that
machine up: what is saved for it here, and whether the rendezvous server
has it online right now. It does not log in; ` + "`remohop TARGET ping`" + ` shows the path
a session takes.

  --offline   do not contact the rendezvous server
  --json      machine-readable report

Tailscale is detected through its local API, its CLI (` + "`tailscale status --json`" + `)
or its network interface; REMOHOP_TAILSCALE=off makes RemoHop ignore it.`

func init() {
	cli.Register(&cli.Command{Name: "doctor", Summary: "explain the network situation (rendezvous, NAT, Tailscale, mesh), or why a host cannot be reached",
		Usage: usage, Run: run, RemoteRun: run})
}

// Report is what remohop doctor finds.
type Report struct {
	Identity  string          `json:"identity"`
	ConfigDir string          `json:"config_dir"`
	Host      *HostReport     `json:"host,omitempty"`
	Signal    SignalReport    `json:"signal"`
	Addresses []string        `json:"addresses"`
	Screen    ScreenReport    `json:"screen"`
	Target    *TargetReport   `json:"target,omitempty"`
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
	// PinMismatch: the server presented another key than signal-pin.
	PinMismatch bool `json:"pin_mismatch,omitempty"`
	// WrongServer: something answered at the address, but not a
	// rendezvous server.
	WrongServer bool `json:"wrong_server,omitempty"`
}

// ScreenReport is what screen sharing to helpers needs here.
type ScreenReport struct {
	FFmpeg string `json:"ffmpeg,omitempty"`
	// FFmpegKind is where it comes from: env (REMOHOP_FFMPEG), system
	// (PATH) or remohop (RemoHop's copy, `remohop ffmpeg install`).
	FFmpegKind ffmpeg.Kind `json:"ffmpeg_kind,omitempty"`
	// FFmpegVersion is the first line of `ffmpeg -version`, empty when it
	// did not run.
	FFmpegVersion string `json:"ffmpeg_version,omitempty"`
	FFmpegError   string `json:"ffmpeg_error,omitempty"`
	Display       string `json:"display,omitempty"`
	DisplayError  string `json:"display_error,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

// TargetReport is a machine looked up with ` + "`remohop doctor TARGET`" + `.
type TargetReport struct {
	Target string `json:"target"`
	// ID is the 9-digit ID dialled, if any (an alias resolves to it).
	ID      string `json:"id,omitempty"`
	Address string `json:"address,omitempty"`
	Saved   bool   `json:"saved"`
	Alias   string `json:"alias,omitempty"`
	// Login is how this machine would log in: pairing, grant, key,
	// saved_password or password (asked for).
	Login string `json:"login"`
	// Presence is online, offline, unknown (no host has the ID),
	// rate_limited, not_checked or error.
	Presence string `json:"presence"`
	Error    string `json:"error,omitempty"`
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "doctor", usage)
	asJSON := fs.Bool("json", false, "")
	offline := fs.Bool("offline", false, "")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	target := env.Target
	switch {
	case len(pos) == 1 && target == "":
		target = cli.NormalizeTarget(pos[0])
	case len(pos) > 0:
		env.Errorf("doctor: unexpected argument %q (%s)", pos[len(pos)-1], cli.FlagHint("doctor"))
		return cli.ExitUsage
	}
	r, err := Collect(ctx, env.Globals.Signal, *offline)
	if err != nil {
		env.Errorf("doctor: %v", err)
		return cli.ExitError
	}
	if target != "" {
		// Asking a server that is missing or did not answer again would
		// only wait for the same failure.
		down := ""
		switch {
		case r.Signal.Server == "" && r.Signal.Bridge == "":
			down = "no rendezvous server is configured (see the advice)"
		case r.Signal.Checked && !r.Signal.Reachable && r.Signal.Bridge == "":
			down = "the rendezvous server does not answer (see above)"
		}
		r.Target = CheckTarget(ctx, target, env.Globals.Signal, *offline || down != "")
		if down != "" && r.Target.ID != "" && !*offline {
			r.Target.Presence, r.Target.Error = "error", down
		}
		r.Advice = append(r.Advice, adviseTarget(r)...)
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
	switch {
	case rerr == nil:
		r.Signal.Server, r.Signal.Pinned, r.Signal.Bridge = addr, sopts.Pin != "", sopts.WS
	case errors.Is(rerr, signal.ErrNotConfigured):
		// Said by "none configured" and the advice, which list the choices.
	default:
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
				r.Signal.PinMismatch = errors.Is(err, signal.ErrPinMismatch)
				r.Signal.WrongServer = signal.IsWrongServer(err)
			}
			sock.Close()
		} else {
			r.Signal.Error = err.Error()
		}
	}
	if f, err := desktop.FindFFmpeg(); err == nil {
		r.Screen.FFmpeg, r.Screen.FFmpegKind = f.Path, f.Kind
		r.Screen.FFmpegVersion = ffmpeg.Version(ctx, f.Path)
	} else {
		r.Screen.FFmpegError = err.Error()
	}
	if found, warn, err := desktop.CheckScreen(); err == nil {
		r.Screen.Display, r.Screen.Warning = found, warn
	} else {
		r.Screen.DisplayError = err.Error()
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
		out = append(out, "No rendezvous server is configured, so 9-digit IDs cannot be looked up. Choose one:\n"+
			strings.TrimRight(signal.Choices("      "), "\n"))
	case r.Signal.PinMismatch:
		out = append(out, "The rendezvous server presented another key than the one pinned with `signal-pin`, so this machine refuses it. "+
			"If its operator reinstalled it or you moved to another server, get the new key from them (`remohop signal --show-key` on the server) "+
			"and run `remohop config set signal-pin KEY`; `remohop config unset signal-pin` stops checking. "+
			"Otherwise something on the way is pretending to be the server: keep the pin.")
	case r.Signal.WrongServer:
		out = append(out, "Something answers at the rendezvous server's address, but it is not a RemoHop rendezvous server (see the problem above). "+
			"Check the address and port: `remohop config get signal`.")
	case r.Signal.Checked && !r.Signal.Reachable && r.Signal.Bridge == "":
		out = append(out, fmt.Sprintf("The rendezvous server %s does not answer. Check, in this order: the address and port "+
			"(`remohop config get signal`); that `remohop signal` is running there; that its firewall and its cloud provider's let UDP %s in "+
			"(`remohop signal --print-firewall` on the server prints the commands). Only if this network lets nothing but HTTPS out, "+
			"use the server's websocket bridge as the last resort (`remohop config set signal-ws wss://...`).", r.Signal.Server, udpPorts(r.Signal.Server)))
	case r.Signal.Checked && !r.Signal.Reachable:
		out = append(out, "The rendezvous server does not answer over UDP; connections will use its websocket bridge and therefore its relay.")
	}
	if r.Screen.FFmpegError != "" {
		out = append(out, "Helpers cannot see this machine's screen live: "+r.Screen.FFmpegError+
			" Screenshots (`remohop ID screenshot`), files and commands work without it.")
	} else if r.Screen.DisplayError != "" {
		out = append(out, "Helpers cannot see this machine's screen from here: "+r.Screen.DisplayError+
			". A host started in the desktop session (or the desktop app) usually can.")
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

	sec("Screen sharing (for helpers)")
	if r.Screen.FFmpeg != "" {
		kv("ffmpeg", r.Screen.FFmpeg+" ("+r.Screen.FFmpegKind.Describe()+")")
		v := r.Screen.FFmpegVersion
		if v == "" {
			v = "unknown: it did not run"
		}
		kv("version", v)
	} else {
		kv("ffmpeg", "not found (screen sharing needs it; screenshots do not)")
	}
	switch {
	case r.Screen.Display != "":
		kv("screen", r.Screen.Display)
	case r.Screen.DisplayError != "":
		kv("screen", "not found from this process: "+r.Screen.DisplayError)
	}
	if r.Screen.Warning != "" {
		kv("note", r.Screen.Warning)
	}

	if t := r.Target; t != nil {
		sec("Target " + t.Target)
		switch {
		case t.ID != "":
			kv("ID", signal.FormatID(t.ID))
		case t.Address != "":
			kv("address", t.Address+" (no rendezvous server involved)")
		}
		saved := "no"
		if t.Saved {
			saved = "yes"
			if t.Alias != "" {
				saved += ", as " + t.Alias
			}
		}
		kv("saved here", saved)
		kv("login", loginText(t.Login))
		kv("lookup", presenceText(t, r.Signal.Server))
		kv("path", "`remohop "+t.Target+" ping` shows it once this machine can log in")
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

// udpPorts names the UDP ports a rendezvous server at addr needs open: its
// port and the next one (NAT detection).
func udpPorts(addr string) string {
	_, port, err := signal.SplitAddr(addr)
	if n, perr := strconv.Atoi(port); err == nil && perr == nil && n < 65535 {
		return fmt.Sprintf("%d-%d", n, n+1)
	}
	return "21116-21117"
}

// CheckTarget looks target up without logging in: what this machine has
// saved for it, and, for an ID, whether the rendezvous server has it online.
func CheckTarget(ctx context.Context, target, signalOverride string, offline bool) *TargetReport {
	t := &TargetReport{Target: target, Login: "password", Presence: "not_checked"}
	store, err := dial.OpenStore("")
	if err != nil {
		t.Presence, t.Error = "error", err.Error()
		return t
	}
	p, ok, _ := store.Get(target)
	if !ok {
		p, ok, _ = store.ByAlias(target)
	}
	if ok {
		t.Saved, t.Alias = true, p.Alias
		switch {
		case p.Paired() && p.Grant:
			t.Login = "grant"
		case p.Paired():
			t.Login = "pairing"
		case p.KeyLogin:
			t.Login = "key"
		case p.SavedPassword():
			t.Login = "saved_password"
		}
		target = p.Target
	}
	switch {
	case cli.IsID(target):
		t.ID = target
	case strings.Contains(target, ":"):
		t.Address = target
		return t
	default:
		t.Presence, t.Error = "error", "not a 9-digit ID, a saved name or HOST:PORT"
		return t
	}
	if offline {
		return t
	}
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	got, err := dial.Presence(cctx, dial.Options{Signal: signalOverride}, []string{t.ID})
	switch {
	case err != nil:
		t.Presence, t.Error = "error", err.Error()
	case got[t.ID] != "":
		t.Presence = got[t.ID]
	default:
		t.Presence = signal.PresenceUnknown
	}
	return t
}

func loginText(login string) string {
	switch login {
	case "pairing":
		return "a saved pairing (no password needed)"
	case "grant":
		return "a saved grant (limited access, see `remohop TARGET limits`)"
	case "key":
		return "this machine's key, authorised there"
	case "saved_password":
		return "a saved password"
	}
	return "nothing saved: the first connection asks for its password"
}

func presenceText(t *TargetReport, server string) string {
	if server == "" {
		server = "the rendezvous server"
	}
	switch t.Presence {
	case signal.PresenceOnline:
		return "online: registered with " + server + " right now"
	case signal.PresenceOffline:
		return "offline: the ID exists on " + server + ", but its host is not connected"
	case signal.PresenceUnknown:
		return "unknown to " + server + ": no host has this ID there"
	case signal.PresenceLimited:
		return "not answered: " + server + " is limiting requests; try again in a minute"
	case "error":
		return "not possible: " + t.Error
	}
	if t.Address != "" {
		return "not needed for an address"
	}
	return "not checked (--offline)"
}

// adviseTarget says what to do about the target looked up.
func adviseTarget(r *Report) []string {
	t := r.Target
	if t == nil {
		return nil
	}
	var out []string
	switch t.Presence {
	case signal.PresenceOffline:
		out = append(out, fmt.Sprintf("%s is not connected to the rendezvous server: start RemoHop there (`remohop host`, the desktop app or "+
			"Quick Support) and check that the computer is awake and online.", t.Target))
	case signal.PresenceUnknown:
		out = append(out, fmt.Sprintf("No host has the ID %s on this rendezvous server. Check the ID with the person at that computer, "+
			"and that both computers use the same server: an ID belongs to the server the host registered with.", signal.FormatID(t.ID)))
	case signal.PresenceOnline:
		out = append(out, fmt.Sprintf("%s is online. If connecting still fails, `remohop -v %s ping` shows where it stops.", t.Target, t.Target))
	}
	if t.Address != "" {
		out = append(out, fmt.Sprintf("%s is an address: RemoHop must be listening there on that UDP port (`remohop host --listen`), "+
			"reachable through any firewall on the way. `remohop %s ping` tries it.", t.Address, t.Target))
	}
	if t.Login == "password" {
		out = append(out, fmt.Sprintf("Nothing is saved to log in to %s: the first connection asks for its password; `remohop %s pair` once "+
			"stops that.", t.Target, t.Target))
	}
	return out
}
