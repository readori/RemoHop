package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	testSrc = netip.MustParseAddr("203.0.113.7")
	bg      = context.Background()
)

// fakeClock is a manually advanced clock for limiter tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

// testBinding returns the client's and the host's view of one honest
// connection: same exporter, each other's keys.
func testBinding(t *testing.T) (cb, hb Binding) {
	t.Helper()
	c, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	ekm := make([]byte, 32)
	rand.Read(ekm)
	b := Binding{Exporter: ekm, ClientKey: c.Public(), HostKey: h.Public()}
	return b, b
}

// exchange runs one handshake over an in-memory pipe.
func exchange(t *testing.T, srv *Server, hb Binding, src netip.Addr, client func(rw io.ReadWriter) error) (res *Result, serr, cerr error) {
	t.Helper()
	cc, hc := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, serr = srv.Handshake(context.Background(), hc, hb, src)
		hc.Close()
	}()
	cerr = client(cc)
	cc.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake did not return")
	}
	return res, serr, cerr
}

func passwordServer(pw string) *Server {
	return &Server{Password: func() string { return pw }, Limiter: NewLimiter(LimiterConfig{})}
}

func TestPasswordSuccess(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	srv.HostID = func() string { return "123456789" }
	res, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		// What the user typed: grouped, with stray whitespace.
		return ClientHandshake(rw, cb, PasswordCredential(" abcd-efgh\n"), "123456789")
	})
	if serr != nil || cerr != nil {
		t.Fatalf("handshake failed: server=%v client=%v", serr, cerr)
	}
	if res.Kind != KindPassword || !res.ClientKey.Equal(cb.ClientKey) {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestWrongPassword(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	_, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		return ClientHandshake(rw, cb, PasswordCredential("abcdefgx"), "")
	})
	if CodeOf(cerr) != CodeAuthFailed {
		t.Errorf("client error = %v, want auth_failed", cerr)
	}
	if CodeOf(serr) != CodeAuthFailed {
		t.Errorf("server error = %v, want auth_failed", serr)
	}
}

// relay copies handshake messages between two legs without touching them,
// as a man in the middle that terminated TLS on both sides would.
func relay(a, b net.Conn) {
	go func() { io.Copy(a, b); a.Close() }()
	go func() { io.Copy(b, a); b.Close() }()
}

// mitm runs client and server on separate legs joined by a verbatim relay,
// each with its own view of the connection.
func mitm(t *testing.T, srv *Server, cb, hb Binding, cred Credential) (serr, cerr error) {
	t.Helper()
	c1, m1 := net.Pipe() // client <-> attacker
	m2, h2 := net.Pipe() // attacker <-> host
	relay(m1, m2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, serr = srv.Handshake(context.Background(), h2, hb, testSrc)
		h2.Close()
	}()
	cerr = ClientHandshake(c1, cb, cred, "")
	c1.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake did not return")
	}
	return serr, cerr
}

func TestMITMDifferentExporters(t *testing.T) {
	cb, hb := testBinding(t)
	// The attacker completed two TLS handshakes, so each leg has its own
	// exporter even if it managed to show both sides the expected keys.
	hb.Exporter = bytes.Repeat([]byte{0x42}, 32)
	srv := passwordServer("abcdefgh")
	serr, cerr := mitm(t, srv, cb, hb, PasswordCredential("abcdefgh"))
	if CodeOf(cerr) != CodeAuthFailed {
		t.Errorf("client error = %v, want auth_failed", cerr)
	}
	if CodeOf(serr) != CodeAuthFailed {
		t.Errorf("server error = %v, want auth_failed", serr)
	}
}

func TestMITMDifferentKeys(t *testing.T) {
	attacker, _ := NewIdentity()
	for _, tc := range []struct {
		name   string
		tamper func(cb, hb *Binding)
	}{
		{"client sees attacker as host", func(cb, hb *Binding) { cb.HostKey = attacker.Public() }},
		{"host sees attacker as client", func(cb, hb *Binding) { hb.ClientKey = attacker.Public() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb, hb := testBinding(t)
			tc.tamper(&cb, &hb)
			serr, cerr := mitm(t, passwordServer("abcdefgh"), cb, hb, PasswordCredential("abcdefgh"))
			if CodeOf(cerr) != CodeAuthFailed || CodeOf(serr) != CodeAuthFailed {
				t.Errorf("client=%v server=%v, want auth_failed on both", cerr, serr)
			}
		})
	}
}

