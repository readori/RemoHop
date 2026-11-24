package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// TestWebPublicE2E serves `remohop web` behind a TLS-terminating tunnel and
// uses it only through the tunnel's public https URL, as a browser would:
//
//	REMOHOP_WEB_E2E=1 REMOHOP_E2E_CLOUDFLARED=/path/to/cloudflared \
//	    go test -count=1 -v -run TestWebPublicE2E ./test/e2e
//
// With REMOHOP_E2E_CLOUDFLARED the tunnel is a Cloudflare quick tunnel
// (cloudflared tunnel --url, no account; a random *.trycloudflare.com
// name). Without it a local TLS reverse proxy plays the tunnel, which
// checks the test itself but not Cloudflare. The public host name, the
// gateway password and the session cookie are never logged.
func TestWebPublicE2E(t *testing.T) {
	gate(t, "REMOHOP_WEB_E2E")

	// The host the page will open a terminal on.
	hostDev := newDevice(t, "web-host")
	hostPort := freePort(t)
	hostPW := randHex(12)
	hostAddr := "127.0.0.1:" + strconv.Itoa(hostPort)
	hp := hostDev.start("host", "--no-signal", "--listen", hostAddr, "--password", hostPW)
	hp.waitFor(t, regexp.MustCompile(`host ready`), 20*time.Second)

	gwPort := freePort(t)
	var tun *tunnel
	if cf := os.Getenv("REMOHOP_E2E_CLOUDFLARED"); cf != "" {
		tun = cloudflareTunnel(t, cf, gwPort)
	} else {
		if os.Getenv("CI") != "" && os.Getenv("REMOHOP_WEB_E2E_LOCAL_OK") != "1" {
			t.Fatal("REMOHOP_E2E_CLOUDFLARED is not set: on CI this test must go through a real tunnel")
		}
		tun = localTunnel(t, gwPort)
	}

	// The gateway, password-protected, knowing its public address.
	gwPW := randHex(16)
	pwFile := filepath.Join(t.TempDir(), "gateway.pw")
	if err := os.WriteFile(pwFile, []byte(gwPW+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gwDev := newDevice(t, "web-gateway")
	gw := gwDev.start("web", "--listen", "127.0.0.1:"+strconv.Itoa(gwPort), "--password-file", pwFile,
		"--public-url", tun.url, "--no-open")
	gw.waitFor(t, regexp.MustCompile(`RemoHop web gateway: https://`), 15*time.Second)

	c := &webClient{t: t, base: tun.url, origin: strings.TrimSuffix(tun.url, "/"), hc: tun.client()}

	// 1. The page, through the tunnel, with its security headers.
	var resp *http.Response
	var body []byte
	eventually(t, "the page to load through the tunnel", 120*time.Second, func() bool {
		resp, body = c.tryDo("GET", "", nil, nil)
		return resp != nil && resp.StatusCode == 200
	})
	if !bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		t.Errorf("index is not the page (%d bytes)", len(body))
	}
	if h := resp.Header.Get("Strict-Transport-Security"); !strings.Contains(h, "max-age=") {
		t.Errorf("no HSTS through the tunnel: %q", h)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "wss://"+tun.host) || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP through the tunnel does not pin wss to the public host: %s", strings.ReplaceAll(csp, tun.host, "PUBLIC-HOST"))
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("framing/sniffing headers: %q %q", resp.Header.Get("X-Frame-Options"), resp.Header.Get("X-Content-Type-Options"))
	}
	t.Log("page: 200 with HSTS, CSP (wss pinned to the public host), X-Frame-Options DENY")

	// 2. Logins.
	if r, _ := c.do("GET", "api/v1/connections", nil, nil); r.StatusCode != 401 {
		t.Errorf("API without a session: %d", r.StatusCode)
	}
	if r, _ := c.do("POST", "api/v1/login", map[string]string{"Origin": "http://" + tun.host}, map[string]any{"password": gwPW}); r.StatusCode != 403 {
		t.Errorf("login from an http:// origin: %d", r.StatusCode)
	}
	r, b := c.do("POST", "api/v1/login", nil, map[string]any{"password": "wrong-" + randHex(4)})
	if r.StatusCode != 401 || !strings.Contains(string(b), "bad_password") {
		t.Fatalf("wrong password: %d %s", r.StatusCode, b)
	}
	if len(remohopCookies(r)) != 0 {
		t.Errorf("a failed login set a session cookie")
	}
	r, b = c.do("POST", "api/v1/login", nil, map[string]any{"password": gwPW})
	if r.StatusCode != 200 {
		t.Fatalf("login with the right password: %d %s", r.StatusCode, b)
	}
	var sess struct {
		CSRF string `json:"csrf"`
		Mode string `json:"mode"`
	}
	json.Unmarshal(b, &sess)
	if sess.CSRF == "" || sess.Mode != "password" {
		t.Fatalf("login answer: mode %q, csrf set %v", sess.Mode, sess.CSRF != "")
	}
	cookies := remohopCookies(r)
	if len(cookies) != 1 {
		t.Fatalf("%d session cookies set", len(cookies))
	}
	attrs := cookieAttrs(cookies[0])
	if attrs["name"] != "__Host-remohop" || !attrs.has("secure") || !attrs.has("httponly") ||
		!strings.EqualFold(attrs["samesite"], "strict") || attrs["path"] != "/" || attrs.has("domain") {
		delete(attrs, "value")
		t.Fatalf("session cookie attributes: %v", attrs)
	}
	c.cookie = attrs["name"] + "=" + attrs["value"]
	t.Log("login: wrong password 401, right password 200; cookie __Host-remohop; Secure; HttpOnly; SameSite=Strict; Path=/")

	// 3. CSRF.
	if r, _ := c.do("POST", "api/v1/connect", nil, map[string]any{"target": hostAddr, "password": hostPW}); r.StatusCode != 403 {
		t.Errorf("connect without the CSRF token: %d", r.StatusCode)
	}
	c.csrf = sess.CSRF

	// 4. A session to the host.
	r, b = c.do("POST", "api/v1/connect", nil, map[string]any{"target": hostAddr, "password": hostPW})
	if r.StatusCode != 202 {
		t.Fatalf("connect: %d %s", r.StatusCode, b)
	}
	var att struct {
		Attempt string `json:"attempt"`
		State   string `json:"state"`
		Error   *struct{ Code, Message string }
		Conn    *struct {
			Key  string `json:"key"`
			Path string `json:"path"`
		}
	}
	json.Unmarshal(b, &att)
	eventually(t, "the gateway to connect to the host", 60*time.Second, func() bool {
		_, b := c.do("GET", "api/v1/connect/"+att.Attempt, nil, nil)
		json.Unmarshal(b, &att)
		if att.State == "failed" {
			t.Fatalf("connect failed: %+v", att.Error)
		}
		return att.State == "connected"
	})
	t.Logf("gateway connected to the host (%s path)", att.Conn.Path)

	// 5. A terminal over the WebSocket, through the tunnel.
	ticket := func() string {
		_, b := c.do("POST", "api/v1/ticket", nil, map[string]any{"purpose": "ws"})
		var tk struct{ Ticket string }
		json.Unmarshal(b, &tk)
		if tk.Ticket == "" {
			t.Fatalf("no ticket: %s", b)
		}
		return tk.Ticket
	}
	q := url.Values{"t": {ticket()}, "c": {att.Conn.Key}, "new": {"1"}, "rows": {"24"}, "cols": {"80"}}
	if _, err := c.websocket("api/v1/term/ws?"+q.Encode(), "https://evil.example"); err == nil {
		t.Errorf("a WebSocket from a foreign Origin was accepted")
	}
	q.Set("t", ticket())
	ws, err := c.websocket("api/v1/term/ws?"+q.Encode(), c.origin)
	if err != nil {
		t.Fatalf("terminal WebSocket: %s", clip(err.Error()))
	}
	defer ws.Close()
	term := newTermReader(ws)
	if m := term.waitJSON(t, "attached", 30*time.Second); m == nil {
		t.Fatal("no attached message")
	}
	send := func(v any) {
		b, _ := json.Marshal(v)
		if err := websocket.Message.Send(ws, string(b)); err != nil {
			t.Fatalf("ws send: %s", clip(err.Error()))
		}
	}
	send(map[string]any{"type": "resize", "rows": 30, "cols": 100})
	// The shell computes the marker, so the echo of the typed line cannot
	// satisfy the check by itself.
	send(map[string]any{"type": "in", "data": "echo e2e-$((6*7))-marker\r"})
	term.waitOutput(t, "e2e-42-marker", 30*time.Second)
	t.Log("terminal: command typed over the WebSocket ran on the host, output came back through the tunnel")
	send(map[string]any{"type": "in", "data": "exit 0\r"})
	if m := term.waitJSON(t, "exit", 30*time.Second); m["code"] != float64(0) {
		t.Errorf("exit message: %v", m)
	}

	// 6. Logout ends the session.
	if r, _ := c.do("POST", "api/v1/logout", nil, map[string]any{}); r.StatusCode != 200 {
		t.Errorf("logout: %d", r.StatusCode)
	}
	if r, _ := c.do("GET", "api/v1/connections", nil, nil); r.StatusCode != 401 {
		t.Errorf("session still valid after logout: %d", r.StatusCode)
	}
	if strings.Contains(gw.output(), gwPW) || strings.Contains(gw.output(), hostPW) {
		t.Errorf("the gateway printed a password")
	}
}

