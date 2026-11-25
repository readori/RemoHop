package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// ServiceLimits tells a client what its session may do. Every session may
// ask; it is how an agent learns its grant's limits from the host that
// enforces them.
const ServiceLimits = "limits"

func init() {
	perm.Register(ServiceLimits, perm.Always)
	peer.Handle(ServiceLimits, serveLimits)
}

// Limits describes what a session may do on its host.
type Limits struct {
	// Grant is the grant the session stands on; empty means full access
	// within the host's feature switches.
	Grant string `json:"grant,omitempty"`
	// Expires is when the grant ends (and the session with it).
	Expires *time.Time `json:"expires,omitempty"`
	// Allowed and Denied list capabilities in the allow-list vocabulary
	// (shell:run-only, files:read under ..., screenshot, ...).
	Allowed []string `json:"allowed"`
	Denied  []string `json:"denied,omitempty"`
	// Shell is "none", "run-only" or "full".
	Shell string `json:"shell"`
	// Commands, if any, are the only commands that may run.
	Commands []string `json:"commands,omitempty"`
	// ReadRoots and WriteRoots confine file access; ReadAll and WriteAll
	// mean anywhere the host's user can.
	ReadAll    bool     `json:"read_all,omitempty"`
	WriteAll   bool     `json:"write_all,omitempty"`
	ReadRoots  []string `json:"read_roots,omitempty"`
	WriteRoots []string `json:"write_roots,omitempty"`
	// Approve is "none", "first" or "each": whether the host's owner is
	// asked before commands and file changes.
	Approve string `json:"approve,omitempty"`
	// Recorded says command and terminal output is recorded on the host.
	Recorded    bool `json:"recorded,omitempty"`
	MaxSessions int  `json:"max_sessions,omitempty"`
	// FeaturesOff are the host's own feature switches that are off; they
	// apply on top of any grant.
	FeaturesOff []string `json:"features_off,omitempty"`
	Error       string   `json:"error,omitempty"`
}

func serveLimits(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
	sess := SessionOf(c)
	if sess == nil {
		return wire.WriteJSON(s, Limits{Error: "not a host session"})
	}
	return wire.WriteJSON(s, sess.limits())
}

func (s *Session) limits() Limits {
	var l Limits
	pol := s.host.policy()
	for _, f := range perm.Features() {
		if pol.off[f] || s.limited(f) {
			l.FeaturesOff = append(l.FeaturesOff, string(f))
		}
	}
	if s.grant == "" {
		l.Shell, l.ReadAll, l.WriteAll = "full", true, true
		l.Allowed = []string{"everything the host's user can do, except the features switched off"}
		if pol.off[perm.Shell] || s.limited(perm.Shell) {
			l.Shell = "none"
		}
		if pol.off[perm.File] || s.limited(perm.File) {
			l.ReadAll, l.WriteAll = false, false
		}
		return l
	}
	l.Grant = s.grant
	e, err := s.activeGrant()
	if err != nil {
		l.Error = err.Error()
		return l
	}
	r := e.rules
	exp := e.g.Expires
	l.Expires = &exp
	l.Allowed, l.Denied = r.Summary(), r.Denied()
	l.Shell = [...]string{"none", "run-only", "full"}[r.Shell]
	l.Commands = r.Commands
	l.ReadAll, l.WriteAll, l.ReadRoots, l.WriteRoots = r.ReadAll, r.WriteAll, r.ReadRoots, r.WriteRoots
	l.Approve, l.Recorded, l.MaxSessions = e.g.Approve, e.g.Record, e.g.MaxSessions
	if l.Approve == "" {
		l.Approve = grant.ApproveNone
	}
	return l
}

// GetLimits asks the host on c what this session may do.
func GetLimits(ctx context.Context, c *peer.Conn) (*Limits, error) {
	s, err := c.Open(ctx, ServiceLimits, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var l Limits
	if err := wire.ReadJSONLimit(s, &l, 64<<10); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("limits: %w", err)
	}
	if l.Error != "" {
		return &l, fmt.Errorf("%s", auth.CleanText(l.Error, 300))
	}
	return &l, nil
}

const limitsUsage = `Usage: remohop <ID> limits [--json]

Show what this machine may do on the host: everything (within the host's
feature switches) for a pairing, a password or an authorised key, or the
limits of the grant this machine redeemed (` + "`remohop peers redeem`" + `): allowed
services, file roots, commands, approval mode and expiry.
`

func init() {
	cli.Register(&cli.Command{Name: "limits", Summary: "show what this machine may do on a host (its grant's limits)", Usage: limitsUsage, Remote: true, Run: runLimits})
}

func runLimits(ctx context.Context, env *cli.Env, args []string) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		env.Errorf("usage: remohop <ID> limits [--json]")
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	l, err := GetLimits(ctx, conn)
	if l == nil {
		env.Errorf("limits: %v", err)
		return cli.ExitError
	}
	if asJSON {
		printJSON(env, l)
		return cli.ExitOK
	}
	w := env.Stdout
	if l.Grant == "" {
		fmt.Fprintln(w, "full access (no grant)")
	} else {
		fmt.Fprintf(w, "grant %s", cli.Sanitize(l.Grant))
		if l.Expires != nil {
			fmt.Fprintf(w, ", expires %s", l.Expires.Local().Format("2006-01-02 15:04"))
		}
		fmt.Fprintln(w)
	}
	if l.Error != "" {
		fmt.Fprintf(w, "  unusable: %s\n", cli.Sanitize(l.Error))
	}
	for _, a := range l.Allowed {
		fmt.Fprintf(w, "  allows   %s\n", cli.Sanitize(a))
	}
	if len(l.Denied) > 0 {
		fmt.Fprintf(w, "  denies   %s\n", cli.Sanitize(strings.Join(l.Denied, ", ")))
	}
	if l.Approve != "" && l.Approve != grant.ApproveNone {
		fmt.Fprintf(w, "  approval %s (the host's owner is asked before commands and file changes)\n", l.Approve)
	}
	if l.Recorded {
		fmt.Fprintln(w, "  output of commands and terminals is recorded on the host")
	}
	if len(l.FeaturesOff) > 0 {
		fmt.Fprintf(w, "  switched off on the host: %s\n", cli.Sanitize(strings.Join(l.FeaturesOff, ", ")))
	}
	if err != nil {
		return cli.ExitError
	}
	return cli.ExitOK
}
