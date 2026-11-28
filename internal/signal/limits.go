package signal

import (
	"net/netip"
	"slices"
	"sync"
	"time"
)

// Limits are the server's abuse controls. Zero fields take the defaults
// documented on each; tests shrink them.
//
// Limits apply at three widths, because addresses are not equally scarce.
// A "source" is an IPv4 address or an IPv6 /64, the least any one host has.
// A "network" is an IPv6 /48, what a single customer is routinely given and
// which holds 65536 sources (for IPv4 the network is the address itself).
// And some budgets are global, so that no number of networks can exhaust
// what they protect.
type Limits struct {
	// MaxConns and MaxConnsPerSource bound open QUIC connections.
	// Defaults 50000 and 64 (the per-source bound has to leave room for
	// many hosts behind one carrier-grade NAT address).
	MaxConns          int
	MaxConnsPerSource int
	// MaxConnsPerNet bounds those of one network (an IPv6 /48), so that the
	// many /64s of one customer cannot fill the table. Default 256. (For
	// IPv4 the network is the address, so MaxConnsPerSource applies.)
	MaxConnsPerNet int
	// Register*: registrations per source. Defaults: burst 10, one more
	// every 6 s.
	RegisterBurst int
	RegisterEvery time.Duration

	// New IDs are the only thing a stranger can make the server keep, so
	// allocating one draws on three budgets. Per source: burst 5, one
	// more every 10 min. Per network: burst 20, one more every 2 min.
	// Globally: burst 500, one more every IDExpiry/MaxIDs (7.8 s with the
	// defaults). The global rate is chosen so that IDs which are never
	// used again expire as fast as they can be created: the table cannot
	// be filled for good that way, whatever the number of addresses.
	NewIDBurst       int
	NewIDEvery       time.Duration
	NewIDNetBurst    int
	NewIDNetEvery    time.Duration
	NewIDGlobalBurst int
	NewIDGlobalEvery time.Duration
	// MaxIDs caps the ID table. Default 1,000,000.
	MaxIDs int
	// IDExpiry is how long an ID is kept for a host that does not
	// register. Default 90 days.
	IDExpiry time.Duration

	// Connect*: connect requests per source and per network. This limits
	// use of the server to make hosts emit probe packets, while leaving
	// room for scripts and agents that run many short commands, each of
	// which is one request. Defaults: burst 60 and one more every 500 ms
	// per source; burst 240 and one more every 125 ms per network.
	ConnectBurst    int
	ConnectEvery    time.Duration
	ConnectNetBurst int
	ConnectNetEvery time.Duration
	// Miss*: connect requests, from anyone, for IDs that are unknown or
	// offline. This is what limits finding out which IDs exist by trying
	// them. Default: burst 1000, one more every 50 ms; at 20 misses a
	// second, sweeping the 900 million possible IDs takes all the
	// world's addresses together more than a year. When the budget is
	// spent such requests are answered "rate limited" instead of
	// "unknown" or "offline"; requests for hosts that are online are not
	// affected.
	MissBurst int
	MissEvery time.Duration
	// Offer*: offers delivered to one host, whoever asks. Defaults: burst
	// 40, one more every 250 ms. A host punches for about 13 s per offer
	// that leads nowhere and runs at most 128 punches at once, so even a
	// sustained flood of requests for one ID (from many sources) leaves
	// the host room to answer real clients.
	OfferBurst int
	OfferEvery time.Duration
	// ClientLifetime is how long a connection that has not registered may
	// stay open; a dialling peer needs a few seconds. Default 60 s.
	ClientLifetime time.Duration
}