// An impostor host must not receive anything it can test password guesses
// against: the client stops before sending its confirmation.
func TestImpostorHostGetsNoConfirm(t *testing.T) {
	cb, hb := testBinding(t)
	cc, hc := net.Pipe()
	got := make(chan int, 1)
	go func() {
		// The impostor does not know the password and plays the host's
		// part with a guess.
		var h hello
		if err := readMsg(hc, &h); err != nil {
			got <- -1
			return
		}
		ci, ad := h.context(hb)
		msgB, key, err := cpaceExchange("wrongguess", ci, h.MsgA)
		if err != nil {
			got <- -1
			return
		}
		writeMsg(hc, reply{V: Version, OK: true, MsgB: msgB, Confirm: confirmTag(key, "host", ad, h.MsgA, msgB)})
		// Anything the client sends now would let the impostor check
		// its guess.
		hc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		n, _ := hc.Read(make([]byte, 1))
		got <- n
	}()
	err := ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), "")
	if CodeOf(err) != CodeAuthFailed {
		t.Fatalf("client error = %v, want auth_failed", err)
	}
	if n := <-got; n != 0 {
		t.Fatal("client sent data after a failed host confirmation")
	}
	cc.Close()
}

func TestTargetMismatch(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	srv.HostID = func() string { return "123456789" }
	_, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		return ClientHandshake(rw, cb, PasswordCredential("abcdefgh"), "987654321")
	})
	if CodeOf(cerr) != CodeWrongHost {
		t.Fatalf("client error = %v, want wrong_host", cerr)
	}
	// A wrong_host rejection happens before the PAKE and is not a guess.
	for i := 0; i < 10; i++ {
		_, _, cerr = exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, PasswordCredential("abcdefgh"), "987654321")
		})
	}
	if CodeOf(cerr) != CodeWrongHost {
		t.Fatalf("after repeats: %v, want wrong_host (not a lockout)", cerr)
	}
}

func TestTokenAuth(t *testing.T) {
	cb, hb := testBinding(t)
	store := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	tok, err := store.Mint("laptop", cb.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	// No password at all: token logins must not depend on it.
	srv := &Server{Tokens: store}

	res, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		return ClientHandshake(rw, cb, TokenCredential(tok.ID, tok.Secret), "")
	})
	if serr != nil || cerr != nil {
		t.Fatalf("token handshake failed: server=%v client=%v", serr, cerr)
	}
	if res.Kind != KindToken || res.TokenID != tok.ID || res.TokenName != "laptop" {
		t.Fatalf("unexpected result %+v", res)
	}

	t.Run("wrong secret", func(t *testing.T) {
		bad := bytes.Repeat([]byte{1}, TokenSize)
		_, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, TokenCredential(tok.ID, bad), "")
		})
		if CodeOf(cerr) != CodeAuthFailed || CodeOf(serr) != CodeAuthFailed {
			t.Fatalf("client=%v server=%v, want auth_failed", cerr, serr)
		}
	})
	t.Run("other client identity", func(t *testing.T) {
		thief, _ := NewIdentity()
		cb2, hb2 := cb, hb
		cb2.ClientKey, hb2.ClientKey = thief.Public(), thief.Public()
		_, _, cerr := exchange(t, srv, hb2, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb2, TokenCredential(tok.ID, tok.Secret), "")
		})
		if CodeOf(cerr) != CodeUnknownToken {
			t.Fatalf("client error = %v, want unknown_token", cerr)
		}
	})
	t.Run("password login disabled", func(t *testing.T) {
		_, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, PasswordCredential("abcdefgh"), "")
		})
		if CodeOf(cerr) != CodeNoPassword {
			t.Fatalf("client error = %v, want password_disabled", cerr)
		}
	})
	t.Run("revoked", func(t *testing.T) {
		if n, err := store.Revoke("laptop"); err != nil || n != 1 {
			t.Fatalf("Revoke = %d, %v", n, err)
		}
		_, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, TokenCredential(tok.ID, tok.Secret), "")
		})
		if CodeOf(cerr) != CodeUnknownToken {
			t.Fatalf("client error = %v, want unknown_token", cerr)
		}
	})
}