// ---- the tunnel ----

type tunnel struct {
	url  string // https://HOST/
	host string // HOST (never logged)
	// dial connects to the tunnel's public end for HOST:443.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
	ca   *testCA // the local stand-in's CA; nil: the system roots
}

func (tu *tunnel) tlsConfig() *tls.Config {
	name := tu.host
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	cfg := &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	if tu.ca != nil {
		cfg.RootCAs = tu.ca.pool()
	}
	return cfg
}

func (tu *tunnel) client() *http.Client {
	tr := &http.Transport{DialContext: tu.dial, TLSClientConfig: tu.tlsConfig(), ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// A quick tunnel's name is words joined by dashes; this also keeps
// https://api.trycloudflare.com, which cloudflared may log, from matching.
var trycloudflareRE = regexp.MustCompile(`https://([a-z0-9]+(?:-[a-z0-9]+)+\.trycloudflare\.com)`)

// cloudflareTunnel starts a quick tunnel to 127.0.0.1:port.
func cloudflareTunnel(t *testing.T, bin string, port int) *tunnel {
	t.Helper()
	// trycloudflare.com rate-limits quick tunnels (HTTP 429 from its
	// tunnel store, and cloudflared exits). That is the free service
	// being busy, not a failure of the code under test: wait and retry.
	var p *proc
	var host string
	for attempt := 1; ; attempt++ {
		p = startProc(t, "cloudflared", os.Environ(), bin, "tunnel", "--no-autoupdate", "--url", "http://127.0.0.1:"+strconv.Itoa(port))
		if m, rateLimited := waitQuickTunnel(p, 90*time.Second); m != "" {
			host = m
			break
		} else if !rateLimited || attempt == 4 {
			t.Fatalf("cloudflared did not print a quick tunnel URL (attempt %d):\n%s", attempt, clip(p.output()))
		}
		wait := time.Duration(attempt*20) * time.Second
		t.Logf("cloudflared: trycloudflare.com is rate-limiting quick tunnels (429); retrying in %s", wait)
		time.Sleep(wait)
	}
	redact(host, "PUBLIC-HOST")
	t.Log("cloudflared: quick tunnel up (https://<random>.trycloudflare.com)")
	// Resolve the fresh name through a public resolver: the runner's
	// caching stub may have cached it as nonexistent moments ago.
	var n atomic.Uint32
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, []string{"1.1.1.1:53", "8.8.8.8:53"}[n.Add(1)%2])
	}}
	dialer := &net.Dialer{Timeout: 15 * time.Second, Resolver: resolver}
	t.Cleanup(func() {
		if t.Failed() {
			out := p.output()
			out = strings.ReplaceAll(out, host, "PUBLIC-HOST")
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) > 25 {
				lines = lines[len(lines)-25:]
			}
			t.Logf("cloudflared (tail):\n%s", strings.Join(lines, "\n"))
		}
	})
	return &tunnel{url: "https://" + host + "/", host: host, dial: dialer.DialContext}
}

