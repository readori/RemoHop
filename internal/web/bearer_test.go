package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// bearerCaller makes JSON calls against a gateway with optional bearer
// token and extra headers.
type bearerCaller struct {
	t    *testing.T
	base string
	hc   *http.Client
}

func (b *bearerCaller) call(method, path, bearer string, body any, hdr ...string) (int, map[string]any) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	if len(resp.Header.Values("Set-Cookie")) != 0 {
		b.t.Errorf("%s %s set a cookie for a bearer client", method, path)
	}
	return resp.StatusCode, m
}

func errCode(m map[string]any) any {
	if e, ok := m["error"].(map[string]any); ok {
		return e["code"]
	}
	return nil
}

// connectBearer connects the shared bearer session to a fake host and
// returns the connection key.
func (b *bearerCaller) connect(tok string) string {
	b.t.Helper()
	code, a := b.call("POST", "api/v1/connect", tok, map[string]any{"target": "123456789", "password": "hostpw"})
	if code != 202 {
		b.t.Fatalf("connect: %d %v", code, a)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, j := b.call("GET", "api/v1/connect/"+a["attempt"].(string), tok, nil)
		switch j["state"] {
		case "connected":
			return j["conn"].(map[string]any)["key"].(string)
		case "failed", "canceled":
			b.t.Fatalf("connect failed: %v", j)
		}
	}
	b.t.Fatal("connect timed out")
	return ""
}

// A program uses a gateway started with --api-token-file with
// "Authorization: Bearer <token>" alone: no login call, no cookie, no CSRF
// token, no Origin. The token is not the browser password.
func TestBearerClient(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t, opts: peertest.Options{}}
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: writePasswordFile(t, "gateway secret"),
		APITokenFile: writePasswordFile(t, "program token 123"), Dial: d.dial})
	b := &bearerCaller{t: t, base: "http://" + s.Addr().String() + "/", hc: &http.Client{Timeout: 30 * time.Second}}
	const tok = "program token 123"

	if code, _ := b.call("GET", "api/v1/connections", "", nil); code != 401 {
		t.Fatalf("no credentials: %d", code)
	}
	if code, m := b.call("GET", "api/v1/connections", "wrong token", nil); code != 401 || errCode(m) != "bad_password" {
		t.Fatalf("wrong bearer: %d %v", code, m)
	}
	if code, m := b.call("GET", "api/v1/connections", "gateway secret", nil); code != 401 || errCode(m) != "bad_password" {
		t.Fatalf("the browser password works as the API token: %d %v", code, m)
	}
	if code, _ := b.call("GET", "api/v1/connections", tok, nil); code != 200 {
		t.Fatalf("bearer GET: %d", code)
	}

	// A state-changing call without CSRF token or Origin.
	key := b.connect(tok)
	// Requests share one session: the connection is visible on the next call.
	if _, m := b.call("GET", "api/v1/connections", tok, nil); len(m["conns"].([]any)) != 1 {
		t.Fatalf("connections after connect: %v", m)
	}

	// Terminal WebSocket: ticket plus the header, without an Origin (the
	// handshake is written by hand: x/net/websocket always sends one) ...
	ticket := func() string {
		_, tk := b.call("POST", "api/v1/ticket", tok, map[string]any{"purpose": "ws"})
		return tk["ticket"].(string)
	}
	q := url.Values{"c": {key}, "rows": {"24"}, "cols": {"80"}, "session": {"api"}}
	q.Set("t", ticket())
	if code := rawUpgrade(t, s.Addr().String(), "/api/v1/term/ws?"+q.Encode(), "Bearer "+tok, ""); code != 101 {
		t.Fatalf("bearer websocket without Origin: %d", code)
	}
	// ... or with the gateway's own Origin.
	q.Set("t", ticket())
	cfg, err := websocket.NewConfig("ws://"+s.Addr().String()+"/api/v1/term/ws?"+q.Encode(), "http://"+s.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = http.Header{"Authorization": {"Bearer " + tok}}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("terminal websocket: %v", err)
	}
	defer ws.Close()
	readControl(t, ws, "attached")
	sendTerm(t, ws, map[string]any{"type": "in", "data": "echo bearer-$((6*7))\r"})
	readUntil(t, ws, "bearer-42")

	// Without the header the same kind of ticket needs a cookie session.
	q.Set("t", ticket())
	cfg2, _ := websocket.NewConfig("ws://"+s.Addr().String()+"/api/v1/term/ws?"+q.Encode(), "http://"+s.Addr().String())
	if ws2, err := websocket.DialConfig(cfg2); err == nil {
		ws2.Close()
		t.Fatal("a ticket was redeemed without the bearer header or a cookie")
	}
}