func TestLockout(t *testing.T) {
	clock := newClock()
	cb, hb := testBinding(t)
	srv := &Server{
		Password: func() string { return "abcdefgh" },
		Limiter:  NewLimiter(LimiterConfig{Now: clock.now}),
	}
	try := func(pw string, src netip.Addr) error {
		_, _, cerr := exchange(t, srv, hb, src, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, PasswordCredential(pw), "")
		})
		return cerr
	}

	// Three wrong guesses are answered; the third arms the lockout.
	for i := 0; i < 3; i++ {
		if err := try("wrongpass", testSrc); CodeOf(err) != CodeAuthFailed {
			t.Fatalf("guess %d: %v, want auth_failed", i+1, err)
		}
	}
	// Locked out: even the right password is refused, before any PAKE.
	err := try("abcdefgh", testSrc)
	e, _ := err.(*Error)
	if e == nil || e.Code != CodeLockedOut || e.RetryAfter <= 0 || e.RetryAfter > 10*time.Second {
		t.Fatalf("during lockout: %v, want locked_out with retry ≤ 10s", err)
	}
	// Another source is unaffected.
	if err := try("abcdefgh", netip.MustParseAddr("198.51.100.1")); err != nil {
		t.Fatalf("other source: %v", err)
	}
	// After the lock, one more wrong guess doubles it.
	clock.advance(11 * time.Second)
	if err := try("wrongpass", testSrc); CodeOf(err) != CodeAuthFailed {
		t.Fatalf("after first lock: %v, want auth_failed", err)
	}
	clock.advance(11 * time.Second)
	if err := try("abcdefgh", testSrc); CodeOf(err) != CodeLockedOut {
		t.Fatalf("second lock should last 20s: %v", err)
	}
	clock.advance(10 * time.Second)
	// Success clears the history: three fresh typos are allowed again.
	if err := try("abcdefgh", testSrc); err != nil {
		t.Fatalf("after lock expiry: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := try("wrongpass", testSrc); CodeOf(err) != CodeAuthFailed {
			t.Fatalf("post-success guess %d: %v, want auth_failed", i+1, err)
		}
	}
}

func TestLockoutIPv6Prefix(t *testing.T) {
	clock := newClock()
	l := NewLimiter(LimiterConfig{Now: clock.now})
	// Rotating through addresses of one /64 must not reset the count.
	for i := 0; i < 3; i++ {
		a, err := l.Begin(bg, netip.AddrFrom16([16]byte{0x20, 0x01, 0xd, 0xb8, 15: byte(i + 1)}))
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		a.Fail()
	}
	if _, err := l.Begin(bg, netip.MustParseAddr("2001:db8::ffff")); CodeOf(err) != CodeLockedOut {
		t.Fatalf("same /64: %v, want locked_out", err)
	}
	if _, err := l.Begin(bg, netip.MustParseAddr("2001:db8:0:1::1")); err != nil {
		t.Fatalf("different /64: %v", err)
	}
}

func TestGlobalBudget(t *testing.T) {
	clock := newClock()
	l := NewLimiter(LimiterConfig{Now: clock.now, GlobalBurst: 5, GlobalRefill: 5 * time.Second})
	src := func(i int) netip.Addr { return netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}) }
	// A botnet: every guess from a new address.
	for i := 0; i < 5; i++ {
		a, err := l.Begin(bg, src(i))
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		a.Fail()
	}
	if _, err := l.Begin(bg, src(100)); CodeOf(err) != CodeLockedOut {
		t.Fatalf("burst spent: %v, want locked_out", err)
	}
	// From then on: one guess per refill period, no matter how many sources.
	granted := 0
	for i := 0; i < 600; i++ {
		clock.advance(100 * time.Millisecond)
		if a, err := l.Begin(bg, src(200+i)); err == nil {
			granted++
			a.Fail()
		}
	}
	if granted < 11 || granted > 13 {
		t.Fatalf("granted %d attempts in 60s, want about 12", granted)
	}
	// Successful logins do not drain the budget.
	clock.advance(time.Minute)
	for i := 0; i < 50; i++ {
		a, err := l.Begin(bg, src(1000+i))
		if err != nil {
			t.Fatalf("successful login %d refused: %v", i, err)
		}
		a.Success()
	}
}

