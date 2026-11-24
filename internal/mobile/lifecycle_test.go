package mobile

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/signal"
)

// freshClient calls the gateway's API over a new TCP connection per
// request, as the page in a web view resumed from the background does:
// keep-alive connections would bypass the listener under test.
type freshClient struct {
	t         *testing.T
	base      string
	sid, csrf string
	hc        *http.Client
}

func newFreshClient(t *testing.T, base string) *freshClient {
	return &freshClient{t: t, base: base, hc: &http.Client{Timeout: 10 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil}}}
}

// try is call without failing the test on a transport error.
func (c *freshClient) try(method, path string, body, out any) error {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+"api/v1/"+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.sid != "" {
		req.Header.Set("X-RemoHop-Session", c.sid)
		req.Header.Set("X-RemoHop-CSRF", c.csrf)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return nil
}

func (c *freshClient) call(method, path string, body, out any) {
	c.t.Helper()
	if err := c.try(method, path, body, out); err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
}

// connect runs the page's connect flow and returns the connection key.
func (c *freshClient) connect(target, password string) string {
	c.t.Helper()
	var att struct {
		Attempt string
		State   string
		Error   *struct{ Code, Message string }
		Conn    *struct{ Key string }
	}
	c.call("POST", "connect", map[string]any{"target": target, "password": password}, &att)
	for deadline := time.Now().Add(20 * time.Second); att.State != "connected"; {
		if att.State == "failed" || att.State == "canceled" || time.Now().After(deadline) {
			c.t.Fatalf("connect: %+v %+v", att, att.Error)
		}
		time.Sleep(50 * time.Millisecond)
		c.call("GET", "connect/"+att.Attempt, nil, &att)
	}
	return att.Conn.Key
}

func (c *freshClient) listFiles(key string) {
	c.t.Helper()
	var list struct{ Path string }
	c.call("GET", "files/list?c="+url.QueryEscape(key)+"&path="+url.QueryEscape(c.t.TempDir()), nil, &list)
	if list.Path == "" {
		c.t.Fatal("files/list returned nothing")
	}
}

func (c *freshClient) conns() []string {
	c.t.Helper()
	var j struct{ Conns []struct{ Key string } }
	c.call("GET", "connections", nil, &j)
	var out []string
	for _, x := range j.Conns {
		out = append(out, x.Key)
	}
	return out
}

// gatewayWithSession starts the gateway, opens a page session and connects
// to a host by address and password, like the owner's first attempt.
func gatewayWithSession(t *testing.T) (gwURL, addr, key string, c *freshClient) {
	t.Helper()
	initDir(t)
	addr = startHost(t)
	gwURL, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	if !GatewayRunning() {
		t.Fatal("GatewayRunning is false after EnsureGateway")
	}
	c = newFreshClient(t, gwURL)
	var sess struct{ SID, CSRF string }
	c.call("POST", "session", map[string]string{"sid": ""}, &sess)
	c.sid, c.csrf = sess.SID, sess.CSRF
	key = c.connect(addr, hostPassword)
	return gwURL, addr, key, c
}

// afterRevive checks what the owner needed: the page, on its old URL,
// works again; the host session from before still works; a new connection
// can be made.
func afterRevive(t *testing.T, gwURL, addr, key string, c *freshClient) {
	t.Helper()
	u, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	if u != gwURL {
		t.Fatal("EnsureGateway changed the URL although the port was free")
	}
	if !GatewayRunning() {
		t.Fatal("GatewayRunning is false after EnsureGateway")
	}
	if got := c.conns(); len(got) != 1 || got[0] != key {
		t.Fatalf("host sessions after revive: %v, want [%s]", got, key)
	}
	c.listFiles(key)
	c.call("POST", "disconnect", map[string]string{"c": key}, nil)
	c.listFiles(c.connect(addr, hostPassword))
}

// iOS's case: the suspended app's listening socket was taken away; Go's
// accept loop does not notice, so the old GatewayRunning said true and
// nothing restarted it. EnsureGateway must see it and revive it.
func TestEnsureGatewayRevivesStalledListener(t *testing.T) {
	gwURL, addr, key, c := gatewayWithSession(t)
	BreakGatewayListenerForTest(true)
	time.Sleep(20 * time.Millisecond)
	if !GatewayRunning() {
		t.Fatal("a stalled listener is invisible to GatewayRunning (no I/O); the test hook is not doing its job")
	}
	if err := c.try("GET", "connections", nil, nil); err == nil {
		t.Fatal("the broken gateway still answered: the hook did not break it")
	}
	afterRevive(t, gwURL, addr, key, c)
}

func TestEnsureGatewayRevivesClosedListener(t *testing.T) {
	gwURL, addr, key, c := gatewayWithSession(t)
	BreakGatewayListenerForTest(false)
	deadline := time.Now().Add(5 * time.Second)
	for GatewayRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if GatewayRunning() {
		t.Fatal("GatewayRunning still true with a failed listener")
	}
	// StartGateway (what older app code calls) repairs it as well.
	if u, err := StartGateway(); err != nil || u != gwURL {
		t.Fatalf("StartGateway after a failed listener: %v (same URL: %v)", err, u == gwURL)
	}
	afterRevive(t, gwURL, addr, key, c)
}

// What the iOS app does on .background and .active.
func TestPauseThenEnsureGateway(t *testing.T) {
	gwURL, addr, key, c := gatewayWithSession(t)
	PauseGateway()
	if GatewayRunning() {
		t.Fatal("GatewayRunning true while paused")
	}
	if err := c.try("GET", "connections", nil, nil); err == nil {
		t.Fatal("a paused gateway accepted a connection")
	}
	afterRevive(t, gwURL, addr, key, c)
}

// If another app took the port while we were paused, the gateway moves to
// a new port and token; the app reloads the page with the new URL.
func TestEnsureGatewayPortTaken(t *testing.T) {
	gwURL, _, key, c := gatewayWithSession(t)
	PauseGateway()
	u, _ := url.Parse(gwURL)
	squat, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Skipf("cannot take the freed port: %v", err)
	}
	defer squat.Close()
	nu, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	if nu == gwURL {
		t.Fatal("EnsureGateway kept a port another program holds")
	}
	if strings.Split(nu, "/")[3] == strings.Split(gwURL, "/")[3] {
		t.Fatal("the token was kept on the new port")
	}
	c2 := newFreshClient(t, nu)
	c2.sid, c2.csrf = c.sid, c.csrf
	if got := c2.conns(); len(got) != 1 || got[0] != key {
		t.Fatalf("host sessions on the new port: %v", got)
	}
}