// rawUpgrade sends a WebSocket upgrade request and returns the status.
func rawUpgrade(t *testing.T, addr, path, auth, origin string) int {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nAuthorization: %s\r\n", path, addr, auth)
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A reverse proxy that injects the bearer header into every request,
// browsers' included, must not switch off the browser protections: a
// cross-site page reaching the gateway through such a proxy is refused,
// and a same-origin page still needs the CSRF token for changes.
func TestBearerFromBrowserKeepsBrowserChecks(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t, opts: peertest.Options{}}
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: writePasswordFile(t, "gateway secret"),
		APITokenFile: writePasswordFile(t, "program token 123"), Dial: d.dial})
	b := &bearerCaller{t: t, base: "http://" + s.Addr().String() + "/", hc: &http.Client{Timeout: 30 * time.Second}}
	const tok = "program token 123"
	self := "http://" + s.Addr().String()
	evil := "https://evil.example"
	connect := map[string]any{"target": "123456789", "password": "hostpw"}

	// Cross-site, as a browser marks it, with the injected token: refused.
	for _, hdr := range [][]string{
		{"Origin", evil},
		{"Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"},
		{"Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "no-cors"},
		{"Origin", "null"},
	} {
		if code, m := b.call("POST", "api/v1/connect", tok, connect, hdr...); code != 403 || errCode(m) != "bad_origin" {
			t.Fatalf("cross-site POST with an injected bearer (%v): %d %v", hdr, code, m)
		}
		if code, m := b.call("GET", "api/v1/connections", tok, nil, hdr...); code != 403 {
			t.Fatalf("cross-site GET with an injected bearer (%v): %d %v", hdr, code, m)
		}
		if code, m := b.call("POST", "api/v1/session", tok, map[string]any{}, hdr...); code != 403 {
			t.Fatalf("cross-site session call with an injected bearer (%v): %d %v", hdr, code, m)
		}
	}
	// Same-origin browser signals (or just a cookie): the CSRF token is
	// needed for changes, as for a logged-in browser ...
	for _, hdr := range [][]string{
		{"Origin", self},
		{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors"},
		{"Cookie", "CF_Authorization=x"},
	} {
		if code, m := b.call("POST", "api/v1/connect", tok, connect, hdr...); code != 403 || errCode(m) != "bad_csrf" {
			t.Fatalf("browser POST with an injected bearer and no CSRF token (%v): %d %v", hdr, code, m)
		}
		if code, _ := b.call("GET", "api/v1/connections", tok, nil, hdr...); code != 200 {
			t.Fatalf("same-origin GET with an injected bearer (%v): %d", hdr, code)
		}
	}
	// ... which the page gets from the session call, as usual.
	code, sess := b.call("POST", "api/v1/session", tok, map[string]any{}, "Origin", self, "Sec-Fetch-Site", "same-origin")
	if code != 200 || sess["csrf"] == nil {
		t.Fatalf("session call from the page: %d %v", code, sess)
	}
	csrf := sess["csrf"].(string)
	if code, m := b.call("POST", "api/v1/connect", tok, connect, "Origin", self, "Sec-Fetch-Site", "same-origin", hdrCSRF, csrf); code != 202 {
		t.Fatalf("same-origin POST with an injected bearer and the CSRF token: %d %v", code, m)
	}
	// Cross-site stays refused even with the token in hand.
	if code, _ := b.call("POST", "api/v1/connect", tok, connect, "Origin", evil, hdrCSRF, csrf); code != 403 {
		t.Fatalf("cross-site POST with bearer and CSRF token: %d", code)
	}

	// WebSocket: a bearer upgrade from a foreign Origin is refused, even
	// with a valid ticket.
	key := b.connect(tok)
	_, tk := b.call("POST", "api/v1/ticket", tok, map[string]any{"purpose": "ws"})
	q := url.Values{"c": {key}, "rows": {"24"}, "cols": {"80"}, "session": {"api"}, "t": {tk["ticket"].(string)}}
	if code := rawUpgrade(t, s.Addr().String(), "/api/v1/term/ws?"+q.Encode(), "Bearer "+tok, evil); code != 403 {
		t.Fatalf("bearer websocket from a foreign Origin: %d", code)
	}
	cfg, _ := websocket.NewConfig("ws://"+s.Addr().String()+"/api/v1/desktop/ws?"+q.Encode(), evil)
	cfg.Header = http.Header{"Authorization": {"Bearer " + tok}}
	if ws, err := websocket.DialConfig(cfg); err == nil {
		ws.Close()
		t.Fatal("desktop websocket from a foreign Origin accepted with a bearer header")
	}
	// Downloads too.
	_, tk = b.call("POST", "api/v1/ticket", tok, map[string]any{"purpose": "dl"})
	if code, _ := b.call("GET", "api/v1/files/download?"+url.Values{"c": {key}, "path": {"/etc/hostname"}, "t": {tk["ticket"].(string)}}.Encode(),
		tok, nil, "Sec-Fetch-Site", "cross-site"); code != 403 {
		t.Fatalf("cross-site download with a bearer header: %d", code)
	}
}

// Program access is off unless asked for: the header is then not a login,
// says so, and does not get in the way of a browser's cookie session.
func TestBearerOffByDefault(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t, opts: peertest.Options{}}
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: writePasswordFile(t, "gateway secret"), Dial: d.dial})
	b := &bearerCaller{t: t, base: "http://" + s.Addr().String() + "/", hc: &http.Client{Timeout: 30 * time.Second}}
	if code, m := b.call("GET", "api/v1/connections", "gateway secret", nil); code != 401 || errCode(m) != "bearer_disabled" {
		t.Fatalf("bearer without --api-token-file: %d %v", code, m)
	}
	if code, m := b.call("POST", "api/v1/connect", "gateway secret", map[string]any{"target": "1"}); code != 401 || errCode(m) != "bearer_disabled" {
		t.Fatalf("bearer POST without --api-token-file: %d %v", code, m)
	}
	// A logged-in browser behind a proxy that injects the header anyway.
	hc, _ := tlsClient()
	hc.Timeout = 30 * time.Second
	bb := &bearerCaller{t: t, base: b.base, hc: hc}
	req, _ := http.NewRequest("POST", b.base+"api/v1/login", strings.NewReader(`{"password":"gateway secret"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	if code, _ := bb.call("GET", "api/v1/connections", "gateway secret", nil); code != 200 {
		t.Fatalf("cookie session with an ignored bearer header: %d", code)
	}
}

// The migration path for gateways that used the gateway password as the
// bearer token: point --api-token-file at the password file.
func TestBearerPasswordFileAsToken(t *testing.T) {
	testEnv(t)
	pw := writePasswordFile(t, "gateway secret")
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: pw, APITokenFile: pw})
	b := &bearerCaller{t: t, base: "http://" + s.Addr().String() + "/", hc: &http.Client{Timeout: 30 * time.Second}}
	if code, _ := b.call("GET", "api/v1/connections", "gateway secret", nil); code != 200 {
		t.Fatalf("bearer with the password file as the token: %d", code)
	}
	if _, err := New(Config{Listen: "127.0.0.1:0", APITokenFile: pw}); err == nil || !strings.Contains(err.Error(), "--password-file") {
		t.Fatalf("--api-token-file without a password: %v", err)
	}
}

// On a loopback gateway without a password the header means nothing.
func TestBearerIgnoredWithoutPassword(t *testing.T) {
	testEnv(t)
	s := startServer(t, Config{Listen: "127.0.0.1:0"})
	req, _ := http.NewRequest("GET", s.URL()+"api/v1/connections", nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