// Parallel connections cannot multiply the number of guesses: a source may
// have only as many attempts open as it has failures left, and the rest
// wait for those to finish.
func TestParallelAttemptsBounded(t *testing.T) {
	l := NewLimiter(LimiterConfig{})
	var open []*Attempt
	for i := 0; i < 3; i++ {
		a, err := l.Begin(bg, testSrc)
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		open = append(open, a)
	}
	// A fourth must wait, and gives up with its context.
	short, cancel := context.WithTimeout(bg, 100*time.Millisecond)
	defer cancel()
	if _, err := l.Begin(short, testSrc); CodeOf(err) != CodeLockedOut {
		t.Fatalf("fourth parallel attempt: %v, want it to wait and be refused", err)
	}
	// Twenty more wait; when the open ones fail, all of them are refused:
	// the source made its three guesses.
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			_, err := l.Begin(bg, testSrc)
			results <- err
		}()
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-results:
		t.Fatalf("a waiting attempt was decided early: %v", err)
	default:
	}
	for _, a := range open {
		a.Fail()
	}
	for i := 0; i < 20; i++ {
		select {
		case err := <-results:
			if CodeOf(err) != CodeLockedOut {
				t.Fatalf("waiter %d: %v, want locked_out", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("waiting attempts were not released")
		}
	}
}

// A legitimate client that opens many sessions at once is not locked out:
// its logins queue behind each other and all succeed.
func TestParallelSuccessfulLogins(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	const n = 24
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			cc, hc := net.Pipe()
			defer cc.Close()
			go func() {
				srv.Handshake(bg, hc, hb, testSrc)
				hc.Close()
			}()
			errs <- ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), "")
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("parallel login %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("parallel logins did not finish")
		}
	}
	// And the source's record is clean afterwards.
	for i := 0; i < 3; i++ {
		a, err := srv.Limiter.Begin(bg, testSrc)
		if err != nil {
			t.Fatalf("attempt %d after parallel logins: %v", i+1, err)
		}
		defer a.Fail()
	}
}

// A client that learns its guess was wrong and hangs up without answering
// is still charged.
func TestAbandonedAttemptCounts(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	for i := 0; i < 3; i++ {
		_, serr, _ := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			h := hello{V: Version, Kind: KindPassword}
			ci, _ := h.context(cb)
			msgA, _, err := cpaceStart("wrongpass", ci)
			if err != nil {
				return err
			}
			h.MsgA = msgA
			if err := writeMsg(rw, h); err != nil {
				return err
			}
			var rep reply
			return readMsg(rw, &rep) // then vanish
		})
		if CodeOf(serr) != CodeAuthFailed {
			t.Fatalf("server error = %v, want auth_failed", serr)
		}
	}
	_, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		return ClientHandshake(rw, cb, PasswordCredential("abcdefgh"), "")
	})
	if CodeOf(cerr) != CodeLockedOut {
		t.Fatalf("after abandoned attempts: %v, want locked_out", cerr)
	}
}

func TestTokenLoginBypassesLockout(t *testing.T) {
	cb, hb := testBinding(t)
	store := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	tok, err := store.Mint("agent", cb.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := passwordServer("abcdefgh")
	srv.Tokens = store
	for i := 0; i < 5; i++ {
		exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, PasswordCredential("wrongpass"), "")
		})
	}
	_, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		return ClientHandshake(rw, cb, TokenCredential(tok.ID, tok.Secret), "")
	})
	if serr != nil || cerr != nil {
		t.Fatalf("token login during lockout: server=%v client=%v", serr, cerr)
	}
}

func frame(body []byte) []byte {
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body)))
	copy(out[4:], body)
	return out
}

