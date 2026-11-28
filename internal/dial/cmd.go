package dial

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/lan"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/portmap"
	"github.com/readori/RemoHop/internal/signal"
)

const pairUsage = `Usage: remohop <ID> pair [--name ALIAS] [--label TEXT] [--json]

Pair with a host so that later connections need no password. The host mints
a random 256-bit token bound to this machine's identity; it is stored here
(mode 0600) and used automatically from then on, also after the host's
temporary password has changed. Scripts and AI agents should pair once and
then run without any password.

The first connection needs the password: type it at the prompt, or pass it
with the global --password flag or REMOHOP_PASSWORD.

Flags:
  --name ALIAS   also save ALIAS as a name for this host: remohop ALIAS shell ...
  --label TEXT   how this machine appears in ` + "`remohop host paired`" + ` on the
                 host (default: this machine's hostname)
  --json         print the result as JSON

Pairing again replaces this machine's previous token on that host. The host
can list and revoke pairings with ` + "`remohop host paired`" + ` / ` + "`remohop host revoke`" + `;
` + "`remohop peers forget`" + ` discards the token on this side.

Examples:
  remohop 123456789 pair
  REMOHOP_PASSWORD=abcd-efgh remohop 123456789 pair --name office
  remohop office shell
`

const pingUsage = `Usage: remohop <ID> ping [-c N] [-i SECONDS] [--json]

Measure the round-trip time to a host over the session itself and show the
path it takes: the UDP addresses on both ends and whether the path is

  lan      direct to the host's private address (same network)
  direct   direct to the host's own public address or to a given HOST:PORT
  punched  through NAT mappings opened by UDP hole punching
  relayed  no direct path exists and the rendezvous server passes the
           packets (only if its operator enabled that and neither side
           turned it off with ` + "`remohop config relay off`" + ` or REMOHOP_RELAY=0)

The first three are peer to peer. A relayed session is as encrypted and
authenticated as any other (the relay sees ciphertext), but slower and
subject to the relay's bandwidth cap; it is announced on stderr when it is
set up (REMOHOP_QUIET=1 silences the notice).

Flags:
  -c N         number of probes (default 4)
  -i SECONDS   interval between probes (default 0.2)
  --json       print one JSON object instead of text

Examples:
  remohop 123456789 ping
  remohop office ping -c 10 --json
`

const keyUsage = `Usage: remohop key show
       remohop key export [--label TEXT]
       remohop key gen --yes
       remohop key import FILE|- --yes
       remohop key export --private FILE
       remohop key clear --yes

Manage this machine's identity: the key pair that names it to every host it
connects to and every client that connects to it.

  show     print this machine's key fingerprint: what a host shows when
           this machine asks to connect, so the two can be compared
  export   print the public key as one line. Give it to the owner of a
           host; with ` + "`remohop host authorize \"LINE\"`" + ` there, this machine may log
           in with its key alone, no password ever typed. The host prints a
           line in return; pass it to ` + "`remohop peers trust`" + `.
           --private FILE writes the PRIVATE key to FILE (mode 0600), to move
           this identity to another machine with ` + "`remohop key import`" + `.
  gen      replace the identity with a new one
  import   replace the identity with the private key in FILE (- for stdin)
  clear    remove the identity; a new one is made the next time one is needed

gen, import and clear need --yes, because the old identity is gone for good:
every pairing and authorised key that hosts hold for this machine stops
working, every client that pinned this machine as a host refuses it until
its user runs ` + "`remohop peers forget`" + `, and a host running here gets a NEW
9-digit ID from the rendezvous server (IDs belong to keys). A host already
running keeps its old key until it is restarted.

Examples:
  remohop key export
  remohop host authorize "$(ssh laptop remohop key export)"     # on the host
  remohop key gen --yes
`

