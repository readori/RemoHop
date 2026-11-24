package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/dial"
)

type peersIn struct{}

type peerInfo struct {
	Target    string `json:"target" jsonschema:"9-digit ID or host:port; usable as the target argument."`
	Alias     string `json:"alias,omitempty" jsonschema:"Saved name; also usable as the target argument."`
	Paired    bool   `json:"paired" jsonschema:"True if a pairing is saved, so tools can connect without a password."`
	FirstSeen string `json:"first_seen,omitempty"`
	Connected bool   `json:"connected" jsonschema:"True if this server currently holds an open session to it."`
	Path      string `json:"path,omitempty" jsonschema:"For a connected machine, how the session travels: lan, direct or punched (peer to peer), or relayed (through the rendezvous server; slower)."`
}

type peersOut struct {
	Peers         []peerInfo `json:"peers"`
	DefaultTarget string     `json:"default_target,omitempty" jsonschema:"Target used when a tool call omits target."`
}

func (s *Server) registerPeers() {
	addTool(s, "remohop_peers", "List known remote machines",
		"List the remote machines this computer has connected to before (saved IDs, aliases and whether each is paired, i.e. reachable without a password). "+
			"Use the target or alias from here as the target argument of other tools. Reads local state only; it does not contact anyone and never reveals credentials. "+
			"A machine that is not listed can still be targeted by its 9-digit ID if the person has paired or can give a password.",
		localReadOnly(), s.peers)
}

func (s *Server) peers(context.Context, peersIn) (*reply[peersOut], error) {
	store, err := dial.OpenStore("")
	if err != nil {
		return nil, fmt.Errorf("opening saved peers: %w", err)
	}
	list, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("reading saved peers: %w", err)
	}
	out := peersOut{Peers: []peerInfo{}, DefaultTarget: s.opts.DefaultTarget}
	var b strings.Builder
	open := s.pool.sessions()
	for _, p := range list {
		pi := peerInfo{Target: p.Target, Alias: p.Alias, Paired: p.Paired()}
		for _, name := range []string{p.Target, p.Alias} {
			if path, ok := open[name]; ok && name != "" {
				pi.Connected, pi.Path = true, path
			}
		}
		if !p.FirstSeen.IsZero() {
			pi.FirstSeen = p.FirstSeen.UTC().Format(time.RFC3339)
		}
		cleanStrings(&pi)
		out.Peers = append(out.Peers, pi)
		alias, paired := pi.Alias, "not paired"
		if alias == "" {
			alias = "-"
		}
		if pi.Paired {
			paired = "paired"
		}
		fmt.Fprintf(&b, "%s  alias %s  %s", pi.Target, alias, paired)
		if pi.Connected {
			b.WriteString("  connected")
			if pi.Path != "" {
				b.WriteString(" (" + pi.Path + ")")
			}
		}
		b.WriteByte('\n')
	}
	if len(out.Peers) == 0 {
		b.WriteString("no saved peers; ask the person for the machine's 9-digit ID and to run `remohop <ID> pair`\n")
	}
	if out.DefaultTarget != "" {
		fmt.Fprintf(&b, "default target: %s\n", out.DefaultTarget)
	}
	return textReply(out, b.String()), nil
}
