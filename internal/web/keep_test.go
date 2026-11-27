package web

import (
	"context"
	"errors"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
)

type connView struct {
	Key     string    `json:"key"`
	State   string    `json:"state"`
	Error   *apiError `json:"error"`
	Retries int       `json:"retries"`
}

func (c *client) connsView() []connView {
	c.t.Helper()
	var j struct {
		Conns []connView `json:"conns"`
	}
	if code := c.jsonCall("GET", "api/v1/connections", nil, &j); code != 200 {
		c.t.Fatalf("connections: %d", code)
	}
	return j.Conns
}

func (c *client) waitState(key, state string) connView {
	c.t.Helper()
	var last []connView
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		last = c.connsView()
		for _, v := range last {
			if v.Key == key && v.State == state {
				return v
			}
		}
	}
	c.t.Fatalf("connection %s never reached %q: %+v", key, state, last)
	return connView{}
}

// flakyDialer wraps the test dialer: fail makes the next dials fail.
type flakyDialer struct {
	*dialer
	fail  atomic.Int32 // dials to fail with a network error
	deny  atomic.Bool  // fail with a refusal instead
	calls atomic.Int32
}

func (f *flakyDialer) dial(ctx context.Context, req DialRequest) (*peer.Conn, error) {
	f.calls.Add(1)
	if f.deny.Load() {
		return nil, &peer.RemoteError{Service: "login", Message: "the owner denied it"}
	}
	if f.fail.Load() > 0 {
		f.fail.Add(-1)
		return nil, errors.New("network is unreachable")
	}
	return f.dialer.dial(ctx, req)
}

func persistentEnv(t *testing.T) (*Server, *client, *flakyDialer, string) {
	t.Helper()
	testEnv(t)
	f := &flakyDialer{dialer: &dialer{t: t}}
	clock := newClock()
	s := startServer(t, Config{Dial: f.dial, Persistent: true, PageIdle: time.Millisecond, ConnIdle: time.Millisecond, Now: clock.Now})
	c := newLoopbackClient(t, s)
	c.newSession()
	key := c.connect("host.test:21118", "hostpw")
	// Hours without a heartbeat or use: nothing is reaped in app mode.
	clock.Add(48 * time.Hour)
	s.reap()
	if v := c.connsView(); len(v) != 1 || v[0].State != stateConnected {
		t.Fatalf("after 48 idle hours: %+v", v)
	}
	return s, c, f, key
}

func TestPersistentReconnectsAfterDrop(t *testing.T) {
	s, c, f, key := persistentEnv(t)
	before := f.calls.Load()
	s.DropSessionsForTest()
	// The drop only closes the connection; the session notices it a moment
	// later. Until then it still reads "connected", so wait for the redial
	// before waiting for the state.
	for end := time.Now().Add(15 * time.Second); f.calls.Load() == before && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	c.waitState(key, stateConnected)
	if f.calls.Load() != before+1 {
		t.Fatalf("dials: %d, want one more than %d", f.calls.Load(), before)
	}
	// The page's handle is the same and the session works.
	var info map[string]any
	if code := c.jsonCall("GET", "api/v1/info?c="+key, nil, &info); code != 200 {
		t.Fatalf("info after reconnecting: %d %v", code, info)
	}
	// The password typed for the session was used again (from memory).
	f.dialer.mu.Lock()
	last := f.dialer.calls[len(f.dialer.calls)-1]
	f.dialer.mu.Unlock()
	if last.Password != "hostpw" || last.Target != "host.test:21118" {
		t.Fatalf("redial request %+v", last)
	}
}

func TestPersistentBacksOffAndReportsReconnecting(t *testing.T) {
	s, c, f, key := persistentEnv(t)
	f.fail.Store(3)
	s.DropSessionsForTest()
	v := c.waitState(key, stateReconnect)
	// While reconnecting, feature calls say so (not "disconnected").
	code, body := c.do("GET", "api/v1/info?c="+key, nil, nil)
	if code != http.StatusServiceUnavailable || !contains(string(body), `"reconnecting"`) {
		t.Fatalf("info while reconnecting: %d %s", code, body)
	}
	_ = v
	// The app's "the network is back" makes it try at once, not after
	// the backoff.
	deadline := time.Now().Add(10 * time.Second)
	for f.fail.Load() > 0 && time.Now().Before(deadline) {
		s.ReconnectNow()
		time.Sleep(20 * time.Millisecond)
	}
	s.ReconnectNow()
	got := c.waitState(key, stateConnected)
	if got.Error != nil {
		t.Fatalf("connected with an error: %+v", got)
	}
}

