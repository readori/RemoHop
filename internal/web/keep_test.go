package web

import (
	"context"
	"errors"
	"net/http"
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