func (l *Limits) defaults() {
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	defDur := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	def(&l.MaxConns, 50000)
	def(&l.MaxConnsPerSource, 64)
	def(&l.MaxConnsPerNet, 256)
	def(&l.RegisterBurst, 10)
	defDur(&l.RegisterEvery, 6*time.Second)
	def(&l.MaxIDs, 1_000_000)
	defDur(&l.IDExpiry, 90*24*time.Hour)
	def(&l.NewIDBurst, 5)
	defDur(&l.NewIDEvery, 10*time.Minute)
	def(&l.NewIDNetBurst, 20)
	defDur(&l.NewIDNetEvery, 2*time.Minute)
	def(&l.NewIDGlobalBurst, 500)
	defDur(&l.NewIDGlobalEvery, max(l.IDExpiry/time.Duration(l.MaxIDs), time.Millisecond))
	def(&l.ConnectBurst, 60)
	defDur(&l.ConnectEvery, 500*time.Millisecond)
	def(&l.ConnectNetBurst, 240)
	defDur(&l.ConnectNetEvery, 125*time.Millisecond)
	def(&l.MissBurst, 1000)
	defDur(&l.MissEvery, 50*time.Millisecond)
	def(&l.OfferBurst, 40)
	defDur(&l.OfferEvery, 250*time.Millisecond)
	defDur(&l.ClientLifetime, 60*time.Second)
}

// sourceOf reduces an address to the two widths per-address limits are
// applied at.
func sourceOf(a netip.Addr) (source, network netip.Addr) {
	a = a.Unmap().WithZone("")
	if !a.Is6() {
		return a, a
	}
	source, network = a, a
	if p, err := a.Prefix(64); err == nil {
		source = p.Addr()
	}
	if p, err := a.Prefix(48); err == nil {
		network = p.Addr()
	}
	return source, network
}

// rateTable is a set of token buckets keyed by K.
type rateTable[K comparable] struct {
	burst float64
	every time.Duration
	max   int

	mu      sync.Mutex
	buckets map[K]*bucket
	seq     uint64 // counts allow calls; orders buckets by last use
}

type bucket struct {
	tokens float64
	at     time.Time
	used   uint64 // value of rateTable.seq at the last allow
}

// rateTableMax bounds a table's memory.
const rateTableMax = 100_000

func newRateTable[K comparable](burst int, every time.Duration) *rateTable[K] {
	return &rateTable[K]{burst: float64(burst), every: every, max: rateTableMax, buckets: map[K]*bucket{}}
}

// allow takes one token for key. When none is left it reports how long
// until the next one.
func (t *rateTable[K]) allow(key K) (bool, time.Duration) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	b := t.buckets[key]
	if b == nil {
		if len(t.buckets) >= t.max {
			t.evict(now)
		}
		b = &bucket{tokens: t.burst, at: now}
		t.buckets[key] = b
	} else {
		b.tokens = min(t.burst, b.tokens+float64(now.Sub(b.at))/float64(t.every))
		b.at = now
	}
	b.used = t.seq
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(t.every))
	}
	b.tokens--
	return true, 0
}

// peek reports whether key has a token left, without taking it.
func (t *rateTable[K]) peek(key K) (bool, time.Duration) {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.buckets[key]
	if b == nil {
		return true, 0
	}
	tokens := min(t.burst, b.tokens+float64(now.Sub(b.at))/float64(t.every))
	if tokens < 1 {
		return false, time.Duration((1 - tokens) * float64(t.every))
	}
	return true, 0
}

// evict makes room in a full table. Buckets that have refilled completely
// carry no information and go first. If that is not enough, the keys that
// have been quiet longest are forgotten, about an eighth of the table at a
// time: a key that comes back afterwards starts with a fresh burst, which
// is the price of staying available to everybody else when someone turns
// up with more addresses than the table holds. What such a crowd can do in
// total is bounded by the global budgets, which have no table to overflow.
func (t *rateTable[K]) evict(now time.Time) {
	full := time.Duration(t.burst * float64(t.every))
	for k, b := range t.buckets {
		if now.Sub(b.at) >= full {
			delete(t.buckets, k)
		}
	}
	if len(t.buckets) < t.max {
		return
	}
	// Estimate, from a sample (map iteration order is random), the point
	// in the order of last use below which the quietest eighth lies, and
	// drop everything below it. The order is a counter, not the clock:
	// clocks tick coarsely enough on some systems for thousands of keys
	// to share a timestamp, and a tie must not take the busiest key with
	// the idle ones.
	const sample = 64
	uses := make([]uint64, 0, sample)
	for _, b := range t.buckets {
		if uses = append(uses, b.used); len(uses) == sample {
			break
		}
	}
	slices.Sort(uses)
	cutoff := uses[len(uses)/8]
	for k, b := range t.buckets {
		if b.used <= cutoff {
			delete(t.buckets, k)
		}
	}
}
