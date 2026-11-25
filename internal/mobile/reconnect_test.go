package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/hostd"
)

// runHost starts a host on loopback with cfg (Dir, Listen and Locals filled
// in) and returns it.
func runHost(t *testing.T, cfg hostd.Config) *hostd.Host {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.Locals = func() []netip.Addr { return nil }
	h, err := hostd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	t.Cleanup(StopGateway) // runs first: the gateway goes before the host (see startHost)
	return h
}

type connState struct {
	Key        string
	State      string
	Retries    int
	Reconnects int
	Error      *struct{ Code, Message string }
}

func (c *freshClient) states() []connState {
	c.t.Helper()
	var j struct{ Conns []connState }
	c.call("GET", "connections", nil, &j)
	return j.Conns
}

func (c *freshClient) waitState(key, state string, timeout time.Duration) connState {
	c.t.Helper()
	var last []connState
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		last = c.states()
		for _, s := range last {
			if s.Key == key && s.State == state {
				return s
			}
		}
	}
	c.t.Fatalf("%s never reached %q: %+v", key, state, last)
	return connState{}
}

// reconnects is how often key's session has been re-established so far.
func (c *freshClient) reconnects(key string) int {
	c.t.Helper()
	for _, s := range c.states() {
		if s.Key == key {
			return s.Reconnects
		}
	}
	c.t.Fatalf("no session %s", key)
	return 0
}

// dropAndWait drops every host session and waits until key's is connected
// again. "connected" alone would not do: the gateway notices the drop a
// moment later, and until then the list still shows the old connection.
func (c *freshClient) dropAndWait(key string, timeout time.Duration) {
	c.t.Helper()
	before := c.reconnects(key)
	DropSessionsForTest()
	var last []connState
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		last = c.states()
		for _, s := range last {
			if s.Key == key && s.State == "connected" && s.Reconnects > before {
				return
			}
		}
	}
	c.t.Fatalf("%s was not re-established: %+v", key, last)
}

// termLine is the terminal input that runs a POSIX shell command line. On
// Windows the host's shell is PowerShell, which writes $(...) for $((...))
// and takes Enter as a carriage return.
func termLine(cmd string) string {
	end := "\n"
	if runtime.GOOS == "windows" {
		cmd = strings.ReplaceAll(strings.ReplaceAll(cmd, "$((", "$("), "))", ")")
		end = "\r"
	}
	b, _ := json.Marshal(map[string]string{"type": "in", "data": cmd + end})
	return string(b)
}

func pageOn(t *testing.T) *freshClient {
	t.Helper()
	u, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	c := newFreshClient(t, u)
	var sess struct{ SID, CSRF string }
	c.call("POST", "session", map[string]string{"sid": ""}, &sess)
	c.sid, c.csrf = sess.SID, sess.CSRF
	return c
}