func TestMalformedHello(t *testing.T) {
	cb, hb := testBinding(t)
	_ = cb
	huge := make([]byte, 4)
	binary.BigEndian.PutUint32(huge, 1<<30)
	mustJSON := func(v any) []byte {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return frame(b)
	}
	cases := []struct {
		name string
		raw  []byte
		code Code // expected code in the host's reply; "" if none is sent
	}{
		{"empty stream", nil, ""},
		{"truncated header", []byte{0, 0}, ""},
		{"truncated body", frame([]byte(`{"v":1`))[:8], ""},
		{"oversized length", huge, CodeProtocol},
		{"not json", frame([]byte("hello there")), CodeProtocol},
		{"json array", frame([]byte("[1,2,3]")), CodeProtocol},
		{"wrong version", mustJSON(hello{V: 99, Kind: KindPassword, MsgA: make([]byte, 48)}), CodeBadVersion},
		{"unknown credential", mustJSON(hello{V: 1, Kind: "kerberos", MsgA: make([]byte, 48)}), CodeProtocol},
		{"short msg_a", mustJSON(hello{V: 1, Kind: KindPassword, MsgA: []byte{1, 2, 3}}), CodeProtocol},
		{"invalid group element", mustJSON(hello{V: 1, Kind: KindPassword, MsgA: bytes.Repeat([]byte{0xff}, 48)}), CodeProtocol},
		{"identity element", mustJSON(hello{V: 1, Kind: KindPassword, MsgA: make([]byte, 48)}), CodeProtocol},
		{"long target", mustJSON(hello{V: 1, Kind: KindPassword, Target: string(make([]byte, 500)), MsgA: make([]byte, 48)}), CodeProtocol},
		{"token id on password", mustJSON(hello{V: 1, Kind: KindPassword, TokenID: "aa", MsgA: make([]byte, 48)}), CodeProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := passwordServer("abcdefgh")
			var rep reply
			var readErr error
			res, serr, _ := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
				go func() {
					rw.Write(tc.raw)
					if tc.code == "" {
						rw.(net.Conn).Close()
					}
				}()
				readErr = readMsg(rw, &rep)
				return nil
			})
			if res != nil || serr == nil {
				t.Fatalf("malformed hello accepted: res=%v err=%v", res, serr)
			}
			if tc.code == "" {
				return
			}
			if readErr != nil || rep.OK || rep.Code != tc.code {
				t.Fatalf("reply = %+v (read err %v), want code %s", rep, readErr, tc.code)
			}
		})
	}
}

func TestMalformedReplyAndFinish(t *testing.T) {
	cb, hb := testBinding(t)

	t.Run("oversized host reply", func(t *testing.T) {
		cc, hc := net.Pipe()
		defer cc.Close()
		go func() {
			var h hello
			readMsg(hc, &h)
			big := make([]byte, 4)
			binary.BigEndian.PutUint32(big, 1<<24)
			hc.Write(big)
			hc.Close()
		}()
		if err := ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), ""); CodeOf(err) != CodeProtocol {
			t.Fatalf("client error = %v, want protocol", err)
		}
	})
	t.Run("bad msg_b", func(t *testing.T) {
		cc, hc := net.Pipe()
		defer cc.Close()
		go func() {
			var h hello
			readMsg(hc, &h)
			writeMsg(hc, reply{V: 1, OK: true, MsgB: []byte{1}, Confirm: make([]byte, 32)})
			hc.Close()
		}()
		if err := ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), ""); CodeOf(err) != CodeProtocol {
			t.Fatalf("client error = %v, want protocol", err)
		}
	})
	t.Run("garbage finish", func(t *testing.T) {
		srv := passwordServer("abcdefgh")
		res, serr, _ := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			h := hello{V: Version, Kind: KindPassword}
			ci, _ := h.context(cb)
			h.MsgA, _, _ = cpaceStart("abcdefgh", ci)
			writeMsg(rw, h)
			var rep reply
			readMsg(rw, &rep)
			return writeMsg(rw, finish{Confirm: []byte("nope")})
		})
		if res != nil || CodeOf(serr) != CodeAuthFailed {
			t.Fatalf("res=%v err=%v, want auth_failed", res, serr)
		}
	})
}

// Random bytes must never panic the host or be accepted.
func TestRandomInput(t *testing.T) {
	_, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	srv.Limiter = NewLimiter(LimiterConfig{GlobalBurst: 1 << 20, FreeFailures: 1 << 20})
	for i := 0; i < 200; i++ {
		raw := make([]byte, 4+i*3)
		rand.Read(raw)
		if i%2 == 0 {
			// Make the length prefix plausible so the body gets parsed.
			binary.BigEndian.PutUint32(raw, uint32(len(raw))-4)
		}
		res, serr, _ := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			go func() {
				rw.Write(raw)
				rw.(net.Conn).Close()
			}()
			io.Copy(io.Discard, rw)
			return nil
		})
		if res != nil || serr == nil {
			t.Fatalf("random input %d accepted", i)
		}
	}
}

// fakeHost plays a host that answers every hello with one canned reply.
func fakeHost(t *testing.T, rep reply) (err error) {
	t.Helper()
	cb, _ := testBinding(t)
	cc, hc := net.Pipe()
	defer cc.Close()
	go func() {
		var h hello
		readMsg(hc, &h)
		writeMsg(hc, rep)
		hc.Close()
	}()
	return ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), "")
}

