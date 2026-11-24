package dial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

const devicesUsage = `Usage: remohop devices [--json] [--offline]

List the devices this machine can connect to, with whether each is online
right now. Devices are the hosts this machine has connected to or paired
with (the same list as ` + "`remohop peers`" + `), and, if this machine is logged in to
an account (` + "`remohop login`" + `), every device of the account.

Account devices come first, marked "account" in the first column, with
their name, platform and presence as the account's rendezvous server
reports them; only devices in the account's signed device list are shown.
A device of the account that has never run ` + "`remohop host`" + ` on that server has no
ID and shows as "not a host". Saved hosts that are not in the account
follow, marked "saved". Being in the account grants no access: connecting
still needs a credential the host accepts, unless its owner turned on
` + "`remohop config account-trust`" + ` there (LOGIN "account" means a key login is tried
first and a password asked for if the host refuses it). Without an
account the list and its columns are as they always were.

For hosts known by their 9-digit ID, the rendezvous server is asked in one
request which of them are registered at the moment. It is asked about
those IDs only, and answers no more than a connection attempt would:
online, offline, or unknown to it. Hosts known by address (HOST:PORT) are
not probed; ` + "`remohop <ID> ping`" + ` checks reachability for real.

Columns: the device, its alias, whether a pairing or key login is saved,
presence, when this machine last found it online, and background commands
(` + "`vpn`, `forward`, ..." + `) currently running to it.

Flags:
  --offline  do not ask the rendezvous server; show saved data only
  --json     machine-readable output

Examples:
  remohop devices
  remohop devices --json
`

func init() {
	cli.Register(&cli.Command{Name: "devices", Summary: "list saved devices and whether they are online", Usage: devicesUsage, Run: runDevices})
}

// Device is one row of `remohop devices`.
type Device struct {
	// Target is the 9-digit ID or HOST:PORT; empty for an account device
	// that has no ID.
	Target string `json:"target"`
	Alias  string `json:"alias,omitempty"`
	// Account marks a device of this machine's account; Name, Platform
	// and ThisDevice are only set for those.
	Account    bool   `json:"account,omitempty"`
	Name       string `json:"name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	ThisDevice bool   `json:"this_device,omitempty"`
	// Login is "pairing", "key", "account" (a key login the host accepts
	// if its owner turned account-trust on, else the password) or
	// "password".
	Login string `json:"login"`
	// Presence is "online", "offline", "unknown" (to the rendezvous
	// server), "rate_limited", "not_checked" (an address, or --offline),
	// "not_a_host" (an account device without an ID), or "error" with
	// Error set.
	Presence     string     `json:"presence"`
	Error        string     `json:"error,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
	HostIdentity string     `json:"host_identity,omitempty"`
	// Running lists the kinds of background commands (vpn, forward, ...)
	// this machine has running to the device.
	Running []string `json:"running,omitempty"`
}

// DevicesOptions configures Devices.
type DevicesOptions struct {
	Options
	// Offline skips the rendezvous server.
	Offline bool
}

// Devices lists the account's devices, when this machine is logged in to
// one, followed by the saved peers that are not among them, each with its
// presence.
func Devices(ctx context.Context, opts DevicesOptions) ([]Device, error) {
	store, err := OpenStore(opts.Dir)
	if err != nil {
		return nil, err
	}
	peers, err := store.List()
	if err != nil {
		return nil, err
	}
	acct, inAccount := accountDevices(ctx, opts, peers)
	running := map[string][]string{}
	if procs, err := bg.List(); err == nil {
		for _, p := range procs {
			running[p.Target] = append(running[p.Target], p.Kind)
		}
	}
	var ids []string
	out := make([]Device, 0, len(peers))
	for _, p := range peers {
		if inAccount[p.Target] {
			continue
		}
		d := Device{Target: p.Target, Alias: p.Alias, Login: "password", Presence: "not_checked", LastSeen: p.LastSeen}
		switch {
		case p.Paired():
			d.Login = "pairing"
		case p.KeyLogin:
			d.Login = "key"
		}
		if key, err := auth.DecodeKey(p.HostKey); err == nil {
			d.HostIdentity = auth.Fingerprint(key)
		}
		kinds := append(running[p.Target], running[p.Alias]...)
		sort.Strings(kinds)
		d.Running = kinds
		if cli.IsID(p.Target) {
			ids = append(ids, p.Target)
		}
		out = append(out, d)
	}
	for i := range acct {
		kinds := append(running[acct[i].Target], running[acct[i].Alias]...)
		sort.Strings(kinds)
		if acct[i].Target == "" {
			kinds = nil
		}
		acct[i].Running = kinds
	}
	if opts.Offline || len(ids) == 0 {
		return append(acct, out...), nil
	}
	presence, err := queryPresence(ctx, opts.Options, ids)
	now := time.Now().UTC().Truncate(time.Second)
	for i := range out {
		if !cli.IsID(out[i].Target) {
			continue
		}
		if err != nil {
			out[i].Presence, out[i].Error = "error", err.Error()
			continue
		}
		out[i].Presence = presence[out[i].Target]
		if out[i].Presence == signal.PresenceOnline {
			out[i].LastSeen = &now
			store.Update(out[i].Target, func(p *Peer) error { p.LastSeen = &now; return nil })
		}
	}
	return append(acct, out...), nil
}

