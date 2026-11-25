package signal

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

const serverAddr = "198.51.100.1:21116"

// testNet is a virtual internet with a rendezvous server on it.
type testNet struct {
	t    *testing.T
	net  *vnet.Net
	srv  *Server
	dir  string
	stop func()
}

func newTestNet(t *testing.T, limits Limits) *testNet {
	t.Helper()
	tn := &testNet{t: t, net: vnet.New(), dir: t.TempDir()}
	tn.net.Host("198.51.100.1")
	tn.start(limits)
	return tn
}

// start (re)starts the server on the same data directory.
func (tn *testNet) start(limits Limits) {
	tn.t.Helper()
	srv, err := NewServer(Config{DataDir: tn.dir, Limits: limits})
	if err != nil {
		tn.t.Fatal(err)
	}
	host := tn.net.Host("198.51.100.1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, host.MustListen(21116), host.MustListen(21117)) }()
	tn.srv = srv
	stopped := false
	tn.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			tn.t.Errorf("Serve: %v", err)
		}
		srv.Close()
	}
	tn.t.Cleanup(tn.stop)
}

func (tn *testNet) socket(h *vnet.Host) *nat.Socket {
	tn.t.Helper()
	sock, err := nat.NewSocket(h.MustListen(0))
	if err != nil {
		tn.t.Fatal(err)
	}
	sock.Locals = func() []netip.Addr { return []netip.Addr{h.Addr()} }
	tn.t.Cleanup(func() { sock.Close() })
	return sock
}

func (tn *testNet) dial(sock *nat.Socket, opts Options) *Client {
	tn.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, sock, serverAddr, opts)
	if err != nil {
		tn.t.Fatalf("Dial: %v", err)
	}
	tn.t.Cleanup(func() { c.Close() })
	return c
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newIdentity(t *testing.T) *auth.Identity {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRegisterAndConnect(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)

	hostSock := tn.socket(tn.net.NAT("203.0.113.20", vnet.PortRestrictedCone).Host("10.0.0.20"))
	hc := tn.dial(hostSock, Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !validID(reg.ID) {
		t.Fatalf("ID %q is not nine digits starting non-zero", reg.ID)
	}
	if reg.Reflexive.Addr() != netip.MustParseAddr("203.0.113.20") {
		t.Fatalf("host reflexive %s, want the NAT's public address", reg.Reflexive)
	}
	if reg.Alt != netip.MustParseAddrPort("198.51.100.1:21117") {
		t.Fatalf("alt address %s", reg.Alt)
	}
	// The address the server reports is the mapping of the shared socket:
	// the NAT-probe port sees the same one through a cone NAT.
	if typ := hc.NATType(); typ != nat.TypeCone || hostSock.NATType() != typ {
		t.Fatalf("host NAT detected as %s (socket says %s), want cone", typ, hostSock.NATType())
	}

	offers := make(chan Offer, 1)
	go hc.ServeOffers(ctx, func(o Offer) Answer {
		offers <- o
		return Answer{Local: hostSock.Candidates(), NAT: nat.TypeCone}
	})

	clientSock := tn.socket(tn.net.NAT("203.0.113.10", vnet.Symmetric).Host("192.168.1.10"))
	cc := tn.dial(clientSock, Options{})
	// The client's NAT type is known before it asks for the introduction,
	// measured against the server alone: nothing has been punched yet.
	clientNAT := cc.NATType()
	if clientNAT != nat.TypeSymmetric {
		t.Fatalf("client NAT detected as %s, want symmetric", clientNAT)
	}
	in, err := cc.Connect(ctx, reg.ID, clientSock.Candidates(), clientNAT)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	offer := <-offers

	if string(in.Nonce) != string(offer.Nonce) || len(in.Nonce) != nat.NonceSize {
		t.Fatal("client and host did not get the same nonce")
	}
	if in.Peer.Reflexive != reg.Reflexive {
		t.Errorf("client was told host is at %s, want %s", in.Peer.Reflexive, reg.Reflexive)
	}
	if len(in.Peer.Local) != 1 || in.Peer.Local[0].Addr() != netip.MustParseAddr("10.0.0.20") {
		t.Errorf("host local candidates = %v", in.Peer.Local)
	}
	if in.Peer.NAT != nat.TypeCone || offer.Peer.NAT != nat.TypeSymmetric {
		t.Errorf("NAT types not relayed: client sees %s, host sees %s", in.Peer.NAT, offer.Peer.NAT)
	}
	if offer.Peer.Reflexive != in.Reflexive || in.Reflexive.Addr() != netip.MustParseAddr("203.0.113.10") {
		t.Errorf("host was told client is at %s, client was told %s", offer.Peer.Reflexive, in.Reflexive)
	}
	if len(offer.Peer.Local) != 1 || offer.Peer.Local[0].Addr() != netip.MustParseAddr("192.168.1.10") {
		t.Errorf("client local candidates = %v", offer.Peer.Local)
	}
	if got := in.Peer.Candidates(); len(got) != 2 || got[0] != reg.Reflexive {
		t.Errorf("Candidates() = %v", got)
	}

	// A second introduction gets a fresh nonce.
	in2, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
	if err != nil {
		t.Fatal(err)
	}
	<-offers
	if string(in2.Nonce) == string(in.Nonce) {
		t.Fatal("nonce reused across introductions")
	}
}

func TestIDsSurviveRestart(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	idA, idB := newIdentity(t), newIdentity(t)
	pub := tn.net.Host("192.0.2.1")

	register := func(id *auth.Identity) string {
		t.Helper()
		c := tn.dial(tn.socket(pub), Options{})
		reg, err := c.Register(ctx, id)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		c.Close()
		return reg.ID
	}
	a1, b1 := register(idA), register(idB)
	if a1 == b1 {
		t.Fatal("two keys got the same ID")
	}
	if register(idA) != a1 {
		t.Fatal("ID changed on re-registration")
	}
	key := tn.srv.PublicKey()

	tn.stop()
	tn.start(Limits{})
	if !tn.srv.PublicKey().Equal(key) {
		t.Fatal("server key changed across restart")
	}
	if a2, b2 := register(idA), register(idB); a2 != a1 || b2 != b1 {
		t.Fatalf("IDs changed across restart: %s→%s, %s→%s", a1, a2, b1, b2)
	}
}

func TestUnknownAndOffline(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")

	hc := tn.dial(tn.socket(pub), Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	cc := tn.dial(tn.socket(pub), Options{})

	other := "100000000"
	if other == reg.ID {
		other = "100000001"
	}
	if _, err := cc.Connect(ctx, other, nil, nat.TypeUnknown); CodeOf(err) != CodeUnknownID {
		t.Fatalf("never-registered ID: %v, want unknown_id", err)
	}
	if _, err := cc.Connect(ctx, "12345", nil, nat.TypeUnknown); CodeOf(err) != CodeBadRequest {
		t.Fatalf("malformed ID: %v, want bad_request", err)
	}

	// Registered and connected, but not answering offers.
	start := time.Now()
	if _, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown); CodeOf(err) != CodeNoAnswer {
		t.Fatalf("silent host: %v, want no_answer", err)
	}
	if time.Since(start) > offerTimeout+2*time.Second {
		t.Fatalf("no_answer took %s", time.Since(start))
	}

	hc.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
		if CodeOf(err) == CodeOffline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after host left: %v, want offline", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeclinedOffer(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	hc := tn.dial(tn.socket(pub), Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{Decline: "busy"} })
	cc := tn.dial(tn.socket(pub), Options{})
	_, err = cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
	if CodeOf(err) != CodeDeclined || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("declined offer: %v", err)
	}
}