// Nothing a host says before it has authenticated is trusted: escape
// sequences in its refusal never reach the caller, codes this version does
// not know are not echoed, and a retry time is clamped.
func TestHostileRefusal(t *testing.T) {
	const esc = "\x1b]0;owned\x07\x1b[2J\U0000202egnp.exe\u009b31m\nremohop: logged in"
	clean := func(t *testing.T, err error) *Error {
		t.Helper()
		e, ok := err.(*Error)
		if !ok {
			t.Fatalf("error %v (%T) is not an *Error", err, err)
		}
		for _, text := range []string{e.Error(), e.Detail, string(e.Code)} {
			if strings.ContainsAny(text, "\x1b\x07\n\U0000202e\u009b") {
				t.Fatalf("unsafe characters reached the caller: %q", text)
			}
		}
		return e
	}

	t.Run("unknown code with detail", func(t *testing.T) {
		e := clean(t, fakeHost(t, reply{V: 1, Code: Code("x\x1b[31m"), Detail: esc, RetryAfterMS: 3e12}))
		if e.Code != CodeProtocol || strings.Contains(e.Error(), "owned") || e.RetryAfter != 0 {
			t.Fatalf("unknown refusal passed on: %+v / %q", e, e.Error())
		}
	})
	t.Run("detail on a known code that carries none", func(t *testing.T) {
		e := clean(t, fakeHost(t, reply{V: 1, Code: CodeProtocol, Detail: esc}))
		if e.Detail != "" {
			t.Fatalf("detail kept: %q", e.Detail)
		}
	})
	t.Run("version detail is cleaned and bounded", func(t *testing.T) {
		e := clean(t, fakeHost(t, reply{V: 1, Code: CodeBadVersion, Detail: esc + strings.Repeat("A", 1000)}))
		if e.Code != CodeBadVersion || len(e.Detail) > maxDetailLen || !strings.Contains(e.Detail, "owned") {
			t.Fatalf("detail = %q", e.Detail)
		}
	})
	t.Run("retry time is clamped", func(t *testing.T) {
		e := clean(t, fakeHost(t, reply{V: 1, Code: CodeLockedOut, RetryAfterMS: 3e12}))
		if e.Code != CodeLockedOut || e.RetryAfter != MaxRetryAfter {
			t.Fatalf("retry-after %s, want %s", e.RetryAfter, MaxRetryAfter)
		}
		e = clean(t, fakeHost(t, reply{V: 1, Code: CodeLockedOut, RetryAfterMS: -1 << 62}))
		if e.RetryAfter != 0 {
			t.Fatalf("negative retry-after became %s", e.RetryAfter)
		}
	})
	t.Run("ok:false without a code", func(t *testing.T) {
		if e := clean(t, fakeHost(t, reply{V: 1})); e.Code != CodeProtocol {
			t.Fatalf("code %q", e.Code)
		}
	})
}

func TestCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                                   "plain text",
		"  spaced\tout\r\n":                            "spaced out",
		"\x1b[31mred\x1b[0m":                           "[31mred[0m",
		"\x1b]52;c;ZXZpbA==\x07":                       "]52;c;ZXZpbA==",
		"c1\u009b31mcsi \u009d0;title\u009c":           "c131mcsi 0;title",
		"bidi \U0000202egnp.exe \U00002066x\U00002069": "bidi gnp.exe x",
		"zero\U0000200bwidth\U0000feff":                "zerowidth",
		"line\U00002028sep\U00002029arators":           "line sep arators",
		"bad \xff\xfe utf8":                            "bad  utf8",
		"名前 ok":                                        "名前 ok",
	} {
		if got := CleanText(in, 100); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
	// Truncation never splits a character.
	if got := CleanText(strings.Repeat("é", 50), 9); got != "éééé" {
		t.Errorf("truncated to %q", got)
	}
	if got := CleanTokenName("\U0000202e\u009b2Jlaptop\x00"); got != "2Jlaptop" {
		t.Errorf("CleanTokenName = %q", got)
	}
}

// The handshake can be driven in two steps, with the caller deciding in
// between what the hello means for its bookkeeping.
func TestReadHelloThenComplete(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	cc, hc := net.Pipe()
	defer cc.Close()
	type out struct {
		kind Kind
		res  *Result
		err  error
	}
	done := make(chan out, 1)
	go func() {
		defer hc.Close()
		h, err := srv.ReadHello(hc)
		if err != nil {
			done <- out{err: err}
			return
		}
		res, err := srv.Complete(bg, hc, h, hb, testSrc, nil)
		done <- out{h.Kind(), res, err}
	}()
	if err := ClientHandshake(cc, cb, PasswordCredential("abcdefgh"), ""); err != nil {
		t.Fatal(err)
	}
	o := <-done
	if o.err != nil || o.kind != KindPassword || o.res == nil {
		t.Fatalf("two-step handshake: %+v", o)
	}
}