// accountDevices returns the rows for the account's devices, and the
// targets they cover, or nothing when this machine is not in an account.
func accountDevices(ctx context.Context, opts DevicesOptions, peers []Peer) ([]Device, map[string]bool) {
	dir := opts.Dir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return nil, nil
		}
	}
	st, err := account.Load(dir)
	if err != nil || st.Status != account.StatusMember {
		return nil, nil
	}
	var list []account.Device
	presenceErr := ""
	if !opts.Offline {
		list, _, err = account.Devices(ctx, account.Options{Dir: dir, Logger: opts.Logger})
		if err != nil {
			presenceErr = err.Error()
		}
	}
	if list == nil {
		list, _, _ = account.CachedDevices(dir)
	}
	saved := map[string]Peer{}
	for _, p := range peers {
		saved[p.Target] = p
	}
	covered := map[string]bool{}
	var out []Device
	for _, a := range list {
		d := Device{Target: a.ID, Account: true, Name: a.Name, Platform: a.Platform, ThisDevice: a.This, Login: "account",
			HostIdentity: a.Identity, LastSeen: a.LastSeen}
		switch {
		case opts.Offline:
			d.Presence = "not_checked"
		case presenceErr != "":
			d.Presence, d.Error = "error", presenceErr
		case a.Online:
			d.Presence = signal.PresenceOnline
		case a.ID == "":
			d.Presence = "not_a_host"
		default:
			d.Presence = signal.PresenceOffline
		}
		if a.ID == "" && d.Presence != "error" && !opts.Offline {
			d.Presence = "not_a_host"
		}
		if p, ok := saved[a.ID]; ok && a.ID != "" {
			covered[a.ID] = true
			d.Alias = p.Alias
			switch {
			case p.Paired():
				d.Login = "pairing"
			case p.KeyLogin:
				d.Login = "key"
			}
			if p.LastSeen != nil && (d.LastSeen == nil || p.LastSeen.After(*d.LastSeen)) {
				d.LastSeen = p.LastSeen
			}
		}
		if a.This {
			d.Login = "-"
		}
		out = append(out, d)
	}
	return out, covered
}

func queryPresence(ctx context.Context, opts Options, ids []string) (map[string]string, error) {
	addr, sopts, err := signal.Resolve(opts.Signal)
	if err != nil {
		return nil, err
	}
	opts.applyTransport(&sopts)
	ctx, cancel := context.WithTimeout(ctx, signalTimeout)
	defer cancel()
	sc, sock, err := signal.Connect(ctx, addr, sopts, func() (*nat.Socket, error) {
		if opts.PacketConn != nil {
			return nat.NewSocket(opts.PacketConn)
		}
		return nat.Listen("")
	}, opts.Logger)
	if err != nil {
		return nil, err
	}
	defer sock.Close()
	defer sc.Close()
	out := map[string]string{}
	for len(ids) > 0 {
		n := min(len(ids), signal.MaxPresenceIDs)
		part, err := sc.Presence(ctx, ids[:n])
		if err != nil {
			return nil, err
		}
		for k, v := range part {
			out[k] = v
		}
		ids = ids[n:]
	}
	return out, nil
}

func runDevices(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "devices", devicesUsage)
	asJSON := fs.Bool("json", false, "")
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return cli.ExitUsage
	}
	list, err := Devices(ctx, DevicesOptions{Options: Options{Signal: env.Globals.Signal}, Offline: *offline})
	if err != nil {
		env.Errorf("devices: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		b, _ := json.MarshalIndent(list, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no devices yet: connect to a host once (remohop <ID> ping) or pair with it (remohop <ID> pair)")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	withAccount := false
	for _, d := range list {
		withAccount = withAccount || d.Account
	}
	if withAccount {
		fmt.Fprintln(tw, "KIND\tDEVICE\tNAME\tALIAS\tLOGIN\tPRESENCE\tLAST SEEN\tPLATFORM\tRUNNING")
	} else {
		fmt.Fprintln(tw, "DEVICE\tALIAS\tLOGIN\tPRESENCE\tLAST SEEN\tRUNNING")
	}
	var failure string
	for _, d := range list {
		alias, seen, running := d.Alias, "-", "-"
		if alias == "" {
			alias = "-"
		}
		if d.LastSeen != nil {
			seen = d.LastSeen.Local().Format("2006-01-02 15:04")
		}
		if len(d.Running) > 0 {
			running = strings.Join(d.Running, ",")
		}
		presence := strings.ReplaceAll(d.Presence, "_", " ")
		if d.Presence == "error" {
			failure = d.Error
		}
		if !withAccount {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", cli.Sanitize(displayTarget(d.Target)), cli.Sanitize(alias), d.Login, presence, seen, cli.Sanitize(running))
			continue
		}
		kind, target, name, platform := "saved", displayTarget(d.Target), d.Name, d.Platform
		if d.Account {
			kind = "account"
		}
		if target == "" {
			target = "-"
		}
		if d.ThisDevice {
			name += " (this device)"
		}
		if name == "" {
			name = "-"
		}
		if platform == "" {
			platform = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", kind, cli.Sanitize(target), cli.Sanitize(name), cli.Sanitize(alias), d.Login,
			presence, seen, cli.Sanitize(platform), cli.Sanitize(running))
	}
	tw.Flush()
	if failure != "" {
		env.Errorf("devices: presence unavailable: %s", failure)
		if errors.Is(err, signal.ErrNotConfigured) {
			return cli.ExitError
		}
	}
	return cli.ExitOK
}
