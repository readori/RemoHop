package signal

import "sync/atomic"

// serverStats are running totals since the server started, for the web
// panel (panel.go). Each is one atomic add on a path that already does far
// more work, and none holds an address, an ID or a key: counts only.
type serverStats struct {
	c [numStats]atomic.Int64
}

type statKey int

const (
	// Activity.
	stRegistrations statKey = iota // successful registrations
	stNewIDs                       // of which allocated a new ID
	stLookups                      // connect requests (valid ID)
	stIntroductions                // connect requests answered with the host's candidates
	stPresence                     // presence requests
	stRelaySessions                // relayed sessions the host accepted
	stRelayBytes                   // bytes relayed
	stWSOpened                     // websocket bridges opened

	// Refusals of the rendezvous service.
	stRateRegister // registrations over the per-source rate
	stRateNewID    // new IDs over a per-source, per-network or global rate
	stRateConnect  // connect, presence and relay requests over the per-source or per-network rate
	stRateMiss     // requests for IDs not online, over the global budget
	stRateOffer    // offers to one host over its rate
	stBusy         // connections refused: MaxConns reached
	stPerSource    // connections refused: MaxConnsPerSource
	stPerNet       // connections refused: MaxConnsPerNet
	stIDsFull      // registrations refused: the ID table is full
	stExtLimited   // extension requests over their limits
	stWSRefused    // websocket bridges refused (their limits)

	// Relay refusals, by reason (relaypolicy.go).
	stRelayPerSource
	stRelayMode
	stRelayDeny
	stRelayBudget
	stRelayFull

	numStats
)

// statNames are the keys the panel's API and history use.
var statNames = [numStats]string{
	stRegistrations:  "registrations",
	stNewIDs:         "new_ids",
	stLookups:        "lookups",
	stIntroductions:  "introductions",
	stPresence:       "presence",
	stRelaySessions:  "relay_sessions",
	stRelayBytes:     "relay_bytes",
	stWSOpened:       "ws_opened",
	stRateRegister:   "rate_register",
	stRateNewID:      "rate_new_id",
	stRateConnect:    "rate_connect",
	stRateMiss:       "rate_miss",
	stRateOffer:      "rate_offer",
	stBusy:           "busy",
	stPerSource:      "per_source",
	stPerNet:         "per_net",
	stIDsFull:        "ids_full",
	stExtLimited:     "ext_limited",
	stWSRefused:      "ws_refused",
	stRelayPerSource: "relay_" + refusedPerSource,
	stRelayMode:      "relay_" + refusedMode,
	stRelayDeny:      "relay_" + refusedDeny,
	stRelayBudget:    "relay_" + refusedBudget,
	stRelayFull:      "relay_" + refusedFull,
}

func (s *serverStats) add(k statKey)           { s.c[k].Add(1) }
func (s *serverStats) addN(k statKey, n int64) { s.c[k].Add(n) }
func (s *serverStats) snapshot() (v [numStats]int64) {
	for i := range v {
		v[i] = s.c[i].Load()
	}
	return v
}

// relayRefusal maps a relay refusal reason to its counter.
func relayRefusal(reason string) statKey {
	switch reason {
	case refusedPerSource:
		return stRelayPerSource
	case refusedMode:
		return stRelayMode
	case refusedDeny:
		return stRelayDeny
	case refusedBudget:
		return stRelayBudget
	default:
		return stRelayFull
	}
}

// refuseRelay counts a refused relay request, for the hourly summary and
// the panel.
func (s *Server) refuseRelay(reason string) {
	s.relayCounters.refuse(reason)
	s.stats.add(relayRefusal(reason))
}