// A gateway that was stopped altogether is started again.
func TestEnsureGatewayAfterStop(t *testing.T) {
	initDir(t)
	u1, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	StopGateway()
	if GatewayRunning() {
		t.Fatal("running after StopGateway")
	}
	u2, err := EnsureGateway()
	if err != nil {
		t.Fatal(err)
	}
	if u2 == u1 {
		t.Fatal("a new gateway reused the old token")
	}
	PauseGateway() // nothing to do
	BreakGatewayListenerForTest(true)
	if u3, err := EnsureGateway(); err != nil || u3 != u2 {
		t.Fatalf("revive: %v", err)
	}
}

type checkResult struct {
	OK     bool   `json:"ok"`
	Code   string `json:"code"`
	Error  string `json:"error"`
	Server string `json:"server"`
	Key    string `json:"key"`
}

func check(t *testing.T, addr string) checkResult {
	t.Helper()
	s, err := CheckRendezvous(addr)
	if err != nil {
		t.Fatal(err)
	}
	var r checkResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return r
}

func loopbackSignal(t *testing.T) string {
	t.Helper()
	main, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	alt, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})
	return main.LocalAddr().String()
}

func TestCheckRendezvous(t *testing.T) {
	initDir(t)
	if r := check(t, ""); r.OK || r.Code != "no_rendezvous" {
		t.Fatalf("nothing configured: %+v", r)
	}
	if r := check(t, "bad host/name"); r.OK || r.Code != "bad_address" {
		t.Fatalf("bad address: %+v", r)
	}
	// The owner's mistake: the demo host's address as the rendezvous server.
	host := startHost(t)
	if r := check(t, host); r.OK || r.Code != "signal_is_host" || !strings.Contains(r.Error, "is a RemoHop host") {
		t.Fatalf("host as rendezvous server: %+v", r)
	}
	sig := loopbackSignal(t)
	r := check(t, sig)
	if !r.OK || r.Code != "ok" || r.Key == "" || r.Server != sig {
		t.Fatalf("real server: %+v", r)
	}
	if err := SetSetting(signal.SettingAddr, sig); err != nil {
		t.Fatal(err)
	}
	if r := check(t, ""); !r.OK {
		t.Fatalf("configured server: %+v", r)
	}
	// A pin for another key.
	other := strings.Repeat("A", len(r.Key)-1) + "E"
	if err := SetSetting(signal.SettingPin, other); err == nil {
		if r := check(t, ""); r.OK || r.Code != "key_mismatch" {
			t.Fatalf("wrong pin: %+v", r)
		}
	}
	if err := SetSetting(signal.SettingPin, r.Key); err != nil {
		t.Fatal(err)
	}
	if r := check(t, ""); !r.OK {
		t.Fatalf("right pin: %+v", r)
	}
}

func TestCheckRendezvousUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a handshake timeout")
	}
	initDir(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0") // answers nothing
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if r := check(t, pc.LocalAddr().String()); r.OK || r.Code != "unreachable" {
		t.Fatalf("silent server: %+v", r)
	}
}

// PairWithPassword reports the wrong-server codes too.
func TestPairWithHostAsRendezvous(t *testing.T) {
	initDir(t)
	host := startHost(t)
	if err := SetSetting(signal.SettingAddr, host); err != nil {
		t.Fatal(err)
	}
	if r := pairJSON(t, "707612667", "x", ""); r.OK || r.Code != "signal_is_host" {
		t.Fatalf("pair by ID with a host as rendezvous server: %+v", r)
	}
}
