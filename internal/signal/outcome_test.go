package signal

import (
	"errors"
	"testing"
)

func TestOutcomeValid(t *testing.T) {
	for _, tc := range []struct {
		o  Outcome
		ok bool
	}{
		{Outcome{Path: PathPunched, MS: 840, NAT: "cone", PeerNAT: "symmetric"}, true},
		{Outcome{Path: PathFailed, MS: 6000}, true},
		{Outcome{Path: PathUpgraded, MS: 25000, NAT: "unknown"}, true},
		{Outcome{Path: "teleported", MS: 1}, false},
		{Outcome{Path: PathRelayed, MS: -1}, false},
		{Outcome{Path: PathRelayed, MS: MaxOutcomeMS + 1}, false},
		{Outcome{Path: PathLAN, NAT: "full-cone"}, false},
	} {
		if got := tc.o.Valid(); got != tc.ok {
			t.Errorf("%+v: Valid() = %v, want %v", tc.o, got, tc.ok)
		}
	}
}

// A server takes outcome reports, a bounded number per connection, and
// refuses malformed ones without the client sending them.
func TestReportOutcome(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	c := tn.dial(tn.socket(tn.net.Host("192.0.2.9")), Options{})
	if err := c.ReportOutcome(ctx, Outcome{Path: PathPunched, MS: 900, NAT: "cone", PeerNAT: "cone"}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportOutcome(ctx, Outcome{Path: "nowhere"}); err == nil || errors.Is(err, ErrNoOutcomes) {
		t.Fatalf("malformed outcome: %v", err)
	}
	var err error
	for range maxOutcomesPerConn {
		if err = c.ReportOutcome(ctx, Outcome{Path: PathRelayed, MS: 3000}); err != nil {
			break
		}
	}
	var se *Error
	if !errors.As(err, &se) || se.Code != CodeRateLimited {
		t.Fatalf("report %d on one connection: %v, want rate limited", maxOutcomesPerConn+1, err)
	}
}