const peersUsage = `Usage: remohop peers [--json]
       remohop peers alias TARGET NAME
       remohop peers trust TARGET "HOST-KEY-LINE"
       remohop peers forget TARGET|ALIAS...
       remohop peers redeem TARGET GRANT-CODE [--alias NAME] [--replace]

List the hosts this machine has connected to: their ID or address, alias,
whether a pairing token is saved (and whether it is a grant), and the fingerprint of the host identity
key pinned at the first login.

  alias    give a host (9-digit ID or HOST:PORT) a name usable as a target
  trust    record a host's identity key, obtained from its owner (the line
           ` + "`remohop host authorize`" + ` printed), and log in to it with this
           machine's key from now on
  forget   remove a host's saved key, pairing token and alias. This is how
           to accept a host whose identity has legitimately changed.
  redeem   store a grant code from ` + "`remohop host grant`" + ` on that host: from now on
           this machine (and ` + "`remohop mcp`" + ` here) logs in to it within the
           grant's limits. A full pairing saved for that host is not
           replaced unless --replace is given: to keep both, redeem the
           grant in a separate config directory for the agent, e.g.
           REMOHOP_CONFIG_DIR=~/.config/remohop-agent remohop peers redeem ...
           (and run its ` + "`remohop mcp`" + ` with the same variable).

Examples:
  remohop peers
  remohop peers alias 123456789 office
  remohop peers forget office
`

const configUsage = `Usage: remohop config [--json]
       remohop config get KEY
       remohop config set KEY VALUE
       remohop config unset KEY
       remohop config feature NAME on|off
       remohop config ask|ask-paired|otp|relay|portmap|lan-discovery|outcomes|account-trust on|off

Read and change settings in the remohop config file (config.json in the config
directory, $REMOHOP_CONFIG_DIR or the platform default). Without arguments all
keys are listed with their current values.

On a host, what connected clients may do (takes effect at once, also for
sessions that are already connected):
  feature NAME on|off   NAME is shell, file, forward, clip, screen, input,
                        info, vpn, serial or usb; all are on by default
  ask on|off            password logins need approval at the host
  ask-paired on|off     with ask on, paired clients need approval too
  otp on|off            the temporary password is replaced after each use
  account-trust on|off  accept the other devices of this machine's account
                        (` + "`remohop login`" + `) like authorised keys, checked against
                        the account's signed device list; default off. An
                        account grants no access without it
  portmap on|off        ask the router (PCP, NAT-PMP or UPnP) to forward the
                        host's UDP port, so clients behind any NAT can reach it
                        directly; default on (REMOHOP_PORTMAP=0 turns it off)
  lan-discovery on|off  answer ` + "`remohop lan`" + ` and the desktop app's "On this
                        network" with this host's name, address, ID and key
                        fingerprint (never a password); default on
                        (REMOHOP_LAN_DISCOVERY=0 turns it off)
On a client or a host:
  relay on|off          accept a relayed connection when no direct one is
                        possible and the rendezvous server offers it
On a client:
  outcomes on|off       after each connection by ID, tell the rendezvous
                        server which kind of path it took (LAN, punched,
                        relayed, ...), how long it took and the two NAT
                        types, with no ID or address, for its aggregate
                        counts; default on (REMOHOP_OUTCOMES=0 turns it off)

Examples:
  remohop config feature input off
  remohop config set signal rendezvous.example.net
  remohop config get signal
  remohop config unset host-password
`

func init() {
	cli.Register(&cli.Command{Name: "pair", Summary: "pair with a host: no password on later connections", Usage: pairUsage, Remote: true, Run: runPair})
	cli.Register(&cli.Command{Name: "ping", Summary: "measure round-trip time and show the P2P path", Usage: pingUsage, Remote: true, Run: runPing})
	cli.Register(&cli.Command{Name: "key", Summary: "show this machine's key fingerprint; export, import or replace its identity key", Usage: keyUsage, Run: runKey})
	hostd.SavedPeers = func() (int, error) {
		store, err := OpenStore("")
		if err != nil {
			return 0, err
		}
		peers, err := store.List()
		return len(peers), err
	}
	cli.Register(&cli.Command{Name: "peers", Summary: "list, name or forget saved hosts", Usage: peersUsage, Run: runPeers})
	cli.Register(&cli.Command{Name: "config", Summary: "show or change settings (rendezvous server, ...)", Usage: configUsage, Run: runConfig})
}

func newFlags(env *cli.Env, name, usage string) *flag.FlagSet {
	fs := cli.NewFlagSet(env, name, usage)
	return fs
}

