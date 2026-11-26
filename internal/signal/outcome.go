package signal

import (
	"context"
	"errors"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/nat"
)

// # Connection outcomes
//
// After a connection attempt, a client may tell the rendezvous server how
// it ended: which kind of path the session runs on, or that none was
// found, and how long it took. The server keeps only aggregate counts
// (how often each path wins, how long it takes, per pair of NAT types), so
// that whoever runs it can see how well hole punching works. A report
// names no peer, no ID and no address; it travels on the client's existing
// connection to the server, which knows the client's address anyway.
//
// An "outcome" request carries one Outcome; the answer is a plain ok. A
// server that predates outcomes answers bad_request ("unknown request"),
// and ReportOutcome returns ErrNoOutcomes: the client simply stops
// reporting to that server.

// Outcome paths: how the session's connection reached the host.
const (
	PathLAN       = "lan"       // a LAN or other private address
	PathDirect    = "direct"    // the host's public address, nothing to punch
	PathPunched   = "punched"   // a hole-punched public address
	PathMapped    = "mapped"    // a port the host's router forwards (internal/portmap)
	PathPredicted = "predicted" // a port predicted for a symmetric NAT (nat.Prediction)
	PathRelayed   = "relayed"   // the server's relay
	PathBridged   = "bridged"   // the relay, reached through the websocket bridge
	PathFailed    = "failed"    // no path: the attempt gave up
	// PathUpgraded reports a relayed session that moved to a direct path
	// later on (internal/dial); MS is how long after the session started.
	PathUpgraded = "upgraded"
)

// MaxOutcomeMS bounds Outcome.MS; longer times are not plausible for one
// attempt, or for an upgrade worth counting separately.
const MaxOutcomeMS = int64(24 * time.Hour / time.Millisecond)

// maxOutcomesPerConn bounds the reports one connection to the server may
// make: a client makes one or two per connection attempt.
const maxOutcomesPerConn = 256

// Outcome is one connection attempt's result, as a client reports it.
type Outcome struct {
	Path string `json:"path"`
	// MS is the time from the client's connect request to a working
	// session (or to giving up), in milliseconds; for PathUpgraded, the
	// time from the session's start to the move.
	MS int64 `json:"ms"`
	// NAT and PeerNAT are the NAT types of the client and the host as
	// they were exchanged in the introduction (nat.Type values).
	NAT     string `json:"nat,omitempty"`
	PeerNAT string `json:"peer_nat,omitempty"`
}

// ErrNoOutcomes is returned by ReportOutcome when the server does not take
// outcome reports.
var ErrNoOutcomes = errors.New("signal: the server does not take connection outcomes")

// Valid reports whether o is well formed: a known path, a plausible time,
// and known NAT types (or none).
func (o Outcome) Valid() bool {
	switch o.Path {
	case PathLAN, PathDirect, PathPunched, PathMapped, PathPredicted, PathRelayed, PathBridged, PathFailed, PathUpgraded:
	default:
		return false
	}
	if o.MS < 0 || o.MS > MaxOutcomeMS {
		return false
	}
	known := func(t string) bool {
		switch nat.Type(t) {
		case "", nat.TypeUnknown, nat.TypeNone, nat.TypeCone, nat.TypeSymmetric:
			return true
		}
		return false
	}
	return known(o.NAT) && known(o.PeerNAT)
}

// ReportOutcome tells the server how a connection attempt ended. It is
// best effort: callers should not wait on it for long (bound ctx) and may
// ignore its error. ErrNoOutcomes means the server does not take reports.
func (c *Client) ReportOutcome(ctx context.Context, o Outcome) error {
	if !o.Valid() {
		return errors.New("signal: malformed outcome")
	}
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, request{T: "outcome", V: protoVersion, Outcome: &o}); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	var se *Error
	if errors.As(err, &se) && se.Code == CodeBadRequest {
		return ErrNoOutcomes
	}
	return err
}

// handleOutcome takes one outcome report and hands it to the server's
// aggregate (recordOutcome).
func (s *Server) handleOutcome(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	if req.Outcome == nil || !req.Outcome.Valid() {
		fail(CodeBadRequest, "malformed outcome", 0)
		return
	}
	if c.outcomes.Add(1) > maxOutcomesPerConn {
		fail(CodeRateLimited, "too many outcome reports on this connection", time.Minute)
		return
	}
	s.recordOutcome(*req.Outcome)
	writeMsg(st, response{OK: true})
}

// recordOutcome adds a valid report to the server's aggregate counts. It
// must not log or keep anything per report: only counts.
//
// The aggregate itself is not built yet; until it is, reports are taken
// and dropped, so that clients can start sending them.
func (s *Server) recordOutcome(Outcome) {}