// A newer registration with the same key takes over; the old connection is
// closed rather than left to receive offers nobody answers.
func TestReRegistrationReplaces(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	id := newIdentity(t)

	old := tn.dial(tn.socket(pub), Options{})
	reg1, err := old.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cur := tn.dial(tn.socket(pub), Options{})
	reg2, err := cur.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if reg1.ID != reg2.ID {
		t.Fatal("same key, different IDs")
	}
	select {
	case <-old.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("replaced connection still open")
	}
	go cur.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
	cc := tn.dial(tn.socket(pub), Options{})
	if _, err := cc.Connect(ctx, reg1.ID, nil, nat.TypeUnknown); err != nil {
		t.Fatalf("connect after re-registration: %v", err)
	}
}

// A registered host's NAT changes its mapping (a rebooted router, a new
// public address, the laptop on another network) and the rendezvous
// connection follows it. Clients must be given the new address at once, and
// the host is asked to register again, so it measures its NAT afresh.
func TestHostMovesBehindItsNAT(t *testing.T) {
	defer func(d time.Duration) { moveCheckEvery = d }(moveCheckEvery)
	moveCheckEvery = 50 * time.Millisecond
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	box := tn.net.NAT("203.0.113.20", vnet.PortRestrictedCone)
	hostSock := tn.socket(box.Host("10.0.0.20"))
	id := newIdentity(t)
	hc := tn.dial(hostSock, Options{})
	reg, err := hc.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{Local: hostSock.Candidates()} })

	box.Rebind()
	// Any packet from the host now leaves through a new mapping.
	if _, err := hc.Presence(ctx, []string{reg.ID}); err != nil {
		t.Fatalf("presence after the NAT rebound: %v", err)
	}
	cc := tn.dial(tn.socket(tn.net.NAT("203.0.113.10", vnet.PortRestrictedCone).Host("192.168.1.10")), Options{})
	in, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
	if err != nil {
		t.Fatalf("connect after the host moved: %v", err)
	}
	if in.Peer.Reflexive == reg.Reflexive || in.Peer.Reflexive.Addr() != reg.Reflexive.Addr() {
		t.Fatalf("client was told the host is at %s; it registered from %s and has since moved to another port", in.Peer.Reflexive, reg.Reflexive)
	}
	select {
	case <-hc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the moved host was not asked to register again")
	}
	if ae, ok := errors.AsType[*quic.ApplicationError](hc.Err()); !ok || ae.ErrorCode != closeMoved {
		t.Fatalf("host connection ended with %v, want code %d", hc.Err(), closeMoved)
	}
	// Registering again from the new mapping works and reports it.
	hc2 := tn.dial(hostSock, Options{})
	reg2, err := hc2.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if reg2.ID != reg.ID || reg2.Reflexive.Addr() != reg.Reflexive.Addr() || reg2.Reflexive == reg.Reflexive {
		t.Fatalf("re-registered at %s", reg2.Reflexive)
	}
}

// Introductions are remembered for a possible relay request, for a minute
// and no longer, and without holding on to either peer's connection.
func TestIntroductionsExpire(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	hc := tn.dial(tn.socket(tn.net.Host("192.0.2.20")), Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
	cc := tn.dial(tn.socket(tn.net.Host("192.0.2.10")), Options{})
	if _, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown); err != nil {
		t.Fatal(err)
	}
	cc.Close()
	count := func() int {
		tn.srv.mu.Lock()
		defer tn.srv.mu.Unlock()
		return len(tn.srv.intros)
	}
	if n := count(); n != 1 {
		t.Fatalf("%d introductions remembered, want 1", n)
	}
	tn.srv.expireIntros(time.Now())
	if n := count(); n != 1 {
		t.Fatal("a fresh introduction was forgotten")
	}
	tn.srv.expireIntros(time.Now().Add(introTTL + time.Second))
	if n := count(); n != 0 {
		t.Fatalf("%d introductions left after their time", n)
	}
}