// waitQuickTunnel waits for cloudflared to print its quick tunnel host.
// It returns the host, or "" and whether cloudflared gave up because the
// service rate-limited it.
func waitQuickTunnel(p *proc, timeout time.Duration) (host string, rateLimited bool) {
	deadline := time.Now().Add(timeout)
	for {
		out := p.output()
		if m := trycloudflareRE.FindStringSubmatch(out); m != nil {
			return m[1], false
		}
		select {
		case <-p.done:
			out = p.output()
			return "", strings.Contains(out, "status 429") || strings.Contains(out, "Too Many Requests")
		default:
		}
		if time.Now().After(deadline) {
			return "", false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// localTunnel is a TLS reverse proxy on loopback standing in for the
// tunnel: it terminates https for desk.e2e.test, forwards with Host and
// Origin unchanged, and adds CF-Connecting-IP as Cloudflare does.
func localTunnel(t *testing.T, port int) *tunnel {
	t.Helper()
	const host = "desk.e2e.test"
	ca := newTestCA(t)
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	rp := httputil.NewSingleHostReverseProxy(target)
	director := rp.Director
	rp.Director = func(r *http.Request) {
		h := r.Host
		director(r)
		r.Host = h
		r.Header.Set("CF-Connecting-IP", "198.51.100.23")
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{ca.issue(t, host)}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rp, ErrorLog: log.New(io.Discard, "", 0)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	pport := ln.Addr().(*net.TCPAddr).Port
	pub := host + ":" + strconv.Itoa(pport)
	t.Log("no REMOHOP_E2E_CLOUDFLARED: a local TLS reverse proxy stands in for the tunnel")
	var d net.Dialer
	return &tunnel{url: "https://" + pub + "/", host: pub, ca: ca,
		dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, ln.Addr().String())
		}}
}

// ---- a minimal browser ----

type webClient struct {
	t            *testing.T
	base, origin string
	hc           *http.Client
	cookie, csrf string
}

func (c *webClient) tryDo(method, path string, hdr map[string]string, body any) (*http.Response, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(clip(err.Error()))
	}
	req.Header.Set("User-Agent", "remohop-e2e")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" {
		req.Header.Set("Origin", c.origin)
		if c.csrf != "" {
			req.Header.Set("X-RemoHop-CSRF", c.csrf)
		}
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp, b
}

func (c *webClient) do(method, path string, hdr map[string]string, body any) (*http.Response, []byte) {
	c.t.Helper()
	for attempt := 0; ; attempt++ {
		resp, b := c.tryDo(method, path, hdr, body)
		// The tunnel's edge can drop a request now and then; retry
		// transport failures and edge errors (52x), not the gateway's.
		if resp != nil && (resp.StatusCode < 520 || resp.StatusCode > 530) {
			return resp, b
		}
		if attempt == 4 {
			if resp == nil {
				c.t.Fatalf("%s %s: no response through the tunnel", method, "/"+strings.SplitN(path, "?", 2)[0])
			}
			return resp, b
		}
		time.Sleep(2 * time.Second)
	}
}

// websocket opens a WebSocket on path with the session cookie and origin.
func (c *webClient) websocket(path, origin string) (*websocket.Conn, error) {
	u, _ := url.Parse(c.base + path)
	wsURL := *u
	wsURL.Scheme = "wss"
	cfg, err := websocket.NewConfig(wsURL.String(), origin)
	if err != nil {
		return nil, err
	}
	cfg.Header.Set("Cookie", c.cookie)
	cfg.Header.Set("User-Agent", "remohop-e2e")
	tr := c.hc.Transport.(*http.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hostport := u.Host
	if u.Port() == "" {
		hostport = net.JoinHostPort(u.Hostname(), "443")
	}
	// A fresh quick-tunnel name can still be negatively cached by one of
	// the public resolvers for a few seconds after the HTTP client's (pooled)
	// connection was made; retry DNS failures for a while.
	var raw net.Conn
	for attempt := 0; ; attempt++ {
		raw, err = tr.DialContext(ctx, "tcp", hostport)
		var dnsErr *net.DNSError
		if err == nil || !errors.As(err, &dnsErr) || attempt >= 12 || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, err
	}
	tcfg := tr.TLSClientConfig.Clone()
	tcfg.NextProtos = []string{"http/1.1"}
	tc := tls.Client(raw, tcfg)
	if err := tc.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	ws, err := websocket.NewClient(cfg, tc)
	if err != nil {
		tc.Close()
		return nil, err
	}
	return ws, nil
}

// remohopCookies are the gateway's Set-Cookie headers of r (a tunnel's edge
// may add cookies of its own, which are ignored).
func remohopCookies(r *http.Response) []string {
	var out []string
	for _, h := range r.Header.Values("Set-Cookie") {
		if name, _, _ := strings.Cut(h, "="); strings.Contains(strings.ToLower(name), "remohop") {
			out = append(out, h)
		}
	}
	return out
}

type cookieMap map[string]string

func (m cookieMap) has(k string) bool { _, ok := m[k]; return ok }

// cookieAttrs parses one Set-Cookie header: "name", "value" and the
// lower-cased attribute names.
func cookieAttrs(h string) cookieMap {
	m := cookieMap{}
	for i, part := range strings.Split(h, ";") {
		part = strings.TrimSpace(part)
		k, v, _ := strings.Cut(part, "=")
		if i == 0 {
			m["name"], m["value"] = k, v
			continue
		}
		m[strings.ToLower(k)] = v
	}
	return m
}

// termReader collects a terminal WebSocket's output and control messages.
type termReader struct {
	mu   sync.Mutex
	out  bytes.Buffer
	msgs []map[string]any
	err  error
}

func newTermReader(ws *websocket.Conn) *termReader {
	r := &termReader{}
	go func() {
		for {
			var frame []byte
			// Message.Receive into []byte takes text and binary frames;
			// text frames are the JSON control messages.
			var msg any
			if err := websocket.Message.Receive(ws, &frame); err != nil {
				r.mu.Lock()
				r.err = err
				r.mu.Unlock()
				return
			}
			r.mu.Lock()
			if json.Unmarshal(frame, &msg) == nil {
				if m, ok := msg.(map[string]any); ok && m["type"] != nil {
					r.msgs = append(r.msgs, m)
					r.mu.Unlock()
					continue
				}
			}
			r.out.Write(frame)
			r.mu.Unlock()
		}
	}()
	return r
}

func (r *termReader) waitJSON(t *testing.T, typ string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, m := range r.msgs {
			if m["type"] == typ {
				r.mu.Unlock()
				return m
			}
			if m["type"] == "error" || m["type"] == "refused" {
				r.mu.Unlock()
				t.Fatalf("terminal: %v", m)
			}
		}
		err := r.err
		r.mu.Unlock()
		if err != nil {
			t.Fatalf("terminal WebSocket ended before %q: %s", typ, clip(err.Error()))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %q message within %s", typ, timeout)
	return nil
}

func (r *termReader) waitOutput(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := r.out.String()
		err := r.err
		r.mu.Unlock()
		if strings.Contains(got, want) {
			return
		}
		if err != nil {
			t.Fatalf("terminal ended before %q appeared: %v\n%s", want, err, clip(got))
		}
		time.Sleep(100 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("%q did not appear within %s; terminal output:\n%s", want, timeout, clip(r.out.String()))
}
