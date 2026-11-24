package web

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// listenerEnv is a loopback gateway with one browser session that holds one
// host session, and a client that opens a new TCP connection per request
// (so every request goes through the listener under test).
func listenerEnv(t *testing.T) (*Server, *client, string) {
	t.Helper()
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial})
	c := newLoopbackClient(t, s)
	c.http.Transport = &http.Transport{DisableKeepAlives: true, Proxy: nil}
	c.newSession()
	key := c.connect("host.test:21118", "hostpw")
	return s, c, key
}

func refused(t *testing.T, base string) bool {
	t.Helper()
	hc := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil}}
	resp, err := hc.Get(base)
	if err != nil {
		return true
	}
	resp.Body.Close()
	return false
}

func healthy(s *Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.Healthy(ctx)
}

// sessionIntact checks that the browser session and its host session
// survived, through a fresh connection.
func sessionIntact(t *testing.T, c *client, key string) {
	t.Helper()
	var j struct {
		Conns []struct{ Key string } `json:"conns"`
	}
	if code := c.jsonCall("GET", "api/v1/connections", nil, &j); code != 200 {
		t.Fatalf("connections: %d", code)
	}
	if len(j.Conns) != 1 || j.Conns[0].Key != key {
		t.Fatalf("host sessions after rebind = %+v, want just %q", j.Conns, key)
	}
	var info map[string]any
	if code := c.jsonCall("GET", "api/v1/info?c="+key, nil, &info); code != 200 {
		t.Fatalf("info over the old host session: %d %v", code, info)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestListenerClosedUnderServerIsRebound(t *testing.T) {
	s, c, key := listenerEnv(t)
	url, port := s.URL(), s.Addr().(*net.TCPAddr).Port
	if err := healthy(s); err != nil {
		t.Fatalf("healthy at start: %v", err)
	}
	s.BreakListenerForTest(false)
	waitFor(t, "Serve to notice the failed listener", func() bool { return !s.Listening() })
	if healthy(s) == nil {
		t.Fatal("Healthy is nil with a dead listener")
	}
	if !refused(t, url) {
		t.Fatal("the dead listener still answers")
	}
	if err := s.Rebind(); err != nil {
		t.Fatal(err)
	}
	if s.URL() != url || s.Addr().(*net.TCPAddr).Port != port {
		t.Fatal("Rebind changed the URL although the port was free")
	}
	if err := healthy(s); err != nil {
		t.Fatalf("not healthy after Rebind: %v", err)
	}
	if !s.Listening() {
		t.Fatal("Listening is false after Rebind")
	}
	sessionIntact(t, c, key)
}

// The iOS case: the socket is gone but Accept never returns, so Serve
// cannot tell. Only the health check sees it.
func TestStalledListenerIsDetectedAndRebound(t *testing.T) {
	s, c, key := listenerEnv(t)
	url := s.URL()
	s.BreakListenerForTest(true)
	time.Sleep(50 * time.Millisecond)
	if !s.Listening() {
		t.Fatal("a stalled listener should look fine to Listening (that is the point)")
	}
	if !refused(t, url) {
		t.Fatal("the stalled listener still answers")
	}
	if healthy(s) == nil {
		t.Fatal("Healthy did not see the stalled listener")
	}
	if err := s.Rebind(); err != nil {
		t.Fatal(err)
	}
	if s.URL() != url {
		t.Fatal("Rebind changed the URL")
	}
	if err := healthy(s); err != nil {
		t.Fatalf("not healthy after Rebind: %v", err)
	}
	sessionIntact(t, c, key)
}

func TestPauseAndResume(t *testing.T) {
	s, c, key := listenerEnv(t)
	url := s.URL()
	s.PauseListening()
	if s.Listening() || healthy(s) == nil {
		t.Fatal("paused listener reported as listening")
	}
	if !refused(t, url) {
		t.Fatal("paused gateway still accepts connections")
	}
	s.PauseListening() // twice is fine
	if err := s.Rebind(); err != nil {
		t.Fatal(err)
	}
	if s.URL() != url || healthy(s) != nil {
		t.Fatal("resume did not restore the same URL")
	}
	sessionIntact(t, c, key)
}

// When another program took the port, Rebind moves to a new port and a new
// token (the other program may have seen requests carrying the old one).
func TestRebindPortTakenMovesAndRotatesToken(t *testing.T) {
	s, c, key := listenerEnv(t)
	oldURL, oldAddr := s.URL(), s.Addr().String()
	s.PauseListening()
	squatter, err := net.Listen("tcp", oldAddr)
	if err != nil {
		t.Skipf("cannot take the freed port: %v", err)
	}
	defer squatter.Close()
	if err := s.Rebind(); err != nil {
		t.Fatal(err)
	}
	newURL := s.URL()
	if newURL == oldURL || s.Addr().String() == oldAddr {
		t.Fatal("Rebind kept the taken address")
	}
	oldTok := strings.Split(strings.TrimPrefix(oldURL, "http://"), "/")[1]
	newTok := strings.Split(strings.TrimPrefix(newURL, "http://"), "/")[1]
	if oldTok == newTok || newTok == "" {
		t.Fatal("the token was not replaced along with the port")
	}
	if err := healthy(s); err != nil {
		t.Fatal(err)
	}
	// The old token is no good on the new port.
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get("http://" + s.Addr().String() + "/" + oldTok + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("old token on the new port: %d, want 404", resp.StatusCode)
	}
	// The page on the new URL works (the Host check follows the port) and
	// the existing browser session can still be resumed by id.
	c2 := newLoopbackClient(t, s)
	c2.sid, c2.csrf = c.sid, c.csrf
	sessionIntact(t, c2, key)
}

func TestRebindAfterCloseFails(t *testing.T) {
	testEnv(t)
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	done := s.Start()
	s.Close()
	<-done
	if err := s.Rebind(); err == nil {
		t.Fatal("Rebind on a closed server succeeded")
	}
	if s.Listening() || healthy(s) == nil {
		t.Fatal("closed server reported as listening")
	}
}