// The owner's requirement: a session stays until the user disconnects,
// even with a host whose one-time password the first login spent.
func TestReconnectWithOneTimePassword(t *testing.T) {
	d := initDir(t)
	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "config.json"), []byte(`{"otp":"on"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := runHost(t, hostd.Config{Dir: hostDir})
	if h.PasswordMode() != "one-time" {
		t.Fatalf("host password mode %q", h.PasswordMode())
	}
	addr, pw := h.Addr().String(), h.Password()
	c := pageOn(t)
	key := c.connect(addr, pw)
	if h.Password() == pw {
		t.Fatal("the one-time password was not spent")
	}
	if savedPairing(d, key) {
		t.Fatal("the session's re-login pairing was saved to disk")
	}
	c.dropAndWait(key, 20*time.Second)
	c.listFiles(key)
	if savedPairing(d, key) {
		t.Fatal("reconnecting saved a pairing to disk")
	}
	// And again (the in-memory pairing is not used up).
	c.dropAndWait(key, 20*time.Second)
	c.listFiles(key)
	// Disconnect ends it for good.
	c.call("POST", "disconnect", map[string]string{"c": key}, nil)
	if s := c.states(); len(s) != 0 {
		t.Fatalf("after disconnect: %+v", s)
	}
}

// A host that allows no pairing and asks its owner about every login (a
// quick-support host): reconnecting needs the owner again, and the page
// sees that rather than an endless loop.
func TestReconnectNeedsApprovalAgain(t *testing.T) {
	initDir(t)
	h := runHost(t, hostd.Config{Password: hostPassword, NoPairing: true, Overrides: &hostd.Overrides{AskAll: true}})
	approve := func(allow bool) {
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if len(h.Pending()) > 0 {
				if err := h.Decide(0, allow); err == nil {
					return
				}
			}
		}
		t.Error("no login waited for approval")
	}
	c := pageOn(t)
	go approve(true)
	key := c.connect(h.Addr().String(), hostPassword)

	DropSessionsForTest()
	c.waitState(key, "waiting_approval", 20*time.Second)
	approve(true)
	c.waitState(key, "connected", 20*time.Second)
	c.listFiles(key)

	DropSessionsForTest()
	c.waitState(key, "waiting_approval", 20*time.Second)
	approve(false)
	s := c.waitState(key, "failed", 20*time.Second)
	if s.Error == nil || s.Error.Code != "denied" {
		t.Fatalf("after the owner said no: %+v", s)
	}
}

// After a drop the terminal re-attaches to the same shell on the host,
// with what ran in it.
func TestReconnectTerminalReattaches(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	initDir(t)
	h := runHost(t, hostd.Config{Password: hostPassword})
	c := pageOn(t)
	key := c.connect(h.Addr().String(), hostPassword)

	// A name of its own: shell sessions live in a process-wide table that
	// outlives this test's host, so with the default one -count=2 found
	// the first run's session there.
	ws, first := c.openTerm(key, fmt.Sprintf("reattach%d", time.Now().UnixNano()%1e9))
	if first.Session == "" || !first.Created {
		t.Fatalf("first attach: %+v", first)
	}
	websocket.Message.Send(ws, termLine("echo remohop-$((6*7))"))
	readUntil(t, ws, "remohop-42")

	c.dropAndWait(key, 20*time.Second)
	ws.Close()
	ws2, again := c.openTerm(key, first.Session)
	defer ws2.Close()
	if again.Session != first.Session || again.Created {
		t.Fatalf("after reconnecting: %+v, want the same session %q re-attached", again, first.Session)
	}
	websocket.Message.Send(ws2, termLine("echo remohop-$((7*8))"))
	readUntil(t, ws2, "remohop-56")
}

type attached struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Created bool   `json:"created"`
}

func (c *freshClient) openTerm(key, session string) (*websocket.Conn, attached) {
	c.t.Helper()
	var tk struct{ Ticket string }
	c.call("POST", "ticket", map[string]string{"purpose": "ws"}, &tk)
	u, _ := url.Parse(c.base)
	q := url.Values{"t": {tk.Ticket}, "c": {key}, "rows": {"24"}, "cols": {"80"}}
	if session != "" {
		q.Set("session", session)
	}
	origin := "http://" + u.Host
	cfg, err := websocket.NewConfig("ws://"+u.Host+u.Path+"api/v1/term/ws?"+q.Encode(), origin)
	if err != nil {
		c.t.Fatal(err)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		c.t.Fatal(err)
	}
	ws.SetDeadline(time.Now().Add(20 * time.Second))
	for {
		var m []byte
		if err := websocket.Message.Receive(ws, &m); err != nil {
			c.t.Fatalf("terminal: %v", err)
		}
		var a attached
		if json.Unmarshal(m, &a) == nil && a.Type != "" {
			if a.Type != "attached" {
				c.t.Fatalf("terminal said %s", m)
			}
			return ws, a
		}
	}
}

func readUntil(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	var seen strings.Builder
	for !strings.Contains(seen.String(), want) {
		var m []byte
		if err := websocket.Message.Receive(ws, &m); err != nil {
			t.Fatalf("waiting for %q: %v (output so far %q)", want, err, seen.String())
		}
		seen.Write(m)
	}
}

func TestSessionsJSONAndDisconnectAll(t *testing.T) {
	initDir(t)
	if s, err := SessionsJSON(); err != nil || s != "[]" {
		t.Fatalf("no gateway: %q %v", s, err)
	}
	h := runHost(t, hostd.Config{Password: hostPassword})
	c := pageOn(t)
	if s, _ := SessionsJSON(); s != "[]" {
		t.Fatalf("no sessions: %q", s)
	}
	key := c.connect(h.Addr().String(), hostPassword)
	var list []struct{ Key, Target, State string }
	s, _ := SessionsJSON()
	if err := json.Unmarshal([]byte(s), &list); err != nil || len(list) != 1 || list[0].Key != key || list[0].State != "connected" {
		t.Fatalf("sessions: %s", s)
	}
	DisconnectAll()
	if s, _ := SessionsJSON(); s != "[]" {
		t.Fatalf("after DisconnectAll: %s", s)
	}
	if st := c.states(); len(st) != 0 {
		t.Fatalf("the page still lists %+v", st)
	}
}

// Forgetting a host in Settings ends its sessions (they would reconnect
// with what was just deleted).
func TestForgetEndsSessions(t *testing.T) {
	initDir(t)
	h := runHost(t, hostd.Config{Password: hostPassword})
	c := pageOn(t)
	key := c.connect(h.Addr().String(), hostPassword)
	if err := SetAlias(key, "lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := Forget("lab"); err != nil {
		t.Fatal(err)
	}
	if s := c.states(); len(s) != 0 {
		t.Fatalf("after Forget: %+v", s)
	}
}
