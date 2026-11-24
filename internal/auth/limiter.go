package auth

import (
	"context"
	"net/netip"
	"sync"
	"time"
)

// LimiterConfig is the brute-force policy for password logins. Zero fields
// take the defaults documented on each.
type LimiterConfig struct {
	// FreeFailures is how many failed attempts a source may make before
	// lockouts start (room for typos). Default 2, so the third consecutive
	// failure locks the source out.
	FreeFailures int
	// BaseLock is the first lockout; each further failure doubles it up to
	// MaxLock. Defaults 10 s and 10 min.
	BaseLock time.Duration
	MaxLock  time.Duration
	// Forget is how long a source must stay quiet before its failure count
	// is cleared. Default 30 min.
	Forget time.Duration
	// GlobalBurst and GlobalRefill bound failed attempts across all
	// sources: an attacker with many addresses is held to one guess per
	// GlobalRefill once the burst is spent. Defaults 20 and 5 s.
	GlobalBurst  int
	GlobalRefill time.Duration
	// MaxSources caps the per-source table. Default 4096.
	MaxSources int
	// Now is the clock; tests replace it.
	Now func() time.Time
}

func (c *LimiterConfig) defaults() {
	if c.FreeFailures == 0 {
		c.FreeFailures = 2
	}
	if c.BaseLock == 0 {
		c.BaseLock = 10 * time.Second
	}
	if c.MaxLock == 0 {
		c.MaxLock = 10 * time.Minute
	}
	if c.Forget == 0 {
		c.Forget = 30 * time.Minute
	}
	if c.GlobalBurst == 0 {
		c.GlobalBurst = 20
	}
	if c.GlobalRefill == 0 {
		c.GlobalRefill = 5 * time.Second
	}
	if c.MaxSources == 0 {
		c.MaxSources = 4096
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Limiter throttles password guessing on a host.
//
// Two budgets apply. Per source (an IPv4 address or an IPv6 /64): a few free
// failures, then exponentially growing lockouts. Globally: a token bucket of
// failures, so a botnet that never reuses an address is still limited to a
// trickle. The global budget can delay a legitimate password login while an
// attack is running; paired clients are unaffected because token logins
// bypass the limiter.
//
// Attempts in progress count as if they had already failed. A source may
// have only as many attempts open at once as it has failures left before
// the next lockout; further ones wait for an open attempt to finish. So an
// attacker gains nothing by opening many connections in parallel or by
// never finishing a handshake, while a legitimate client that starts
// several sessions at once just sees them log in one after another.
type Limiter struct {
	cfg LimiterConfig

	mu      sync.Mutex
	sources map[netip.Addr]*source
	tokens  float64 // global budget
	filled  time.Time
	changed chan struct{} // closed and replaced whenever an attempt ends
}

type source struct {
	fails       int
	pending     int
	lockedUntil time.Time
	last        time.Time
}

// NewLimiter returns a limiter with the given policy.
func NewLimiter(cfg LimiterConfig) *Limiter {
	cfg.defaults()
	return &Limiter{
		cfg:     cfg,
		sources: map[netip.Addr]*source{},
		tokens:  float64(cfg.GlobalBurst),
		filled:  cfg.Now(),
		changed: make(chan struct{}),
	}
}

// sourceKey groups addresses an attacker can cheaply rotate through: any
// host with IPv6 owns at least a /64.
func sourceKey(a netip.Addr) netip.Addr {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		if p, err := a.Prefix(64); err == nil {
			return p.Addr()
		}
	}
	return a
}

func (l *Limiter) refill(now time.Time) {
	if d := now.Sub(l.filled); d > 0 {
		l.tokens = min(float64(l.cfg.GlobalBurst), l.tokens+float64(d)/float64(l.cfg.GlobalRefill))
		l.filled = now
	}
}

func (l *Limiter) lockFor(fails int) time.Duration {
	over := fails - l.cfg.FreeFailures
	if over <= 0 {
		return 0
	}
	d := l.cfg.BaseLock
	for i := 1; i < over && d < l.cfg.MaxLock; i++ {
		d *= 2
	}
	return min(d, l.cfg.MaxLock)
}

// Attempt is one admitted password attempt. Exactly one of Success or Fail
// takes effect; later calls are no-ops, so `defer a.Fail()` is safe.
type Attempt struct {
	l    *Limiter
	key  netip.Addr
	done bool
}

// Begin admits a password attempt from src, waiting (until ctx ends) while
// the source already has as many attempts open as it may fail. It returns a
// *Error with CodeLockedOut when the source or the host as a whole is over
// budget.
func (l *Limiter) Begin(ctx context.Context, src netip.Addr) (*Attempt, error) {
	key := sourceKey(src)
	for {
		l.mu.Lock()
		now := l.cfg.Now()
		s := l.sources[key]
		if s != nil && s.pending == 0 && now.Sub(s.last) > l.cfg.Forget && !now.Before(s.lockedUntil) {
			delete(l.sources, key)
			s = nil
		}
		if s != nil && now.Before(s.lockedUntil) {
			wait := s.lockedUntil.Sub(now)
			l.mu.Unlock()
			return nil, &Error{Code: CodeLockedOut, RetryAfter: wait}
		}
		// If every open attempt and this one failed, would that stay
		// within one lockout step? With no attempt open the answer is
		// always yes: the source has served its last lockout.
		if s != nil && s.pending > 0 && s.fails+s.pending > l.cfg.FreeFailures {
			changed := l.changed
			l.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, &Error{Code: CodeLockedOut, RetryAfter: time.Second, Detail: "too many logins in progress"}
			}
		}
		l.refill(now)
		if l.tokens < 1 {
			wait := time.Duration((1 - l.tokens) * float64(l.cfg.GlobalRefill))
			l.mu.Unlock()
			return nil, &Error{Code: CodeLockedOut, RetryAfter: wait}
		}
		if s == nil {
			l.evict(now)
			s = &source{}
			l.sources[key] = s
		}
		l.tokens--
		s.pending++
		s.last = now
		l.mu.Unlock()
		return &Attempt{l: l, key: key}, nil
	}
}

