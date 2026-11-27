package signal

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/nat"
)

// Hardening against denial of service by anonymous clients: each test
// holds the server to a limit an attacker without credentials could
// otherwise get round.

// clientFrom dials the server from a socket bound to ip (any 127/8 address
// works on Linux), so that the server counts it as its own source.
func (as *acctServer) clientFrom(ip string) *Client {
	as.t.Helper()
	sock, err := nat.Listen(ip + ":0")
	if err != nil {
		as.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, sock, as.addr, Options{})
	if err != nil {
		as.t.Fatal(err)
	}
	as.t.Cleanup(func() { c.Close(); sock.Close() })
	return c
}

// Extension requests read their body before taking a slot: a client that
// sends headers only holds none.
func TestExtHeaderOnlyHoldsNoSlot(t *testing.T) {
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{})) // default ExtLimits: 20 burst, 5/s, 64 at once
	ctx := tctx(t)
	held := 0
	for _, ip := range []string{"127.0.0.2", "127.0.0.3", "127.0.0.4", "127.0.0.5"} {
		for range 2 { // 2 connections x 8 streams = 16 per source, inside its burst of 20
			c := as.clientFrom(ip)
			for range 8 {
				st, err := c.q.OpenStreamSync(ctx)
				if err != nil {
					t.Fatal(err)
				}
				// Header only; the body never comes.
				if err := writeMsg(st, request{T: "x-test-echo", V: protoVersion}); err != nil {
					t.Fatal(err)
				}
				held++
			}
		}
	}
	time.Sleep(300 * time.Millisecond)
	if held < as.srv.cfg.Ext.MaxConcurrent {
		t.Fatalf("only %d header-only streams; the test needs at least MaxConcurrent", held)
	}
	victim := as.clientFrom("127.0.0.9")
	if _, err := victim.Ext(ctx, "x-test-echo", nil, "hello"); err != nil {
		t.Fatalf("a legitimate request while %d header-only streams wait: %v", held, err)
	}
}

// The relay cap per source is charged to the client that asks, not to the
// host, so one client cannot take a host's whole share.
func TestRelayHostShareNotTakenByOneClient(t *testing.T) {
	as, _ := policyServer(t, RelayConfig{PerSource: 2})
	s := as.srv
	host := relayPeer{source: netip.MustParseAddr("203.0.113.5"), addr: netip.MustParseAddr("203.0.113.5")}
	attacker := relayPeer{source: netip.MustParseAddr("198.51.100.7"), addr: netip.MustParseAddr("198.51.100.7")}
	victim := relayPeer{source: netip.MustParseAddr("192.0.2.9"), addr: netip.MustParseAddr("192.0.2.9")}
	for i := range 2 {
		if _, reason, msg := s.admitRelay(attacker, host); reason != "" {
			t.Fatalf("attacker session %d refused: %s", i+1, msg)
		}
	}
	if _, reason, msg := s.admitRelay(victim, host); reason != "" {
		t.Fatalf("another client refused the relay to the host: %s", msg)
	}
	if _, reason, _ := s.admitRelay(attacker, host); reason != refusedPerSource {
		t.Fatalf("a third session for one client source: %q", reason)
	}
}

// A relay session lives while both sides speak, not while one sends junk.
func TestRelayNeedsBothSides(t *testing.T) {
	relayPC, err := netListenUDP()
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, nat.NonceSize)
	done := make(chan nat.RelayStats, 1)
	go func() {
		done <- nat.ServeRelay(context.Background(), relayPC, nonce, nat.RelayLimits{Rate: 1 << 20, Idle: 300 * time.Millisecond})
	}()
	relay := netip.MustParseAddrPort(relayPC.LocalAddr().String())
	hostSock, _ := nat.Listen("127.0.0.1:0")
	defer hostSock.Close()
	atk, _ := nat.Listen("127.0.0.1:0")
	defer atk.Close()
	ctx := tctx(t)
	if err := hostSock.BindRelay(ctx, relay, nonce, nat.RoleHost); err != nil {
		t.Fatal(err)
	}
	if err := atk.BindRelay(ctx, relay, nonce, nat.RoleClient); err != nil {
		t.Fatal(err)
	}
	// The attacker's own socket must be the bound one: send from it.
	stop := time.After(2 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-stop:
			break loop
		case <-tick.C:
			atk.Transport().WriteTo([]byte{0x40, 1, 2, 3}, netUDPAddr(relay))
		}
	}
	select {
	case <-done:
	default:
		t.Fatal("a relay session outlived Idle with only one side speaking")
	}
}

func netListenUDP() (net.PacketConn, error) { return net.ListenPacket("udp", "127.0.0.1:0") }

func netUDPAddr(ap netip.AddrPort) *net.UDPAddr { return net.UDPAddrFromAddrPort(ap) }

// One network (an IPv6 /48) holds at most MaxConnsPerNet connections,
// whatever number of its /64 sources they come from; other networks are
// still served.
func TestConnCapPerNetwork(t *testing.T) {
	srv, err := NewServer(Config{DataDir: t.TempDir(), Limits: Limits{MaxConns: 100, MaxConnsPerNet: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	for i := range 4 {
		src, n := sourceOf(netip.MustParseAddr(fmt.Sprintf("2001:db8:aa:%x::1", i+1)))
		if !srv.admitConn(src, n) {
			t.Fatalf("connection %d of the network refused", i+1)
		}
	}
	src, n := sourceOf(netip.MustParseAddr("2001:db8:aa:99::1"))
	if srv.admitConn(src, n) {
		t.Fatal("a fifth /64 of the same /48 was admitted")
	}
	other, on := sourceOf(netip.MustParseAddr("2001:db8:bb:1::1"))
	if !srv.admitConn(other, on) {
		t.Fatal("another network was refused")
	}
	srv.mu.Lock()
	first, fn := sourceOf(netip.MustParseAddr("2001:db8:aa:1::1"))
	srv.releaseConnLocked(first, fn)
	srv.mu.Unlock()
	if !srv.admitConn(src, n) {
		t.Fatal("the network's slot was not freed")
	}
	v4, v4n := sourceOf(netip.MustParseAddr("192.0.2.1"))
	if !srv.admitConn(v4, v4n) {
		t.Fatal("IPv4 refused")
	}
}

// Asking for a new login code does not reset the count of wrong ones.
func TestNewCodeKeepsWrongAttempts(t *testing.T) {
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{}))
	ctx := tctx(t)
	c := as.client()
	id := newID(t)
	const email = "victim@example.com"
	wrongGuess := func(real string) {
		t.Helper()
		guess := "00000000"
		if guess == real {
			guess = "11111111"
		}
		c.AccountVerify(ctx, id, email, guess, "", "")
	}
	if err := c.AccountLogin(ctx, email); err != nil {
		t.Fatal(err)
	}
	real := as.mailer.code(t, email)
	for range as.srv.accounts.cfg.MaxAttempts - 1 {
		wrongGuess(real)
	}
	// A new code, then one more wrong guess: that is MaxAttempts in all.
	if err := c.AccountLogin(ctx, email); err != nil {
		t.Fatal(err)
	}
	real = as.mailer.code(t, email)
	wrongGuess(real)
	if _, err := c.AccountVerify(ctx, id, email, real, "", ""); err == nil {
		t.Fatal("the right code worked after MaxAttempts wrong ones spread over two codes")
	}
	if err := c.AccountLogin(ctx, email); err == nil {
		t.Fatal("a new code was sent to a locked-out address")
	}
}
