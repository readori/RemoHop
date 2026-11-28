package dial

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/lan"
)

const lanUsage = `Usage: remohop lan [--json] [--wait DURATION]

List the RemoHop hosts on this network, found without a rendezvous server:
their name, address, ID (when they have one), the fingerprint of their
identity key, and whether this machine has saved them. Connect with the
address shown: remohop ADDRESS <command>.

A host is listed while it runs ` + "`remohop host`" + `, the desktop app with incoming
connections on, or quick support, unless its owner turned that off
(` + "`remohop config lan-discovery off`" + `). It is found with multicast DNS on
UDP port 5353, which firewalls and some Wi-Fi networks block.

What a host says about itself is only a hint: connecting checks its key as
always. When a host claims the ID of a host saved here with another key,
the listing warns: it may be an impostor.

  --json      machine-readable output
  --wait DUR  how long to listen for answers (default 2s, at most 10s)

Examples:
  remohop lan
  remohop 192.168.1.20:21118 shell
`

func init() {
	cli.Register(&cli.Command{Name: "lan", Summary: "find hosts on this network, with no server", Usage: lanUsage, Run: runLAN})
}

// LANHost is a host found on the local network, with what this machine
// knows about it.
type LANHost struct {
	lan.Host
	// Saved lists the hosts saved here (connected to or paired with) that
	// have its identity key: how this machine already knows it.
	Saved []LANSaved `json:"saved,omitempty"`
	// Conflict is a saved target (its ID or this address) whose pinned
	// key is not the one the host announced. For its ID that means the
	// answer may come from an impostor; for the address, that another
	// machine had it before. Connecting there is refused until the saved
	// host is forgotten.
	Conflict string `json:"conflict,omitempty"`
	// Self is this machine's own host.
	Self bool `json:"self,omitempty"`
}

// LANSaved is a saved host with the identity key a LAN host announced.
type LANSaved struct {
	Target string `json:"target"`
	Alias  string `json:"alias,omitempty"`
	Paired bool   `json:"paired"`
}

// MaxLANWait bounds how long DiscoverLAN listens.
const MaxLANWait = 10 * time.Second

// DiscoverLAN asks the local network for RemoHop hosts (lan.Browse) for
// wait (lan.DefaultWait if 0) and tells, from the saved hosts in dir (the
// config directory if empty), which of them this machine knows.
func DiscoverLAN(ctx context.Context, dir string, wait time.Duration) ([]LANHost, error) {
	hosts, err := lan.Browse(ctx, min(wait, MaxLANWait))
	if err != nil && len(hosts) == 0 {
		return nil, err
	}
	if dir == "" {
		if dir, err = config.Dir(); err != nil {
			return nil, err
		}
	}
	store, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	peers, err := store.List()
	if err != nil {
		return nil, err
	}
	return annotateLAN(hosts, peers, ownFingerprint(dir)), nil
}

// annotateLAN adds what the saved peers say to each host; self is this
// machine's own key fingerprint ("" if it has none yet).
func annotateLAN(hosts []lan.Host, peers []Peer, self string) []LANHost {
	keys := make([]string, len(peers))
	for i, p := range peers {
		if k, err := auth.DecodeKey(p.HostKey); err == nil {
			keys[i] = auth.Fingerprint(k)
		}
	}
	out := make([]LANHost, 0, len(hosts))
	for _, h := range hosts {
		lh := LANHost{Host: h, Self: self != "" && h.Fingerprint == self}
		for i, p := range peers {
			switch {
			case keys[i] == h.Fingerprint:
				lh.Saved = append(lh.Saved, LANSaved{Target: p.Target, Alias: p.Alias, Paired: p.Paired()})
			case keys[i] != "" && lh.Conflict == "" && (p.Target == h.ID || p.Target == h.Addr.String()):
				lh.Conflict = p.Target
			}
		}
		out = append(out, lh)
	}
	return out
}

// ownFingerprint is the fingerprint of this machine's identity key in dir,
// which is also its host's; "" if there is none yet (it is not made here).
func ownFingerprint(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, auth.IdentityFile)); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return ""
	}
	return auth.Fingerprint(id.Public())
}

func runLAN(ctx context.Context, env *cli.Env, args []string) int {
	fs := newFlags(env, "lan", lanUsage)
	asJSON := fs.Bool("json", false, "")
	wait := fs.Duration("wait", lan.DefaultWait, "")
	if err := fs.Parse(args); err != nil {
		return cli.FlagExit(err)
	}
	if fs.NArg() != 0 || *wait <= 0 || *wait > MaxLANWait {
		env.Errorf("usage: remohop lan [--json] [--wait DURATION up to %v]", MaxLANWait)
		return cli.ExitUsage
	}
	hosts, err := DiscoverLAN(ctx, "", *wait)
	if err != nil {
		env.Errorf("lan: %v", err)
		return cli.ExitError
	}
	if *asJSON {
		printJSON(env, hosts)
		return cli.ExitOK
	}
	if len(hosts) == 0 {
		fmt.Fprintln(env.Stdout, "no RemoHop hosts answered on this network")
		return cli.ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tADDRESS\tID\tHOST IDENTITY\tSAVED HERE")
	for _, h := range hosts {
		name := cli.Sanitize(h.Name)
		if h.Self {
			name += " (this computer)"
		}
		id := "-"
		if h.ID != "" {
			id = displayTarget(h.ID)
		}
		var saved []string
		for _, s := range h.Saved {
			t := displayTarget(s.Target)
			if s.Alias != "" {
				t = s.Alias + " = " + t
			}
			if s.Paired {
				t += " (paired)"
			}
			saved = append(saved, cli.Sanitize(t))
		}
		if len(saved) == 0 {
			saved = []string{"-"}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", name, h.Addr, id, h.Fingerprint, strings.Join(saved, ", "))
	}
	tw.Flush()
	for _, h := range hosts {
		if h.Conflict == "" {
			continue
		}
		what := "it may be an impostor"
		if h.Conflict == h.Addr.String() {
			what = "another machine may have had that address before"
		}
		env.Errorf("warning: %s at %s announces key %s, but the key saved here for %s is different: %s. Connecting to %s is refused until you check it and run `remohop peers forget %s`.",
			h.Name, h.Addr, h.Fingerprint, displayTarget(h.Conflict), what, displayTarget(h.Conflict), h.Conflict)
	}
	fmt.Fprintf(env.Stdout, "\nConnect with the address, e.g. remohop %s shell\n", hosts[0].Addr)
	return cli.ExitOK
}