func TestOneIdentityPerConnection(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	first := newIdentity(t)
	c := tn.dial(tn.socket(pub), Options{})
	reg, err := c.Register(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(ctx, newIdentity(t)); CodeOf(err) != CodeBadRequest {
		t.Fatalf("second identity on one connection: %v, want bad_request", err)
	}
	if again, err := c.Register(ctx, first); err != nil || again.ID != reg.ID {
		t.Fatalf("repeating the same registration: %+v, %v", again, err)
	}
	// Presence follows the connection exactly.
	c.Close()
	cc := tn.dial(tn.socket(pub), Options{})
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
		if CodeOf(err) == CodeOffline {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the host left: %v, want offline", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPin(t *testing.T) {
	tn := newTestNet(t, Limits{})
	sock := tn.socket(tn.net.Host("192.0.2.1"))

	good := tn.dial(sock, Options{Pin: auth.EncodeKey(tn.srv.PublicKey())})
	if !good.ServerKey().Equal(tn.srv.PublicKey()) {
		t.Fatal("ServerKey does not report the server's key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wrong := auth.EncodeKey(newIdentity(t).Public())
	if _, err := Dial(ctx, sock, serverAddr, Options{Pin: wrong}); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("dial with wrong pin: %v", err)
	}
	if _, err := Dial(ctx, sock, serverAddr, Options{Pin: "garbage"}); err == nil {
		t.Fatal("malformed pin accepted")
	}
}

func TestRateLimits(t *testing.T) {
	tn := newTestNet(t, Limits{ConnectBurst: 3, ConnectEvery: time.Hour, NewIDBurst: 2, NewIDEvery: time.Hour})
	ctx := testCtx(t)
	attacker := tn.net.Host("192.0.2.66")

	// ID enumeration: lookups are charged whether or not the ID exists.
	cc := tn.dial(tn.socket(attacker), Options{})
	for i := 0; i < 3; i++ {
		if _, err := cc.Connect(ctx, "123456789", nil, nat.TypeUnknown); CodeOf(err) != CodeUnknownID {
			t.Fatalf("lookup %d: %v", i, err)
		}
	}
	// A new connection from the same address does not reset the budget.
	cc2 := tn.dial(tn.socket(attacker), Options{})
	_, err := cc2.Connect(ctx, "123456789", nil, nat.TypeUnknown)
	if e, _ := err.(*Error); e == nil || e.Code != CodeRateLimited || e.RetryAfter <= 0 {
		t.Fatalf("over budget: %v, want rate_limited with a retry time", err)
	}
	// Another address is not affected.
	other := tn.dial(tn.socket(tn.net.Host("192.0.2.77")), Options{})
	if _, err := other.Connect(ctx, "123456789", nil, nat.TypeUnknown); CodeOf(err) != CodeUnknownID {
		t.Fatalf("other source: %v", err)
	}

	// ID squatting: new IDs are scarce per source, re-registration is not.
	first := newIdentity(t)
	for i, id := range []*auth.Identity{first, newIdentity(t)} {
		if _, err := tn.dial(tn.socket(attacker), Options{}).Register(ctx, id); err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}
	if _, err := tn.dial(tn.socket(attacker), Options{}).Register(ctx, newIdentity(t)); CodeOf(err) != CodeRateLimited {
		t.Fatalf("third new ID: %v, want rate_limited", err)
	}
	if _, err := tn.dial(tn.socket(attacker), Options{}).Register(ctx, first); err != nil {
		t.Fatalf("re-registration of an existing key: %v", err)
	}
}

func TestIDTableFull(t *testing.T) {
	tn := newTestNet(t, Limits{MaxIDs: 1})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	if _, err := tn.dial(tn.socket(pub), Options{}).Register(ctx, newIdentity(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := tn.dial(tn.socket(pub), Options{}).Register(ctx, newIdentity(t)); CodeOf(err) != CodeFull {
		t.Fatalf("beyond MaxIDs: %v, want full", err)
	}
}

func TestConnectionLimits(t *testing.T) {
	// The lifetime must comfortably cover connecting and registering on a
	// slow machine; a registration cut off by it is this test's own doing.
	tn := newTestNet(t, Limits{MaxConnsPerSource: 2, ClientLifetime: 2 * time.Second})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")

	host := tn.dial(tn.socket(pub), Options{})
	if _, err := host.Register(ctx, newIdentity(t)); err != nil {
		t.Fatal(err)
	}
	idle := tn.dial(tn.socket(pub), Options{})
	// Third connection from the same source: refused.
	third := tn.dial(tn.socket(pub), Options{})
	select {
	case <-third.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("connection over the per-source limit stayed open")
	}
	// A connection that never registers is dropped; a registered host stays.
	select {
	case <-idle.Done():
	case <-time.After(8 * time.Second):
		t.Fatal("unregistered connection outlived ClientLifetime")
	}
	select {
	case <-host.Done():
		t.Fatalf("registered host was dropped: %v", host.Err())
	case <-time.After(500 * time.Millisecond):
	}
}

// rawStream opens a stream on a bare QUIC connection to the server, for
// sending things the Client would never send.
func rawStream(t *testing.T, tn *testNet, h *vnet.Host) (*quic.Conn, *quic.Stream) {
	t.Helper()
	sock := tn.socket(h)
	q, err := sock.Transport().Dial(testCtx(t), net.UDPAddrFromAddrPort(netip.MustParseAddrPort(serverAddr)),
		&tls.Config{InsecureSkipVerify: true, NextProtos: []string{ALPN}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.CloseWithError(0, "") })
	st, err := q.OpenStreamSync(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	st.SetDeadline(time.Now().Add(5 * time.Second))
	return q, st
}

func TestRegistrationNeedsTheKey(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	victim, attacker := newIdentity(t), newIdentity(t)

	vc := tn.dial(tn.socket(pub), Options{})
	reg, err := vc.Register(ctx, victim)
	if err != nil {
		t.Fatal(err)
	}

	// Claim the victim's key, sign with our own.
	q, st := rawStream(t, tn, tn.net.Host("192.0.2.66"))
	writeMsg(st, request{T: "register", V: protoVersion, Key: victim.Public()})
	var ch response
	if err := readMsg(st, &ch); err != nil || !ch.OK {
		t.Fatalf("challenge: %+v %v", ch, err)
	}
	cs := q.ConnectionState().TLS
	exporter, _ := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	writeMsg(st, proof{Sig: attacker.Sign(registerMessage(ch.Challenge, exporter))})
	var resp response
	if err := readMsg(st, &resp); err != nil || resp.OK || resp.Code != CodeBadRequest {
		t.Fatalf("forged proof: %+v %v", resp, err)
	}

	// A signature made for another connection (another exporter) is no
	// good either, even from the real key.
	q2, st2 := rawStream(t, tn, tn.net.Host("192.0.2.67"))
	_ = q2
	writeMsg(st2, request{T: "register", V: protoVersion, Key: victim.Public()})
	readMsg(st2, &ch)
	writeMsg(st2, proof{Sig: victim.Sign(registerMessage(ch.Challenge, exporter))})
	resp = response{}
	if err := readMsg(st2, &resp); err != nil || resp.OK {
		t.Fatalf("proof replayed across connections: %+v %v", resp, err)
	}

	// The victim is still the one online under its ID.
	select {
	case <-vc.Done():
		t.Fatal("victim's registration was displaced")
	default:
	}
	go vc.ServeOffers(ctx, func(Offer) Answer { return Answer{} })
	if _, err := tn.dial(tn.socket(pub), Options{}).Connect(ctx, reg.ID, nil, nat.TypeUnknown); err != nil {
		t.Fatalf("connect to victim: %v", err)
	}
}

func TestMalformedRequests(t *testing.T) {
	tn := newTestNet(t, Limits{})
	h := tn.net.Host("192.0.2.66")

	t.Run("oversized", func(t *testing.T) {
		_, st := rawStream(t, tn, h)
		hdr := make([]byte, 4)
		binary.BigEndian.PutUint32(hdr, 64<<20)
		st.Write(hdr)
		st.Write(make([]byte, 8192))
		var resp response
		if err := readMsg(st, &resp); err == nil {
			t.Fatalf("oversized request answered: %+v", resp)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		_, st := rawStream(t, tn, h)
		st.Write([]byte{0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'})
		var resp response
		if err := readMsg(st, &resp); err == nil {
			t.Fatalf("garbage answered: %+v", resp)
		}
	})
	for name, req := range map[string]request{
		"unknown type":        {T: "teleport", V: protoVersion},
		"wrong version":       {T: "connect", V: 99, ID: "123456789"},
		"short key":           {T: "register", V: protoVersion, Key: []byte{1, 2, 3}},
		"too many candidates": {T: "connect", V: protoVersion, ID: "123456789", Candidates: strings.Split(strings.Repeat("10.0.0.1:1,", 30), ",")},
		"bad candidate":       {T: "connect", V: protoVersion, ID: "123456789", Candidates: []string{"not-an-address"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, st := rawStream(t, tn, h)
			if err := writeMsg(st, req); err != nil {
				t.Fatal(err)
			}
			var resp response
			if err := readMsg(st, &resp); err != nil || resp.OK || resp.Code != CodeBadRequest {
				t.Fatalf("response %+v, err %v; want bad_request", resp, err)
			}
		})
	}
	// The server is still healthy.
	c := tn.dial(tn.socket(h), Options{})
	if _, err := c.Register(testCtx(t), newIdentity(t)); err != nil {
		t.Fatalf("server unusable after malformed requests: %v", err)
	}
}

func TestParseCandidates(t *testing.T) {
	observed := netip.MustParseAddr("203.0.113.7")
	got, err := parseCandidates([]string{
		"192.168.1.5:4000", "[fd00::1]:4000", "[::ffff:10.0.0.1]:9", "100.64.3.4:5", "203.0.113.7:4000",
		"127.0.0.1:4000", "0.0.0.0:4000", "224.0.0.1:4000",
	}, observed)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.5:4000", "[fd00::1]:4000", "10.0.0.1:9", "100.64.3.4:5", "203.0.113.7:4000"}
	if len(got) != len(want) {
		t.Fatalf("parseCandidates kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("parseCandidates kept %v, want %v", got, want)
		}
	}
	if _, err := parseCandidates([]string{"[fe80::1%eth0]:1"}, observed); err == nil {
		t.Error("zoned address accepted")
	}

	// Reflection: a peer may not name addresses that are not its own. A
	// third party's public address, a neighbour's, another IPv6 network.
	for _, victim := range []string{"8.8.8.8:53", "203.0.113.8:4000", "[2001:db8:1::1]:9", "[fe80::1]:9", "10.0.0.1:0"} {
		got, err := parseCandidates([]string{victim}, observed)
		if err != nil || len(got) != 0 {
			t.Errorf("candidate %s from a peer at %s was passed on (%v, %v)", victim, observed, got, err)
		}
	}
	// IPv6: the peer's own /64 is its own.
	got, _ = parseCandidates([]string{"[2001:db8:1:2::99]:9", "[2001:db8:1:3::99]:9"}, netip.MustParseAddr("2001:db8:1:2::1"))
	if len(got) != 1 || got[0].String() != "[2001:db8:1:2::99]:9" {
		t.Errorf("IPv6 same-/64 rule kept %v", got)
	}
	// Loopback is only a candidate for a peer that is itself on loopback.
	got, _ = parseCandidates([]string{"127.0.0.1:9"}, netip.MustParseAddr("127.0.0.1"))
	if len(got) != 1 {
		t.Errorf("loopback candidate of a loopback peer dropped")
	}
}

// The filter is applied by the server to both sides of an introduction,
// and again by the receiving side, which does not trust the server.
func TestCandidatesFilteredEndToEnd(t *testing.T) {
	tn := newTestNet(t, Limits{})
	ctx := testCtx(t)
	hostSock := tn.socket(tn.net.NAT("203.0.113.20", vnet.FullCone).Host("10.0.0.20"))
	hc := tn.dial(hostSock, Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	victim := netip.MustParseAddrPort("8.8.8.8:53")
	own := netip.MustParseAddrPort("10.0.0.20:4000")
	offers := make(chan Offer, 1)
	go hc.ServeOffers(ctx, func(o Offer) Answer {
		offers <- o
		return Answer{Local: []netip.AddrPort{victim, own}}
	})
	cc := tn.dial(tn.socket(tn.net.NAT("203.0.113.10", vnet.FullCone).Host("192.168.1.10")), Options{})
	mine := netip.MustParseAddrPort("192.168.1.10:5000")
	in, err := cc.Connect(ctx, reg.ID, []netip.AddrPort{victim, mine}, nat.TypeCone)
	if err != nil {
		t.Fatal(err)
	}
	if o := <-offers; len(o.Peer.Local) != 1 || o.Peer.Local[0] != mine {
		t.Errorf("host was handed client candidates %v, want only %s", o.Peer.Local, mine)
	}
	if len(in.Peer.Local) != 1 || in.Peer.Local[0] != own {
		t.Errorf("client was handed host candidates %v, want only %s", in.Peer.Local, own)
	}

	// A server that does not filter: the receiving side does.
	p, err := parsePeer(&peerInfo{Reflexive: "203.0.113.20:40000", Local: []string{"8.8.8.8:53", "10.0.0.20:4000", "203.0.113.20:1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Local) != 2 || p.Local[0].String() != "10.0.0.20:4000" || p.Local[1].String() != "203.0.113.20:1" {
		t.Errorf("unfiltered introduction parsed to %v", p.Local)
	}
}

// What a rendezvous server says in a failure is not trusted: a huge retry
// time is clamped, unknown codes and terminal escapes do not get through.
func TestHostileFailureReply(t *testing.T) {
	e := (&response{Code: CodeRateLimited, RetryAfterMS: 3e12, Error: "wait\x1b]0;owned\x07 \u202eforever\nremohop: all good"}).err().(*Error)
	if e.RetryAfter != MaxRetryAfter {
		t.Errorf("retry-after %s, want it clamped to %s", e.RetryAfter, MaxRetryAfter)
	}
	if strings.ContainsAny(e.Message, "\x1b\x07\n\u202e") || !strings.Contains(e.Message, "forever") {
		t.Errorf("message not cleaned: %q", e.Message)
	}
	if e := (&response{Code: CodeRateLimited, RetryAfterMS: -5}).err().(*Error); e.RetryAfter != 0 {
		t.Errorf("negative retry-after became %s", e.RetryAfter)
	}
	e = (&response{Code: "\x1b[2Jpwned", Error: strings.Repeat("A", 5000), RetryAfterMS: 3e12}).err().(*Error)
	if e.Code != CodeBadRequest || len(e.Message) > maxErrorText || e.RetryAfter != 0 {
		t.Errorf("unknown code passed on: %+v", e)
	}
	if strings.ContainsRune(e.Error(), 0x1b) {
		t.Errorf("escape in error text: %q", e.Error())
	}
}

// fakeServer is a rendezvous server that answers every request stream with
// one canned response.
func fakeServer(t *testing.T, tn *testNet, ip string, resp response) string {
	t.Helper()
	id := newIdentity(t)
	tlsConf, err := id.ServerTLS(ALPN, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: tn.net.Host(ip).MustListen(21116)}
	ln, err := tr.Listen(tlsConf, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); tr.Close() })
	go func() {
		for {
			q, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				for {
					st, err := q.AcceptStream(context.Background())
					if err != nil {
						return
					}
					var req request
					readMsg(st, &req)
					writeMsg(st, resp)
					st.Close()
				}
			}()
		}
	}()
	return ip + ":21116"
}

// The reviewer's case: one forged "rate limited" reply naming a wait of a
// century must not keep a host away from the rendezvous server.
func TestForgedRetryAfterIsClamped(t *testing.T) {
	tn := newTestNet(t, Limits{})
	addr := fakeServer(t, tn, "198.51.100.66", response{Code: CodeRateLimited, RetryAfterMS: 3e12, Error: "\x1b[31mgo away"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, tn.socket(tn.net.Host("192.0.2.1")), addr, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Register(ctx, newIdentity(t))
	e, ok := err.(*Error)
	if !ok || e.Code != CodeRateLimited {
		t.Fatalf("Register against the fake server: %v", err)
	}
	if e.RetryAfter > MaxRetryAfter || e.RetryAfter <= 0 {
		t.Fatalf("retry-after %s, want at most %s", e.RetryAfter, MaxRetryAfter)
	}
	if strings.ContainsRune(err.Error()+e.Message, 0x1b) {
		t.Fatalf("escape sequence in the error: %q / %q", err.Error(), e.Message)
	}
	// This server offers no NAT-probe service; the type is unknown, and
	// asking for it does not hang.
	start := time.Now()
	if typ := c.NATType(); typ != nat.TypeUnknown || time.Since(start) > time.Second {
		t.Fatalf("NATType = %s after %s", typ, time.Since(start))
	}
}

// Limits hold against a crowd of sources: many IPv6 /64s of one /48 share
// that network's budgets, and many networks share the global ones.
func TestLimitsAcrossManySources(t *testing.T) {
	s, err := NewServer(Config{DataDir: t.TempDir(), Limits: Limits{
		NewIDNetBurst: 3, NewIDNetEvery: time.Hour,
		NewIDGlobalBurst: 5, NewIDGlobalEvery: time.Hour,
		ConnectNetBurst: 4, ConnectNetEvery: time.Hour,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	addr := func(net, sub int) netip.Addr {
		return netip.AddrFrom16([16]byte{0x20, 0x01, 0x0d, 0xb8, byte(net >> 8), byte(net), byte(sub >> 8), byte(sub), 15: 1})
	}
	// New IDs: every /64 has its own budget of 5, but a /48 gets 3.
	granted := 0
	for sub := 0; sub < 50; sub++ {
		src, network := sourceOf(addr(1, sub))
		if ok, _ := s.newIDs.allow(src); !ok {
			t.Fatal("a fresh /64 was refused its own budget")
		}
		if ok, _ := s.newIDsNet.allow(network); ok {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("one /48 was granted %d new IDs, want 3", granted)
	}
	// ... and all networks together get 5.
	granted = 0
	for network := 100; network < 200; network++ {
		_, n := sourceOf(addr(network, 0))
		if ok, _ := s.newIDsNet.allow(n); !ok {
			t.Fatal("a fresh /48 was refused its own budget")
		}
		if ok, _ := s.newIDsAll.allow(struct{}{}); ok {
			granted++
		}
	}
	if granted != 5 {
		t.Fatalf("100 networks were granted %d new IDs, want 5", granted)
	}
	// Connects: a /48's /64s share its connect budget.
	granted = 0
	for sub := 0; sub < 50; sub++ {
		_, n := sourceOf(addr(2, sub))
		if ok, _ := s.connectsNet.allow(n); ok {
			granted++
		}
	}
	if granted != 4 {
		t.Fatalf("one /48 was granted %d connects, want 4", granted)
	}
	// IPv4 addresses are their own network.
	a, n := sourceOf(netip.MustParseAddr("::ffff:192.0.2.9"))
	if a != n || a != netip.MustParseAddr("192.0.2.9") {
		t.Fatalf("sourceOf(v4-mapped) = %s, %s", a, n)
	}
	// The default global rate for new IDs is what expiry can keep up with.
	var l Limits
	l.defaults()
	if l.NewIDGlobalEvery*time.Duration(l.MaxIDs) < l.IDExpiry {
		t.Fatalf("defaults let %d IDs be created within the expiry time of %s", l.MaxIDs, l.IDExpiry)
	}
}

// Requests for IDs that are not online draw on a global budget; once it is
// spent the server stops saying which IDs exist, but still introduces
// clients to hosts that are online.
func TestMissBudget(t *testing.T) {
	tn := newTestNet(t, Limits{MissBurst: 3, MissEvery: time.Hour})
	ctx := testCtx(t)
	hc := tn.dial(tn.socket(tn.net.Host("192.0.2.1")), Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{} })

	// Sweeping from a different address each time.
	for i := 0; i < 6; i++ {
		ip := netip.AddrFrom4([4]byte{192, 0, 2, byte(50 + i)}).String()
		c := tn.dial(tn.socket(tn.net.Host(ip)), Options{})
		_, err := c.Connect(ctx, "123456789", nil, nat.TypeUnknown)
		want := CodeUnknownID
		if i >= 3 {
			want = CodeRateLimited
		}
		if CodeOf(err) != want {
			t.Fatalf("lookup %d: %v, want %s", i+1, err, want)
		}
	}
	c := tn.dial(tn.socket(tn.net.Host("192.0.2.99")), Options{})
	if _, err := c.Connect(ctx, reg.ID, nil, nat.TypeUnknown); err != nil {
		t.Fatalf("connect to an online host with the miss budget spent: %v", err)
	}
}

// A full rate table forgets its quietest keys; it does not clear itself
// (which would lift every limit at once) and does not grow.
func TestRateTableOverflow(t *testing.T) {
	tab := newRateTable[int](2, time.Hour)
	tab.max = 1000
	// A key that is over its limit and stays active.
	tab.allow(-1)
	tab.allow(-1)
	if ok, _ := tab.allow(-1); ok {
		t.Fatal("limit not applied")
	}
	for i := 0; i < 20000; i++ {
		tab.allow(i)
		if i%50 == 0 {
			if ok, _ := tab.allow(-1); ok {
				t.Fatalf("after %d other keys the busy key's limit was forgotten", i)
			}
		}
		if n := len(tab.buckets); n > tab.max {
			t.Fatalf("table grew to %d entries", n)
		}
	}
	if n := len(tab.buckets); n < tab.max/2 {
		t.Fatalf("table shrank to %d entries: overflow clears too much", n)
	}
}

func TestStoreJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), idsFile)
	const day = 24 * time.Hour
	now := time.Unix(1_800_000_000, 0)
	open := func() *idStore {
		t.Helper()
		s, err := openStore(path, 100, 90*day)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		s.now = func() time.Time { return now }
		return s
	}
	offline := func(string) bool { return false }

	// A journal written by the first version: "key id created" lines.
	old := newIdentity(t)
	legacy := fmt.Sprintf("%s 123456789 %d\n", auth.EncodeKey(old.Public()), now.Unix())
	os.WriteFile(path, []byte(legacy), 0o600)

	s := open()
	if id, ok := s.lookup(old.Public()); !ok || id != "123456789" {
		t.Fatalf("legacy journal entry lost: %q %v", id, ok)
	}
	a, b := newIdentity(t), newIdentity(t)
	idA, isNew, err := s.assign(a.Public(), offline)
	if err != nil || !isNew {
		t.Fatalf("assign: %v %v", isNew, err)
	}
	idB, _, _ := s.assign(b.Public(), offline)
	if again, isNew, _ := s.assign(a.Public(), offline); again != idA || isNew {
		t.Fatal("second assign for the same key allocated again")
	}
	s.close()

	// A crash mid-append leaves a torn last line: it is dropped, and the
	// file is usable afterwards.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("AAAAtorn 1234")
	f.Close()
	s = open()
	if id, ok := s.lookup(a.Public()); !ok || id != idA {
		t.Fatal("ID lost after reopen")
	}
	c := newIdentity(t)
	idC, _, err := s.assign(c.Public(), offline)
	if err != nil {
		t.Fatal(err)
	}
	s.close()
	s = open()
	for key, want := range map[*auth.Identity]string{old: "123456789", a: idA, b: idB, c: idC} {
		if id, ok := s.lookup(key.Public()); !ok || id != want {
			t.Fatalf("lookup after reopen = %q, %v; want %q", id, ok, want)
		}
	}
	if !s.known(idB) || s.count() != 4 {
		t.Fatalf("store holds %d IDs", s.count())
	}

	// Expiry. 80 days on, A registers again and B is online; 20 days
	// later the legacy key and C, unseen for 100 days, are gone.
	now = now.Add(80 * day)
	if id, isNew, _ := s.assign(a.Public(), offline); id != idA || isNew {
		t.Fatal("re-registration changed the ID")
	}
	now = now.Add(20 * day)
	dropped, err := s.maintain(func(id string) bool { return id == idB })
	if err != nil || dropped != 2 {
		t.Fatalf("maintain dropped %d (%v), want 2", dropped, err)
	}
	if s.known("123456789") || s.known(idC) || !s.known(idA) || !s.known(idB) {
		t.Fatal("the wrong IDs expired")
	}
	s.close()
	// ... and stay gone, while A's and B's liveness survived the restart.
	s = open()
	if s.count() != 2 || !s.known(idA) || !s.known(idB) {
		t.Fatalf("after restart: %d IDs", s.count())
	}
	now = now.Add(60 * day)
	if dropped, _ := s.maintain(offline); dropped != 0 {
		t.Fatalf("IDs seen 80 and 60 days ago expired (%d dropped)", dropped)
	}
	// The expired key simply gets a new ID when it comes back.
	if id, isNew, err := s.assign(c.Public(), offline); err != nil || !isNew || !validID(id) {
		t.Fatalf("returning expired key: %q %v %v", id, isNew, err)
	}
	s.close()

	// Opening compacts: one line per live ID, whatever came before.
	data, _ := os.ReadFile(path)
	s = open()
	s.close()
	data, _ = os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != 3 {
		t.Fatalf("journal has %d lines for 3 IDs:\n%s", n, data)
	}

	// A full table makes room by expiring before it refuses.
	small := filepath.Join(t.TempDir(), idsFile)
	s2, err := openStore(small, 2, 90*day)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_800_000_000, 0)
	s2.now = func() time.Time { return clock }
	s2.assign(newIdentity(t).Public(), offline)
	s2.assign(newIdentity(t).Public(), offline)
	if _, _, err := s2.assign(newIdentity(t).Public(), offline); err != errStoreFull {
		t.Fatalf("third ID in a table of two: %v, want full", err)
	}
	clock = clock.Add(91 * day)
	if _, _, err := s2.assign(newIdentity(t).Public(), offline); err != nil {
		t.Fatalf("assign after the old IDs expired: %v", err)
	}
	if s2.count() != 1 {
		t.Fatalf("table holds %d IDs, want 1", s2.count())
	}
	s2.close()

	// Corruption in the middle is not silently skipped.
	os.WriteFile(path, []byte("this is not a journal line\n"), 0o600)
	if _, err := openStore(path, 100, 90*day); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestSplitAddr(t *testing.T) {
	for in, want := range map[string]string{
		"example.net":       "example.net:21116",
		"example.net:443":   "example.net:443",
		"192.0.2.1":         "192.0.2.1:21116",
		"[2001:db8::1]":     "[2001:db8::1]:21116",
		"[2001:db8::1]:999": "[2001:db8::1]:999",
	} {
		h, p, err := SplitAddr(in)
		if err != nil || net.JoinHostPort(h, p) != want {
			t.Errorf("SplitAddr(%q) = %q, %q, %v; want %s", in, h, p, err, want)
		}
	}
	for _, bad := range []string{"", ":21116", "[]"} {
		if _, _, err := SplitAddr(bad); err == nil {
			t.Errorf("SplitAddr(%q) accepted", bad)
		}
	}
	if FormatID("123456789") != "123 456 789" {
		t.Error("FormatID")
	}
}

// A relay is only set up for the client of an introduction the server made
// itself, once, and only when relaying is enabled.
func TestRelayRequests(t *testing.T) {
	tn := &testNet{t: t, net: vnet.New(), dir: t.TempDir()}
	host := tn.net.Host("198.51.100.1")
	srv, err := NewServer(Config{DataDir: tn.dir, Relay: &RelayConfig{
		Listen: func() (net.PacketConn, error) { return host.Listen(0) },
		Idle:   300 * time.Millisecond,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(sctx, host.MustListen(21116), host.MustListen(21117)) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	ctx := testCtx(t)

	hostSock := tn.socket(tn.net.Host("192.0.2.1"))
	hc := tn.dial(hostSock, Options{})
	reg, err := hc.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	relayed := make(chan netip.AddrPort, 4)
	hc.OnRelay = func(nonce []byte, relay netip.AddrPort) error {
		relayed <- relay
		return hostSock.BindRelay(ctx, relay, nonce, nat.RoleHost)
	}
	go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{} })

	clientSock := tn.socket(tn.net.Host("192.0.2.2"))
	cc := tn.dial(clientSock, Options{})
	in, err := cc.Connect(ctx, reg.ID, nil, nat.TypeUnknown)
	if err != nil || !in.RelayOffered {
		t.Fatalf("introduction: %+v, %v", in, err)
	}

	// Somebody else who learned the nonce cannot have the relay opened.
	other := tn.dial(tn.socket(tn.net.Host("192.0.2.66")), Options{})
	if _, err := other.Relay(ctx, in.Nonce); CodeOf(err) != CodeBadRequest {
		t.Fatalf("relay request by a third party: %v", err)
	}
	if _, err := cc.Relay(ctx, make([]byte, nat.NonceSize)); CodeOf(err) != CodeBadRequest {
		t.Fatalf("relay request for an unknown nonce: %v", err)
	}
	relay, err := cc.Relay(ctx, in.Nonce)
	if err != nil {
		t.Fatalf("relay request: %v", err)
	}
	if got := <-relayed; got != relay || relay.Addr() != netip.MustParseAddr("198.51.100.1") {
		t.Fatalf("client was given %s, host %s", relay, got)
	}
	if err := clientSock.BindRelay(ctx, relay, in.Nonce, nat.RoleClient); err != nil {
		t.Fatalf("client bind: %v", err)
	}
	// One relay per introduction.
	if _, err := cc.Relay(ctx, in.Nonce); CodeOf(err) != CodeBadRequest {
		t.Fatalf("second relay for one introduction: %v", err)
	}
	// An idle session frees its slot.
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.mu.Lock()
		n := srv.relays
		srv.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d relay sessions still open after the idle time", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A presence request answers what connect requests would, for many IDs at
// once, and costs as much.
func TestPresence(t *testing.T) {
	tn := newTestNet(t, Limits{ConnectBurst: 5, ConnectEvery: time.Hour, MissBurst: 100, MissEvery: time.Hour})
	ctx := testCtx(t)
	pub := tn.net.Host("192.0.2.1")
	online := tn.dial(tn.socket(pub), Options{})
	regOn, err := online.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	gone := tn.dial(tn.socket(pub), Options{})
	regOff, err := gone.Register(ctx, newIdentity(t))
	if err != nil {
		t.Fatal(err)
	}
	gone.Close()
	unknown := "100000000"
	for unknown == regOn.ID || unknown == regOff.ID {
		unknown = "100000001"
	}

	asker := tn.net.Host("192.0.2.9")
	c := tn.dial(tn.socket(asker), Options{})
	deadline := time.Now().Add(3 * time.Second)
	var got map[string]string
	for {
		got, err = c.Presence(ctx, []string{regOn.ID, regOff.ID, unknown})
		if err != nil {
			t.Fatal(err)
		}
		if got[regOff.ID] == PresenceOffline || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got[regOn.ID] != PresenceOnline || got[regOff.ID] != PresenceOffline || got[unknown] != PresenceUnknown {
		t.Fatalf("presence %v", got)
	}
	// Each ID was charged to the connect budget (5): what is left is spent
	// by the next request, and the rest is rate limited, per ID.
	if got, err = c.Presence(ctx, []string{regOn.ID, regOn.ID}); err != nil || got[regOn.ID] != PresenceOnline {
		t.Fatalf("within budget: %v %v", got, err)
	}
	limited := 0
	_, st := rawStream(t, tn, asker)
	writeMsg(st, request{T: "presence", V: protoVersion, IDs: []string{regOn.ID, regOn.ID, regOn.ID}})
	var r response
	if err := readMsg(st, &r); err != nil || !r.OK || len(r.Presence) != 3 {
		t.Fatalf("raw presence: %+v %v", r, err)
	}
	for _, p := range r.Presence {
		if p == PresenceLimited {
			limited++
		}
	}
	if limited != 3 {
		t.Fatalf("after the connect budget was spent: %v", r.Presence)
	}
	// Connects from that source are limited too: the budget is shared.
	if _, err := c.Connect(ctx, regOn.ID, nil, nat.TypeUnknown); CodeOf(err) != CodeRateLimited {
		t.Fatalf("connect after presence spent the budget: %v", err)
	}

	// Malformed requests.
	other := tn.net.Host("192.0.2.10")
	for name, ids := range map[string][]string{
		"none":      nil,
		"too many":  strings.Split(strings.Repeat("123456789,", MaxPresenceIDs+1), ",")[:MaxPresenceIDs+1],
		"malformed": {"12345"},
	} {
		_, st := rawStream(t, tn, other)
		writeMsg(st, request{T: "presence", V: protoVersion, IDs: ids})
		var r response
		if err := readMsg(st, &r); err != nil || r.OK || r.Code != CodeBadRequest {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
	if _, err := c.Presence(ctx, make([]string, MaxPresenceIDs+1)); err == nil {
		t.Error("client sent an oversized presence request")
	}
}
