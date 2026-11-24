package dial

import (
	"encoding/json"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// A relay hint remembers, for a short while, that hole punching to a host
// failed and the relay was needed. Each CLI command is a fresh dial, and a
// punch that cannot work costs its full time (8 s, plus a 4 s retry)
// before the relay is tried; with a hint, the next commands to that host go
// to the relay at once.
//
// A hint applies only while nothing that decided the outcome has changed:
// the same host, seen from the same public address of ours and at the same
// public address of its own (both as the rendezvous server reports them).
// Moving either machine to another network, or the hint expiring, brings
// punching back. A hint never makes a connection less secure: a relayed
// session is the same end-to-end session.
const (
	relayHintsFile = "relay-hints.json"
	relayHintTTL   = 10 * time.Minute
	maxRelayHints  = 64
)

type relayHint struct {
	Ours    netip.Addr `json:"ours"`
	Theirs  netip.Addr `json:"theirs"`
	Expires time.Time  `json:"expires"`
}

var relayHintsMu sync.Mutex // within one process; across processes the last write wins

func readRelayHints(dir string) map[string]relayHint {
	hints := map[string]relayHint{}
	b, err := config.ReadFile(filepath.Join(dir, relayHintsFile))
	if err == nil {
		json.Unmarshal(b, &hints)
	}
	now := time.Now()
	for k, h := range hints {
		if !now.Before(h.Expires) || h.Expires.Sub(now) > relayHintTTL {
			delete(hints, k)
		}
	}
	return hints
}

// relayHinted reports whether target needed the relay recently, from the
// same pair of public addresses.
func relayHinted(dir, target string, ours, theirs netip.AddrPort) bool {
	if !ours.IsValid() || !theirs.IsValid() {
		return false
	}
	relayHintsMu.Lock()
	defer relayHintsMu.Unlock()
	h, ok := readRelayHints(dir)[target]
	return ok && h.Ours == ours.Addr() && h.Theirs == theirs.Addr()
}

// setRelayHint records (or, with ok false, forgets) that target needed the
// relay. Failures to write are ignored: a hint is only an optimisation.
func setRelayHint(dir, target string, ours, theirs netip.AddrPort, ok bool) {
	relayHintsMu.Lock()
	defer relayHintsMu.Unlock()
	hints := readRelayHints(dir)
	_, had := hints[target]
	switch {
	case ok && ours.IsValid() && theirs.IsValid():
		hints[target] = relayHint{Ours: ours.Addr(), Theirs: theirs.Addr(), Expires: time.Now().Add(relayHintTTL)}
		for k := range hints {
			if len(hints) <= maxRelayHints {
				break
			}
			if k != target {
				delete(hints, k)
			}
		}
	case !ok && had:
		delete(hints, target)
	default:
		return
	}
	b, err := json.Marshal(hints)
	if err != nil {
		return
	}
	config.WriteFileAtomic(filepath.Join(dir, relayHintsFile), b, 0o600)
}
