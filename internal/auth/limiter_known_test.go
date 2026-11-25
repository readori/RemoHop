package auth

import (
	"net/netip"
	"testing"
	"time"
)

// Strangers who keep the global budget empty cannot keep out a source that
// logged in with the password before; that source still has its own
// lockouts, and its failures do not spend the budget (security audit:
// host password logins could be denied by anyone with a few addresses).
func TestGlobalBudgetSparesKnownSources(t *testing.T) {
	clock := newClock()
	l := NewLimiter(LimiterConfig{Now: clock.now, GlobalBurst: 5, GlobalRefill: 5 * time.Second})
	owner := netip.MustParseAddr("192.0.2.10")
	a, err := l.Begin(bg, owner)
	if err != nil {
		t.Fatal(err)
	}
	a.Success()

	src := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}) }
	for i := range 5 {
		a, err := l.Begin(bg, src(i))
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		a.Fail()
	}
	if _, err := l.Begin(bg, src(100)); CodeOf(err) != CodeLockedOut {
		t.Fatalf("stranger with the budget spent: %v, want locked_out", err)
	}
	a, err = l.Begin(bg, owner)
	if err != nil {
		t.Fatalf("known source refused while strangers spend the budget: %v", err)
	}
	a.Success()

	// Its own failures lock it out as before and leave the budget alone.
	l.refill(clock.now())
	tokens := l.tokens // below the burst: strangers spent it
	for i := range 3 {
		a, err := l.Begin(bg, owner)
		if err != nil {
			t.Fatalf("known source guess %d: %v", i, err)
		}
		a.Fail()
	}
	if _, err := l.Begin(bg, owner); CodeOf(err) != CodeLockedOut {
		t.Fatalf("known source after three failures: %v, want locked_out", err)
	}
	if l.tokens != tokens {
		t.Fatalf("known source's failures spent the budget: %v -> %v", tokens, l.tokens)
	}

	// Known only for a while.
	clock.advance(31 * 24 * time.Hour)
	for i := range 5 {
		if a, err := l.Begin(bg, src(200+i)); err == nil {
			a.Fail()
		}
	}
	if _, err := l.Begin(bg, owner); CodeOf(err) != CodeLockedOut {
		t.Fatalf("a source is known forever: %v", err)
	}
}