func printJSON(env *cli.Env, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(env.Stdout, string(b))
}

// displayTarget formats a canonical target for people.
func displayTarget(t string) string {
	if cli.IsID(t) {
		return signal.FormatID(t)
	}
	return t
}

// SavePairing stores a pairing obtained with hostd.Pair for the host behind
// a session made by Dial, optionally under an alias, so later Dials log in
// with it.
func SavePairing(info *Info, p *hostd.Pairing, alias string, dir string) error {
	if alias != "" {
		if err := CheckAlias(alias); err != nil {
			return err
		}
	}
	store, err := OpenStore(dir)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	err = store.Update(info.Target, func(peer *Peer) error {
		peer.TokenID, peer.Token, peer.PairedAt, peer.Grant = p.TokenID, p.Token, &now, false
		if peer.HostKey == "" {
			peer.HostKey = auth.EncodeKey(info.HostKey)
		}
		return nil
	})
	if err != nil || alias == "" {
		return err
	}
	return store.SetAlias(info.Target, alias)
}

func runPair(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "pair", pairUsage)
	alias := fs.String("name", "", "")
	label := fs.String("label", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("pair: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if *alias != "" {
		if err := CheckAlias(*alias); err != nil {
			env.Errorf("pair: %v", err)
			return cli.ExitUsage
		}
	}
	if *label == "" {
		*label, _ = os.Hostname()
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	info := InfoOf(conn)
	if info == nil {
		env.Errorf("pair: this connection was not established by remohop and cannot be paired")
		return cli.ExitError
	}
	pairing, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: *label})
	if err != nil {
		env.Errorf("pair: %v", err)
		return cli.ExitError
	}
	if err := SavePairing(info, pairing, *alias, ""); err != nil {
		env.Errorf("pair: the host issued a token but it could not be saved: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		printJSON(env, map[string]any{
			"target":        info.Target,
			"alias":         *alias,
			"token_id":      pairing.TokenID,
			"host_identity": auth.Fingerprint(info.HostKey),
		})
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "paired with %s (host identity %s)\n", displayTarget(info.Target), auth.Fingerprint(info.HostKey))
	name := info.Target
	if *alias != "" {
		name = *alias
	}
	fmt.Fprintf(env.Stdout, "no password needed from now on: remohop %s <command>\n", name)
	return cli.ExitOK
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func runPing(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "ping", pingUsage)
	count := fs.Int("c", 4, "")
	interval := fs.Float64("i", 0.2, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 || *count < 1 || *count > 100000 || *interval < 0 || *interval > 3600 {
		env.Errorf("ping: usage: remohop <ID> ping [-c N] [-i SECONDS] [--json]")
		return cli.ExitUsage
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	// Sessions not made by Dial (tests, a future broker) have no
	// establishment record; ping still measures them.
	info := InfoOf(conn)
	target, path, authKind, transport := conn.RemoteID(), "", "", ""
	if info != nil {
		target, path, authKind, transport = info.Target, string(info.Path), string(info.Auth), info.Transport
	}
	local, remote := conn.LocalAddr().String(), conn.RemoteAddr().String()

	opts := hostd.PingOptions{Count: *count, Interval: time.Duration(*interval * float64(time.Second))}
	if opts.Interval == 0 {
		opts.Interval = time.Nanosecond
	}
	if !*asJSON {
		via := "P2P path"
		if path != "" {
			via = path + " path"
		}
		how := "peer-to-peer, no relay"
		if path == string(PathRelayed) {
			how = "RELAYED through the rendezvous server, end-to-end encrypted"
		}
		if path == string(PathTailnet) {
			how = "through the tailnet both machines are on; the session is end-to-end encrypted by RemoHop as on any path"
		}
		if transport == TransportWebSocket {
			via += " (websocket)"
			how = "RELAYED through the rendezvous server's websocket bridge, end-to-end encrypted"
		}
		fmt.Fprintf(env.Stdout, "PING %s over %s %s -> %s (%s)\n", cli.Sanitize(displayTarget(target)), via, local, remote, how)
		opts.OnReply = func(seq int, rtt time.Duration) {
			fmt.Fprintf(env.Stdout, "reply from %s: seq=%d time=%.2f ms\n", remote, seq, ms(rtt))
		}
	}
	res, err := hostd.Ping(ctx, conn, opts)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return cli.ExitError
		}
		env.Errorf("ping: %v", err)
		return cli.ExitError
	}

	lo, hi, sum := res.RTTs[0], res.RTTs[0], time.Duration(0)
	for _, r := range res.RTTs {
		lo, hi, sum = min(lo, r), max(hi, r), sum+r
	}
	avg := sum / time.Duration(len(res.RTTs))
	if *asJSON {
		rtts := make([]float64, len(res.RTTs))
		for i, r := range res.RTTs {
			rtts[i] = ms(r)
		}
		printJSON(env, map[string]any{
			"target":       target,
			"path":         path,
			"p2p":          path != string(PathRelayed),
			"relayed":      path == string(PathRelayed),
			"transport":    transport,
			"local":        local,
			"remote":       remote,
			"observed":     res.Info.Observed,
			"auth":         authKind,
			"host_version": res.Info.Version,
			"host_name":    res.Info.Name,
			"rtt_ms":       rtts,
			"min_ms":       ms(lo),
			"avg_ms":       ms(avg),
			"max_ms":       ms(hi),
		})
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stdout, "%d probes, rtt min/avg/max = %.2f/%.2f/%.2f ms\n", len(res.RTTs), ms(lo), ms(avg), ms(hi))
	if res.Info.Name != "" {
		fmt.Fprintf(env.Stdout, "the host calls itself %q\n", cli.Sanitize(res.Info.Name))
	}
	if res.Info.Observed != "" {
		fmt.Fprintf(env.Stdout, "the host sees this machine as %s\n", cli.Sanitize(res.Info.Observed))
	}
	return cli.ExitOK
}