// A host with two passwords accepts either, and a wrong guess is still one
// failed attempt, not two.
func TestTwoPasswords(t *testing.T) {
	cb, hb := testBinding(t)
	clock := newClock()
	srv := &Server{
		Password:    func() string { return "onetimeaa" },
		AltPassword: func() string { return "fixedpassword" },
		Limiter:     NewLimiter(LimiterConfig{Now: clock.now}),
	}
	try := func(pw string) (*Result, error) {
		res, serr, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, PasswordCredential(pw), "")
		})
		if (serr == nil) != (cerr == nil) {
			t.Fatalf("ends disagree: server %v, client %v", serr, cerr)
		}
		return res, cerr
	}
	if res, err := try("onetimeaa"); err != nil || res.AltPassword {
		t.Fatalf("primary password: %+v %v", res, err)
	}
	if res, err := try("fixedpassword"); err != nil || !res.AltPassword {
		t.Fatalf("alternative password: %+v %v", res, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := try("neitherofthem"); CodeOf(err) != CodeAuthFailed {
			t.Fatalf("wrong guess %d: %v", i+1, err)
		}
	}
	if _, err := try("onetimeaa"); CodeOf(err) != CodeLockedOut {
		t.Fatalf("after three wrong guesses: %v, want locked_out (one failure per guess)", err)
	}
	// A client that claims the alternative but confirms for the primary
	// (or the reverse) gets nowhere.
	clock.advance(time.Hour)
	_, serr, _ := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
		h := hello{V: Version, Kind: KindPassword}
		ci, ad := h.context(cb)
		msgA, state, _ := cpaceStart("onetimeaa", ci)
		h.MsgA = msgA
		writeMsg(rw, h)
		var rep reply
		readMsg(rw, &rep)
		key, _ := state.Finish(rep.MsgB)
		return writeMsg(rw, finish{Confirm: confirmTag(key, "client", ad, msgA, rep.MsgB), Alt: true})
	})
	if CodeOf(serr) != CodeAuthFailed {
		t.Fatalf("confirm for the wrong answer accepted: %v", serr)
	}
}

func TestKeyLoginHandshake(t *testing.T) {
	cb, hb := testBinding(t)
	store := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	srv := &Server{Tokens: store, Password: func() string { return "abcdefgh" }}
	try := func() (*Result, error) {
		res, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, KeyCredential(), "")
		})
		return res, cerr
	}
	if _, err := try(); CodeOf(err) != CodeUnknownKey {
		t.Fatalf("unauthorised key: %v", err)
	}
	// A pairing token for the key is not a key authorisation.
	if _, err := store.Mint("paired", cb.ClientKey); err != nil {
		t.Fatal(err)
	}
	if _, err := try(); CodeOf(err) != CodeUnknownKey {
		t.Fatalf("paired but not authorised: %v", err)
	}
	tok, err := store.Authorize("alice", cb.ClientKey)
	if err != nil {
		t.Fatal(err)
	}
	res, err := try()
	if err != nil || res.Kind != KindKey || res.TokenName != "alice" || res.TokenID != tok.ID {
		t.Fatalf("key login: %+v %v", res, err)
	}
	// The authorisation entry cannot be used as a token, and both kinds
	// of entry coexist for one key.
	if _, ok, _ := store.Lookup(tok.ID); ok {
		t.Fatal("a key authorisation is usable as a pairing token")
	}
	if toks, _ := store.List(); len(toks) != 2 {
		t.Fatalf("%d entries, want the pairing and the key", len(toks))
	}
	// Unknown keys are refused before anything is asked of the limiter.
	other, _ := NewIdentity()
	hb2 := hb
	hb2.ClientKey = other.Public()
	for i := 0; i < 10; i++ {
		_, serr, _ := exchange(t, srv, hb2, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, KeyCredential(), "")
		})
		if CodeOf(serr) != CodeUnknownKey {
			t.Fatalf("attempt %d: %v", i, serr)
		}
	}
	if line := FormatKeyLine(cb.ClientKey, "my\x1b[31m laptop"); strings.ContainsRune(line, 0x1b) {
		t.Fatalf("key line %q", line)
	} else if key, label, err := ParseKeyLine(line); err != nil || !key.Equal(cb.ClientKey) || label != "my[31m laptop" {
		t.Fatalf("ParseKeyLine: %v %q %v", key, label, err)
	}
}