func TestPersistentStopsOnRefusal(t *testing.T) {
	s, c, f, key := persistentEnv(t)
	f.deny.Store(true)
	s.DropSessionsForTest()
	v := c.waitState(key, stateFailed)
	if v.Error == nil || v.Error.Code != "host_refused" {
		t.Fatalf("failed state %+v", v)
	}
	n := f.calls.Load()
	s.ReconnectNow()
	time.Sleep(100 * time.Millisecond)
	if f.calls.Load() != n {
		t.Fatal("a refused reconnection is being retried")
	}
	code, body := c.do("GET", "api/v1/info?c="+key, nil, nil)
	if code != http.StatusGone || !contains(string(body), "could not be re-established") {
		t.Fatalf("info after a failed reconnection: %d %s", code, body)
	}
	// Connecting again replaces the failed session.
	f.deny.Store(false)
	if k := c.connect("host.test:21118", "hostpw"); k != key {
		t.Fatalf("new key %q", k)
	}
	c.waitState(key, stateConnected)
}

func TestPersistentDisconnectEndsIt(t *testing.T) {
	s, c, f, key := persistentEnv(t)
	f.fail.Store(1000)
	s.DropSessionsForTest()
	c.waitState(key, stateReconnect)
	if code := c.jsonCall("POST", "api/v1/disconnect", map[string]string{"c": key}, nil); code != 200 {
		t.Fatalf("disconnect: %d", code)
	}
	time.Sleep(50 * time.Millisecond)
	if v := c.connsView(); len(v) != 0 {
		t.Fatalf("after disconnect: %+v", v)
	}
	n := f.calls.Load()
	s.ReconnectNow()
	time.Sleep(100 * time.Millisecond)
	if f.calls.Load() != n {
		t.Fatal("still dialling after the user disconnected")
	}
}

// A page that lost its storage (or moved to a new address) starts a new
// browser session; in app mode it gets the host sessions.
func TestPersistentNewPageTakesOver(t *testing.T) {
	s, c, _, key := persistentEnv(t)
	c2 := newLoopbackClient(t, s)
	c2.newSession()
	if v := c2.connsView(); len(v) != 1 || v[0].Key != key {
		t.Fatalf("new page's sessions: %+v", v)
	}
	if code, _ := c.do("GET", "api/v1/connections", nil, nil); code == 200 {
		t.Fatal("the old browser session still works")
	}
}

// Outside app mode a dropped session ends, as before.
func TestNotPersistentDropEnds(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial})
	c := newLoopbackClient(t, s)
	c.newSession()
	key := c.connect("host.test:21118", "hostpw")
	s.DropSessionsForTest()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if len(c.connsView()) == 0 {
			return
		}
	}
	t.Fatalf("session %s still listed after its connection ended", key)
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// A lockout expires: it is waited out, not taken as the end. What the host
// or the rendezvous server asks to wait is honoured, and every wait has
// some jitter so that many sessions do not come back in the same instant.
func TestReconnectPolicy(t *testing.T) {
	locked := &auth.Error{Code: auth.CodeLockedOut, RetryAfter: 2 * time.Minute}
	if permanent(locked, classify(locked)) {
		t.Fatal("a lockout ends the reconnection attempts for good")
	}
	if w := retryWait(time.Second, locked); w < 2*time.Minute || w > 150*time.Second {
		t.Fatalf("after a 2 min lockout the next attempt waits %s", w)
	}
	limited := &signal.Error{Code: signal.CodeRateLimited, RetryAfter: 40 * time.Second}
	if w := retryWait(time.Second, limited); w < 40*time.Second || w > 50*time.Second {
		t.Fatalf("rate limited for 40 s: waits %s", w)
	}
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		w := retryWait(4*time.Second, errors.New("network down"))
		if w < 4*time.Second || w > 5*time.Second {
			t.Fatalf("backoff 4 s: waits %s", w)
		}
		seen[w] = true
	}
	if len(seen) < 10 {
		t.Fatal("no jitter")
	}
	for _, code := range []auth.Code{auth.CodeWrongHost, auth.CodeBadVersion} {
		err := &auth.Error{Code: code}
		if !permanent(err, classify(err)) {
			t.Errorf("%s is retried forever", code)
		}
	}
}