// evict keeps the table bounded. Idle entries past their Forget time go
// first; if the table is still full the least recently active idle source
// is dropped. Losing a source's count is safe because the global budget
// does not depend on the table.
func (l *Limiter) evict(now time.Time) {
	if len(l.sources) < l.cfg.MaxSources {
		return
	}
	var oldest netip.Addr
	var oldestAt time.Time
	for k, s := range l.sources {
		if s.pending > 0 {
			continue
		}
		if now.Sub(s.last) > l.cfg.Forget && !now.Before(s.lockedUntil) {
			delete(l.sources, k)
			continue
		}
		if oldestAt.IsZero() || s.last.Before(oldestAt) {
			oldest, oldestAt = k, s.last
		}
	}
	if len(l.sources) >= l.cfg.MaxSources && !oldestAt.IsZero() {
		delete(l.sources, oldest)
	}
}

// finish ends the attempt and wakes attempts waiting on the source.
func (a *Attempt) finish(success bool) {
	l := a.l
	l.mu.Lock()
	defer l.mu.Unlock()
	if a.done {
		return
	}
	a.done = true
	close(l.changed)
	l.changed = make(chan struct{})

	s := l.sources[a.key]
	if s == nil {
		return
	}
	s.pending--
	now := l.cfg.Now()
	s.last = now
	if success {
		// The global charge is refunded and the source's history cleared.
		l.tokens = min(float64(l.cfg.GlobalBurst), l.tokens+1)
		s.fails, s.lockedUntil = 0, time.Time{}
		if s.pending == 0 {
			delete(l.sources, a.key)
		}
		return
	}
	s.fails++
	if d := l.lockFor(s.fails); d > 0 {
		s.lockedUntil = now.Add(d)
	}
}

// Success records that the client proved knowledge of the password.
func (a *Attempt) Success() { a.finish(true) }

// Fail records a failed (or abandoned) guess.
func (a *Attempt) Fail() { a.finish(false) }