func TestApprovalInHandshake(t *testing.T) {
	cb, hb := testBinding(t)
	srv := passwordServer("abcdefgh")
	run := func(approve Approver) (waits int, serr, cerr error) {
		cc, hc := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer hc.Close()
			h, err := srv.ReadHello(hc)
			if err != nil {
				serr = err
				return
			}
			_, serr = srv.Complete(bg, hc, h, hb, testSrc, approve)
		}()
		cerr = ClientHandshakeWith(cc, cb, PasswordCredential("abcdefgh"), "", ClientOptions{OnWait: func() { waits++ }})
		cc.Close()
		<-done
		return waits, serr, cerr
	}
	if waits, serr, cerr := run(func(*Result, func() error) error { return nil }); waits != 0 || serr != nil || cerr != nil {
		t.Fatalf("no approval needed: %d %v %v", waits, serr, cerr)
	}
	if waits, serr, cerr := run(func(_ *Result, announce func() error) error { return announce() }); waits != 1 || serr != nil || cerr != nil {
		t.Fatalf("approved: %d %v %v", waits, serr, cerr)
	}
	waits, serr, cerr := run(func(_ *Result, announce func() error) error {
		announce()
		return errors.New("no")
	})
	if waits != 1 || CodeOf(serr) != CodeDenied || CodeOf(cerr) != CodeDenied {
		t.Fatalf("denied: %d %v %v", waits, serr, cerr)
	}
	// A denial is not a wrong password: no lockout follows.
	for i := 0; i < 5; i++ {
		run(func(*Result, func() error) error { return errors.New("no") })
	}
	if _, serr, cerr := run(nil); serr != nil || cerr != nil {
		t.Fatalf("login after denials: %v %v", serr, cerr)
	}
}

// A host that hands out its password with a generation learns which
// generation a login proved (none for the alternative password), and its
// approver's refusal reaches the client with the approver's code. That is
// how a host spends a one-time password atomically before the welcome.
func TestPasswordGenerationAndApproverCode(t *testing.T) {
	cb, hb := testBinding(t)
	gen := uint64(7)
	srv := &Server{
		PasswordGen: func() (string, uint64) { return "onetimeaa", gen },
		AltPassword: func() string { return "fixedpassword" },
		Limiter:     NewLimiter(LimiterConfig{}),
	}
	try := func(pw string, approve Approver) (*Result, error, error) {
		cc, hc := net.Pipe()
		type out struct {
			res *Result
			err error
		}
		done := make(chan out, 1)
		go func() {
			hello, err := srv.ReadHello(hc)
			if err != nil {
				done <- out{nil, err}
				hc.Close()
				return
			}
			res, err := srv.Complete(bg, hc, hello, hb, testSrc, approve)
			done <- out{res, err}
			hc.Close()
		}()
		cerr := ClientHandshake(cc, cb, PasswordCredential(pw), "")
		cc.Close()
		o := <-done
		return o.res, o.err, cerr
	}
	res, serr, cerr := try("onetimeaa", nil)
	if serr != nil || cerr != nil || res.PasswordGen != 7 || res.AltPassword {
		t.Fatalf("primary: %+v %v %v", res, serr, cerr)
	}
	res, serr, cerr = try("fixedpassword", nil)
	if serr != nil || cerr != nil || res.PasswordGen != 0 || !res.AltPassword {
		t.Fatalf("alternative: %+v %v %v", res, serr, cerr)
	}
	// The approver sees the generation and refuses with its own code.
	var seen uint64
	_, serr, cerr = try("onetimeaa", func(res *Result, _ func() error) error {
		seen = res.PasswordGen
		return &Error{Code: CodeAuthFailed}
	})
	if seen != 7 || CodeOf(serr) != CodeAuthFailed || CodeOf(cerr) != CodeAuthFailed {
		t.Fatalf("spent at the approver: seen %d, server %v, client %v", seen, serr, cerr)
	}
	// Any other error is a denial, as before.
	_, _, cerr = try("onetimeaa", func(*Result, func() error) error { return errors.New("no") })
	if CodeOf(cerr) != CodeDenied {
		t.Fatalf("plain approver error: %v", cerr)
	}
}