// Someone at the host pressed Disconnect: the session is not logged back
// in (the remembered pairing or a saved password would let it straight
// back), it says the host ended it, and stays so until the user acts.
func TestPersistentHostDisconnectSticks(t *testing.T) {
	_, c, f, key := persistentEnv(t)
	f.dialer.mu.Lock()
	host := f.dialer.hosts[len(f.dialer.hosts)-1]
	f.dialer.mu.Unlock()
	n := f.calls.Load()
	host.CloseWithReason(hostd.ReasonDisconnectedAtHost)
	v := c.waitState(key, stateFailed)
	if v.Error == nil || v.Error.Code != "host_disconnected" {
		t.Fatalf("after the host disconnected: %+v", v)
	}
	time.Sleep(200 * time.Millisecond)
	if f.calls.Load() != n {
		t.Fatal("reconnected after the host's owner disconnected")
	}
	code, body := c.do("GET", "api/v1/info?c="+key, nil, nil)
	if code != http.StatusGone || !contains(string(body), "the host ended the session") {
		t.Fatalf("info after the host disconnected: %d %s", code, body)
	}
	// Connecting again works as usual.
	if k := c.connect("host.test:21118", "hostpw"); k != key {
		t.Fatalf("new key %q", k)
	}
	c.waitState(key, stateConnected)
}

// The host stopping (a restart, an upgrade) is not the owner ending the
// session: that one reconnects.
func TestPersistentHostShutdownReconnects(t *testing.T) {
	_, c, f, key := persistentEnv(t)
	f.dialer.mu.Lock()
	host := f.dialer.hosts[len(f.dialer.hosts)-1]
	f.dialer.mu.Unlock()
	n := f.calls.Load()
	host.CloseWithReason("host shutting down")
	for end := time.Now().Add(15 * time.Second); f.calls.Load() == n && time.Now().Before(end); {
		time.Sleep(10 * time.Millisecond)
	}
	c.waitState(key, stateConnected)
}

// The system ended the app with sessions open: the next gateway logs in
// to those hosts again by itself, with what is saved for them (Restore),
// and the page finds them. A session the user ended, and one whose host
// has nothing saved, is not restored; the list holds no credential.
func TestPersistentRestoresOpenSessions(t *testing.T) {
	testEnv(t)
	dir := t.TempDir()
	f := &flakyDialer{dialer: &dialer{t: t}}
	restore := func(target string) Redialer {
		if target == "typed.test:21118" {
			return nil // only a typed password: nothing saved
		}
		return func(ctx context.Context, notify func(string)) (*peer.Conn, error) {
			// The test host takes "hostpw": here, what is saved for it.
			return f.dial(ctx, DialRequest{Target: target, Password: "hostpw", Notify: notify})
		}
	}
	cfg := Config{Dial: f.dial, Persistent: true, ConfigDir: dir, Restore: restore}
	s1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := s1.Start()
	c := newLoopbackClient(t, s1)
	c.newSession()
	host := c.connect("host.test:21118", "hostpw")
	c.connect("typed.test:21118", "hostpw")
	ended := c.connect("ended.test:21118", "hostpw")
	if code := c.jsonCall("POST", "api/v1/disconnect", map[string]string{"c": ended}, nil); code != 200 {
		t.Fatalf("disconnect: %d", code)
	}
	// The ended session's goroutine rewrites the list as it finishes:
	// wait for that write rather than for a fixed time.
	var b []byte
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, err = os.ReadFile(filepath.Join(dir, openSessionsFile))
		if err == nil && !strings.Contains(string(b), "ended.test") || time.Now().After(end) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, "host.test") || !strings.Contains(s, "typed.test") || strings.Contains(s, "ended.test") || strings.Contains(s, "pw") {
		t.Fatalf("open sessions file: %s", s)
	}
	s1.Close() // the system ended the app
	<-done
	if b2, _ := os.ReadFile(filepath.Join(dir, openSessionsFile)); string(b2) != string(b) {
		t.Fatalf("closing the gateway changed the list: %s", b2)
	}

	n := f.calls.Load()
	s2 := startServer(t, cfg)
	c2 := newLoopbackClient(t, s2)
	c2.newSession()
	c2.waitState(host, stateConnected)
	if v := c2.connsView(); len(v) != 1 || v[0].Key != host {
		t.Fatalf("restored sessions: %+v", v)
	}
	if f.calls.Load() != n+1 {
		t.Fatalf("dials %d, want %d", f.calls.Load(), n+1)
	}
	f.dialer.mu.Lock()
	last := f.dialer.calls[len(f.dialer.calls)-1]
	f.dialer.mu.Unlock()
	if last.Target != "host.test:21118" {
		t.Fatalf("restore dial %+v", last)
	}
	var info map[string]any
	if code := c2.jsonCall("GET", "api/v1/info?c="+host, nil, &info); code != 200 {
		t.Fatalf("info on the restored session: %d %v", code, info)
	}
	// The user ends it: it leaves the list.
	if code := c2.jsonCall("POST", "api/v1/disconnect", map[string]string{"c": host}, nil); code != 200 {
		t.Fatalf("disconnect: %d", code)
	}
	time.Sleep(50 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(dir, openSessionsFile)); strings.Contains(string(b), "host.test") {
		t.Fatalf("after disconnecting: %s", b)
	}
}