func runPeers(_ context.Context, env *cli.Env, args []string) int {
	store, err := OpenStore("")
	if err != nil {
		env.Errorf("peers: %v", err)
		return cli.ExitError
	}
	if len(args) > 0 {
		switch args[0] {
		case "forget":
			return runPeersForget(env, store, args[1:])
		case "alias":
			return runPeersAlias(env, store, args[1:])
		case "trust":
			return runPeersTrust(env, store, args[1:])
		case "redeem":
			return runPeersRedeem(env, store, args[1:])
		}
	}
	fs := newFlags(env, "peers", peersUsage)
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("peers: unknown subcommand %q (alias, forget)", fs.Arg(0))
		return cli.ExitUsage
	}
	peers, err := store.List()
	if err != nil {
		env.Errorf("peers: %v", err)
		return cli.ExitError
	}
	fingerprint := func(p Peer) string {
		if key, err := auth.DecodeKey(p.HostKey); err == nil {
			return auth.Fingerprint(key)
		}
		return ""
	}
	if *asJSON {
		// Never the tokens: this output is meant to be passed around.
		type row struct {
			Target       string     `json:"target"`
			Alias        string     `json:"alias,omitempty"`
			Paired       bool       `json:"paired"`
			Grant        bool       `json:"grant"`
			PairedAt     *time.Time `json:"paired_at,omitempty"`
			HostIdentity string     `json:"host_identity,omitempty"`
			FirstSeen    time.Time  `json:"first_seen"`
		}
		rows := make([]row, 0, len(peers))
		for _, p := range peers {
			rows = append(rows, row{p.Target, p.Alias, p.Paired(), p.Paired() && p.Grant, p.PairedAt, fingerprint(p), p.FirstSeen})
		}
		printJSON(env, rows)
		return cli.ExitOK
	}
	if len(peers) == 0 {
		fmt.Fprintln(env.Stdout, "no saved hosts")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tALIAS\tPAIRED\tGRANT\tHOST IDENTITY")
	for _, p := range peers {
		paired := "no"
		if p.Paired() {
			paired = "yes"
		} else if p.KeyLogin {
			paired = "key"
		}
		alias := p.Alias
		if alias == "" {
			alias = "-"
		}
		grant := "-"
		if p.Paired() && p.Grant {
			grant = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", cli.Sanitize(displayTarget(p.Target)), cli.Sanitize(alias), paired, grant, fingerprint(p))
	}
	tw.Flush()
	return cli.ExitOK
}

func runPeersForget(env *cli.Env, store *Store, refs []string) int {
	if len(refs) == 0 {
		env.Errorf("usage: remohop peers forget TARGET|ALIAS...")
		return cli.ExitUsage
	}
	code := cli.ExitOK
	for _, ref := range refs {
		n, err := store.Forget(cli.NormalizeTarget(ref))
		switch {
		case err != nil:
			env.Errorf("peers: %v", err)
			return cli.ExitError
		case n == 0:
			env.Errorf("peers: no saved host %q", ref)
			code = cli.ExitError
		default:
			fmt.Fprintf(env.Stdout, "forgot %s\n", ref)
		}
	}
	return code
}

func runKey(_ context.Context, env *cli.Env, args []string) int {
	if len(args) == 0 {
		env.Errorf("usage: remohop key show|export|gen|import|clear (see `remohop help key`)")
		return cli.ExitUsage
	}
	sub, args := args[0], args[1:]
	fs := newFlags(env, "key "+sub, keyUsage)
	label := fs.String("label", "", "")
	private := fs.String("private", "", "")
	yes := fs.Bool("yes", false, "")
	// Flags may come after the FILE argument of import.
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("key: %v", err)
		return cli.ExitError
	}
	needYes := func(what string) bool {
		if *yes {
			return true
		}
		env.Errorf("key %s: this discards this machine's identity (%s) for good: every pairing and authorised key that hosts "+
			"hold for it stops working, clients that pinned it refuse it, and a host here gets a new ID. Add --yes if that is what you want.",
			what, currentFingerprint(dir))
		return false
	}
	switch sub {
	case "show":
		if len(rest) != 0 || *private != "" || *label != "" {
			return keyUsageError(env)
		}
		id, err := auth.LoadIdentity(dir)
		if err != nil {
			env.Errorf("key: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, auth.Fingerprint(id.Public()))
		return cli.ExitOK
	case "export":
		if len(rest) != 0 {
			return keyUsageError(env)
		}
		id, err := auth.LoadIdentity(dir)
		if err != nil {
			env.Errorf("key: %v", err)
			return cli.ExitError
		}
		if *private != "" {
			blob, err := id.MarshalPrivate()
			if err == nil {
				err = writeNew(*private, blob)
			}
			if err != nil {
				env.Errorf("key export: %v", err)
				return cli.ExitError
			}
			env.Errorf("wrote the PRIVATE key of identity %s to %s; keep it secret, and delete it once imported", auth.Fingerprint(id.Public()), *private)
			return cli.ExitOK
		}
		if *label == "" {
			*label, _ = os.Hostname()
		}
		fmt.Fprintln(env.Stdout, auth.FormatKeyLine(id.Public(), *label))
		return cli.ExitOK
	case "gen", "clear":
		if len(rest) != 0 || *label != "" || *private != "" {
			return keyUsageError(env)
		}
		if !needYes(sub) {
			return cli.ExitUsage
		}
		var id *auth.Identity
		if sub == "gen" {
			if id, err = auth.NewIdentity(); err != nil {
				env.Errorf("key gen: %v", err)
				return cli.ExitError
			}
		}
		if err := auth.ReplaceIdentity(dir, id); err != nil {
			env.Errorf("key %s: %v", sub, err)
			return cli.ExitError
		}
		if id != nil {
			fmt.Fprintf(env.Stdout, "new identity %s\n", auth.Fingerprint(id.Public()))
		} else {
			fmt.Fprintln(env.Stdout, "identity removed; a new one is made when next needed")
		}
		return cli.ExitOK
	case "import":
		if len(rest) != 1 || *label != "" || *private != "" {
			return keyUsageError(env)
		}
		var blob []byte
		if rest[0] == "-" {
			blob, err = io.ReadAll(io.LimitReader(env.Stdin, 1<<16))
		} else {
			blob, err = os.ReadFile(rest[0])
		}
		if err != nil {
			env.Errorf("key import: %v", err)
			return cli.ExitError
		}
		id, err := auth.ParseIdentity(blob)
		if err != nil {
			env.Errorf("key import: %v (expected a file written by `remohop key export --private`)", err)
			return cli.ExitUsage
		}
		if !needYes("import") {
			return cli.ExitUsage
		}
		if err := auth.ReplaceIdentity(dir, id); err != nil {
			env.Errorf("key import: %v", err)
			return cli.ExitError
		}
		fmt.Fprintf(env.Stdout, "imported identity %s\n", auth.Fingerprint(id.Public()))
		return cli.ExitOK
	}
	return keyUsageError(env)
}

func keyUsageError(env *cli.Env) int {
	env.Errorf("usage: remohop key show | export [--label TEXT] | export --private FILE | gen --yes | import FILE --yes | clear --yes")
	return cli.ExitUsage
}

// currentFingerprint names the identity stored in dir without creating one.
func currentFingerprint(dir string) string {
	blob, err := config.ReadFile(filepath.Join(dir, auth.IdentityFile))
	if err != nil {
		return "none yet"
	}
	id, err := auth.ParseIdentity(blob)
	if err != nil {
		return "unreadable"
	}
	return auth.Fingerprint(id.Public())
}

// writeNew writes a secret to a file that must not exist yet.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func runPeersTrust(env *cli.Env, store *Store, args []string) int {
	if len(args) < 2 {
		env.Errorf("usage: remohop peers trust TARGET \"HOST-KEY-LINE\"")
		return cli.ExitUsage
	}
	target := cli.NormalizeTarget(args[0])
	if !cli.IsID(target) {
		if p, ok, _ := store.ByAlias(target); ok {
			target = p.Target
		} else if !strings.Contains(target, ":") {
			env.Errorf("peers: %q is not a 9-digit ID, a HOST:PORT or a saved alias", args[0])
			return cli.ExitUsage
		}
	}
	key, _, err := auth.ParseKeyLine(strings.Join(args[1:], " "))
	if err != nil {
		env.Errorf("peers: %v", err)
		return cli.ExitUsage
	}
	encoded := auth.EncodeKey(key)
	err = store.Update(target, func(p *Peer) error {
		if p.HostKey != "" && p.HostKey != encoded {
			return fmt.Errorf("%s is already known with a different key (%s); run `remohop peers forget %s` first if the host really changed",
				displayTarget(target), fingerprintOf(p.HostKey), target)
		}
		p.HostKey, p.KeyLogin = encoded, true
		return nil
	})
	if err != nil {
		env.Errorf("peers: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "%s is trusted with identity %s; this machine logs in to it with its key\n", displayTarget(target), auth.Fingerprint(key))
	return cli.ExitOK
}

func fingerprintOf(encoded string) string {
	if key, err := auth.DecodeKey(encoded); err == nil {
		return auth.Fingerprint(key)
	}
	return "?"
}

func runPeersAlias(env *cli.Env, store *Store, args []string) int {
	if len(args) != 2 {
		env.Errorf("usage: remohop peers alias TARGET NAME")
		return cli.ExitUsage
	}
	target := cli.NormalizeTarget(args[0])
	if !cli.IsID(target) {
		if p, ok, _ := store.ByAlias(target); ok {
			target = p.Target
		} else if !strings.Contains(target, ":") {
			env.Errorf("peers: %q is not a 9-digit ID, a HOST:PORT or a saved alias", args[0])
			return cli.ExitUsage
		}
	}
	if err := store.SetAlias(target, args[1]); err != nil {
		env.Errorf("peers: %v", err)
		return cli.ExitUsage
	}
	fmt.Fprintf(env.Stdout, "%s is now also %s\n", displayTarget(target), args[1])
	return cli.ExitOK
}

// featureAlias maps the names people naturally type to the feature names
// of `remohop config feature` ("files" is "file").
func featureAlias(name string) string {
	switch strings.ToLower(name) {
	case "files", "fs":
		return string(perm.File)
	case "clipboard":
		return string(perm.Clip)
	case "screen", "desktop":
		return string(perm.Screen)
	case "terminal":
		return string(perm.Shell)
	}
	return name
}

func runConfig(_ context.Context, env *cli.Env, args []string) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	known := func(key string) bool {
		if _, ok := config.LookupSetting(key); ok {
			return true
		}
		keys := make([]string, 0)
		for _, s := range config.Settings() {
			keys = append(keys, s.Key)
		}
		sort.Strings(keys)
		env.Errorf("config: unknown key %q (known keys: %s)", key, strings.Join(keys, ", "))
		return false
	}
	// said confirms a change, so that a typo in a value is seen at once.
	said := func(key, value string) {
		s, _ := config.LookupSetting(key)
		switch {
		case value == "":
			fmt.Fprintf(env.Stdout, "%s: unset\n", key)
		case s.Secret:
			fmt.Fprintf(env.Stdout, "%s: set\n", key)
		default:
			fmt.Fprintf(env.Stdout, "%s: %s\n", key, value)
		}
	}
	onOff := func(key string, args []string, usage string) int {
		if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
			env.Errorf("usage: remohop config %s on|off", usage)
			return cli.ExitUsage
		}
		if err := config.Set(key, args[0]); err != nil {
			env.Errorf("config: %v", err)
			return cli.ExitError
		}
		said(key, args[0])
		return cli.ExitOK
	}
	switch sub {
	case "feature":
		if len(args) > 0 {
			args[0] = featureAlias(args[0])
		}
		if len(args) < 1 || !perm.Valid(perm.Feature(args[0])) {
			var names []string
			for _, f := range perm.Features() {
				names = append(names, string(f))
			}
			env.Errorf("usage: remohop config feature NAME on|off  (NAME: %s)", strings.Join(names, ", "))
			return cli.ExitUsage
		}
		return onOff(hostd.FeatureSetting(perm.Feature(args[0])), args[1:], "feature "+args[0])
	case hostd.SettingAsk, hostd.SettingAskPaired, hostd.SettingOTP, signal.SettingRelay, portmap.Setting, lan.Setting, SettingOutcomes, account.SettingTrust:
		return onOff(sub, args, sub)
	case "":
		fs := newFlags(env, "config", configUsage)
		asJSON := fs.Bool("json", false, "")
		if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
			return cli.ExitUsage
		}
		values, err := config.All()
		if err != nil {
			env.Errorf("config: %v", err)
			return cli.ExitError
		}
		if *asJSON {
			printJSON(env, values)
			return cli.ExitOK
		}
		tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
		for _, s := range config.Settings() {
			v := values[s.Key]
			switch {
			case v == "" && s.Default != "":
				v = s.Default
			case v == "":
				v = "(not set)"
			case s.Secret:
				v = "(set)"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", s.Key, v, s.Help)
		}
		tw.Flush()
		return cli.ExitOK
	case "get":
		if len(args) != 1 {
			env.Errorf("usage: remohop config get KEY")
			return cli.ExitUsage
		}
		if !known(args[0]) {
			return cli.ExitUsage
		}
		v, err := config.Get(args[0])
		if err != nil {
			env.Errorf("config: %v", err)
			return cli.ExitError
		}
		if v == "" {
			// Like `git config`: nothing printed, non-zero status.
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, v)
		return cli.ExitOK
	case "set", "unset":
		value := ""
		if sub == "set" {
			if len(args) != 2 || args[1] == "" {
				env.Errorf("usage: remohop config set KEY VALUE")
				return cli.ExitUsage
			}
			value = args[1]
		} else if len(args) != 1 {
			env.Errorf("usage: remohop config unset KEY")
			return cli.ExitUsage
		}
		if !known(args[0]) {
			return cli.ExitUsage
		}
		if err := config.Set(args[0], value); err != nil {
			env.Errorf("config: %v", err)
			return cli.ExitUsage
		}
		said(args[0], value)
		return cli.ExitOK
	}
	// `remohop config KEY VALUE` is shorthand for `set` with any declared key
	// (vpn-exit on, name my-laptop, term-keep 2h, ...).
	if _, ok := config.LookupSetting(sub); ok && len(args) == 1 && args[0] != "" {
		if err := config.Set(sub, args[0]); err != nil {
			env.Errorf("config: %v", err)
			return cli.ExitUsage
		}
		said(sub, args[0])
		return cli.ExitOK
	}
	env.Errorf("config: unknown subcommand %q (get, set, unset, feature, or a setting's name)", sub)
	return cli.ExitUsage
}
