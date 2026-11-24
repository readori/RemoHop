package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestRefusesNonLoopbackWithoutTLSAndPassword(t *testing.T) {
	testEnv(t)
	cert, key := writeTestCert(t)
	pw := writePasswordFile(t, "correct horse")
	for name, cfg := range map[string]Config{
		"nothing":       {Listen: "0.0.0.0:0"},
		"tls only":      {Listen: "0.0.0.0:0", TLSCert: cert, TLSKey: key},
		"password only": {Listen: "0.0.0.0:0", PasswordFile: pw},
		"bare port":     {Listen: ":0", TLSCert: cert, TLSKey: key},
		"tls half":      {Listen: "127.0.0.1:0", TLSCert: cert},
	} {
		if s, err := New(cfg); err == nil {
			s.Close()
			t.Errorf("%s: started, want refusal", name)
		}
	}
	s, err := New(Config{Listen: "0.0.0.0:0", TLSCert: cert, TLSKey: key, PasswordFile: pw})
	if err != nil {
		t.Fatalf("with TLS and password: %v", err)
	}
	s.Close()
}

func TestPasswordFileChecks(t *testing.T) {
	testEnv(t)
	short := writePasswordFile(t, "short")
	if s, err := New(Config{PasswordFile: short}); err == nil {
		s.Close()
		t.Error("short password accepted")
	}
	if _, err := New(Config{PasswordFile: "/nonexistent/x"}); err == nil {
		t.Error("missing password file accepted")
	}
}

func TestLoopbackAuthMatrix(t *testing.T) {
	testEnv(t)
	s := startServer(t, Config{})
	base := s.URL() // http://127.0.0.1:PORT/TOKEN/
	u, _ := url.Parse(base)
	token := strings.Trim(u.Path, "/")
	hc := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path, host string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("GET", "http://"+u.Host+path, nil)
		if host != "" {
			req.Host = host
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		return r
	}
	if r := get("/"+token+"/", "", nil); r.StatusCode != 200 {
		t.Errorf("index with token: %d", r.StatusCode)
	} else {
		csp := r.Header.Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("CSP lacks %q: %s", want, csp)
			}
		}
		if strings.Contains(csp, "unsafe-inline") && strings.Contains(csp, "script-src 'unsafe") {
			t.Error("CSP allows inline scripts")
		}
		if r.Header.Get("X-Content-Type-Options") != "nosniff" || r.Header.Get("Cache-Control") != "no-store" {
			t.Error("missing hardening headers")
		}
	}
	if r := get("/"+token, "", nil); r.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("token without slash: %d", r.StatusCode)
	}
	for _, p := range []string{"/", "/wrong/", "/" + token + "x/", "/api/v1/connections", "/assets/app.js"} {
		if r := get(p, "", nil); r.StatusCode != 404 {
			t.Errorf("GET %s without the token: %d, want 404", p, r.StatusCode)
		}
	}
	for _, h := range []string{"evil.example:" + u.Port(), "evil.example", "127.0.0.1:1", "localhost:1"} {
		if r := get("/"+token+"/", h, nil); r.StatusCode != 403 {
			t.Errorf("Host %q: %d, want 403", h, r.StatusCode)
		}
	}
	if r := get("/"+token+"/", "localhost:"+u.Port(), nil); r.StatusCode != 200 {
		t.Errorf("Host localhost: %d", r.StatusCode)
	}
	for _, p := range []string{"assets/app.js", "assets/term_core.js", "assets/style.css"} {
		if r := get("/"+token+"/"+p, "", nil); r.StatusCode != 200 {
			t.Errorf("asset %s: %d", p, r.StatusCode)
		}
	}

	c := newLoopbackClient(t, s)
	// no session yet
	if code, _ := c.do("GET", "api/v1/connections", nil, nil); code != 401 {
		t.Errorf("API without session: %d", code)
	}
	// session creation is same-origin JSON only
	if code, _ := c.do("POST", "api/v1/session", map[string]any{}, map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Errorf("cross-origin session: %d", code)
	}
	if code, _ := c.do("POST", "api/v1/session", []byte("sid=x"), nil); code != 415 {
		t.Errorf("non-JSON session: %d", code)
	}
	c.newSession()
	if c.sid == "" || c.csrf == "" {
		t.Fatal("no session")
	}
	if code, _ := c.do("GET", "api/v1/connections", nil, nil); code != 200 {
		t.Errorf("GET with session: %d", code)
	}
	if code, _ := c.do("GET", "api/v1/connections", nil, map[string]string{hdrSession: "bogus"}); code != 401 {
		t.Errorf("bogus session: %d", code)
	}
	// state-changing matrix
	post := func(name string, hdr map[string]string, want int) {
		t.Helper()
		if code, b := c.do("POST", "api/v1/disconnect", map[string]any{"c": "x"}, hdr); code != want {
			t.Errorf("%s: %d (%s), want %d", name, code, b, want)
		}
	}
	post("valid", nil, 200)
	post("no csrf", map[string]string{hdrCSRF: ""}, 403)
	post("wrong csrf", map[string]string{hdrCSRF: "nope"}, 403)
	post("foreign origin", map[string]string{"Origin": "http://evil.example"}, 403)
	post("null origin", map[string]string{"Origin": "null"}, 403)
	post("same host other port", map[string]string{"Origin": "http://127.0.0.1:1"}, 403)
	post("no origin but csrf", map[string]string{"Origin": ""}, 200)
	post("no session", map[string]string{hdrSession: ""}, 401)
	// the CSRF token of one session is no good for another
	c2 := newLoopbackClient(t, s)
	c2.newSession()
	post("other session's csrf", map[string]string{hdrCSRF: c2.csrf}, 403)

	// tickets: single use, session bound, purpose bound
	var tk struct{ Ticket string }
	if code := c.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "ws"}, &tk); code != 200 || tk.Ticket == "" {
		t.Fatal("ticket")
	}
	wsURL := func(ticket string) string {
		return "ws://" + u.Host + "/" + token + "/api/v1/term/ws?c=x&t=" + url.QueryEscape(ticket)
	}
	dialWS := func(ticket, origin string) error {
		cfg, _ := websocket.NewConfig(wsURL(ticket), origin)
		ws, err := websocket.DialConfig(cfg)
		if err == nil {
			ws.Close()
		}
		return err
	}
	good := "http://" + u.Host
	if err := dialWS(tk.Ticket, "http://evil.example"); err == nil {
		t.Error("cross-site WebSocket upgrade accepted")
	}
	// the foreign-origin attempt must not have burnt the ticket... or may
	// have; either way a replay must fail:
	c.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "ws"}, &tk)
	err := dialWS(tk.Ticket, good)
	if err == nil || !strings.Contains(err.Error(), "410") {
		// a valid ticket reaches the handler; "x" is not connected: 410
		t.Logf("first use: %v", err)
	}
	if err := dialWS(tk.Ticket, good); err == nil {
		t.Error("ticket reused")
	}
	if err := dialWS("", good); err == nil {
		t.Error("empty ticket accepted")
	}
	c.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "dl"}, &tk)
	if err := dialWS(tk.Ticket, good); err == nil {
		t.Error("download ticket used for a WebSocket")
	}
}

func TestTicketExpiresAndBindsToSession(t *testing.T) {
	testEnv(t)
	clk := newClock()
	s := startServer(t, Config{Now: clk.Now})
	ws, _ := s.newSession()
	tk, _ := s.issueTicket(ws, "ws")
	clk.Add(ticketLife + time.Second)
	if s.redeemTicket(tk, "ws") != nil {
		t.Error("expired ticket redeemed")
	}
	tk, _ = s.issueTicket(ws, "ws")
	if got := s.redeemTicket(tk, "ws"); got != ws {
		t.Error("fresh ticket not redeemed")
	}
	if s.redeemTicket(tk, "ws") != nil {
		t.Error("ticket redeemed twice")
	}
}

func TestPasswordModeTLS(t *testing.T) {
	testEnv(t)
	cert, key := writeTestCert(t)
	clk := newClock()
	s := startServer(t, Config{Listen: "127.0.0.1:0", TLSCert: cert, TLSKey: key,
		PasswordFile: writePasswordFile(t, "correct horse"), Now: clk.Now})
	if !s.PasswordMode() {
		t.Fatal("not in password mode")
	}
	base := s.URL()
	if !strings.HasPrefix(base, "https://") || strings.Count(strings.TrimPrefix(base, "https://"), "/") != 1 {
		t.Fatalf("URL %q should be https without a token", base)
	}
	hc, jar := tlsClient()
	origin := strings.TrimSuffix(base, "/")
	call := func(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method != "GET" {
			req.Header.Set("Origin", origin)
		}
		for k, v := range hdr {
			if v == "" {
				req.Header.Del(k)
			} else {
				req.Header.Set(k, v)
			}
		}
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, b
	}

	// the page itself is public, the API is not
	if r, _ := call("GET", "", nil, nil); r.StatusCode != 200 || r.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("index: %d hsts=%q", r.StatusCode, r.Header.Get("Strict-Transport-Security"))
	}
	if r, _ := call("POST", "api/v1/session", map[string]any{}, nil); r.StatusCode != 401 {
		t.Errorf("session without login: %d", r.StatusCode)
	}
	if r, _ := call("GET", "api/v1/peers", nil, nil); r.StatusCode != 401 {
		t.Errorf("peers without login: %d", r.StatusCode)
	}
	// Host header must be one the certificate vouches for
	if r, _ := call("GET", "", nil, map[string]string{"Host": "evil.example:" + strings.Split(base, ":")[2][:len(strings.Split(base, ":")[2])-1]}); r != nil {
		// Go's client ignores a Host header map entry; set via req.Host below
	}
	req, _ := http.NewRequest("GET", base, nil)
	req.Host = "evil.example"
	if r, err := hc.Do(req); err != nil || r.StatusCode != 403 {
		t.Errorf("foreign Host: %v %v", r, err)
	}
	req, _ = http.NewRequest("GET", base, nil)
	req.Host = "gateway.test:" + strings.TrimSuffix(strings.Split(base, ":")[2], "/")
	if r, err := hc.Do(req); err != nil || r.StatusCode != 200 {
		t.Errorf("certificate name as Host: %v %v", r, err)
	}

	// login: content type, origin, wrong password
	if r, _ := call("POST", "api/v1/login", map[string]any{"password": "x"}, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != 403 {
		t.Errorf("cross-origin login: %d", r.StatusCode)
	}
	if r, _ := call("POST", "api/v1/login", map[string]any{"password": "x"}, map[string]string{"Content-Type": "text/plain"}); r.StatusCode != 415 {
		t.Errorf("text/plain login: %d", r.StatusCode)
	}
	for i := 0; i < freeFailures; i++ {
		if r, _ := call("POST", "api/v1/login", map[string]any{"password": "wrong"}, nil); r.StatusCode != 401 {
			t.Fatalf("wrong password: %d", r.StatusCode)
		}
	}
	// one more failure starts the lockout...
	if r, _ := call("POST", "api/v1/login", map[string]any{"password": "wrong"}, nil); r.StatusCode != 401 {
		t.Fatalf("4th failure: %d", r.StatusCode)
	}
	// ... during which even the right password is refused unlooked-at
	r, _ := call("POST", "api/v1/login", map[string]any{"password": "correct horse"}, nil)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("login during lockout: %d", r.StatusCode)
	}
	if len(jar.Cookies(mustURL(base))) != 0 {
		t.Fatal("cookie set during lockout")
	}
	clk.Add(baseLockout + time.Second)
	r, body := call("POST", "api/v1/login", map[string]any{"password": "correct horse"}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("login after lockout: %d %s", r.StatusCode, body)
	}
	var sess struct {
		CSRF, Mode string
		SID        string
	}
	json.Unmarshal(body, &sess)
	if sess.Mode != "password" || sess.CSRF == "" || sess.SID != "" {
		t.Errorf("session body %s (the id must travel in the cookie only)", body)
	}
	var ck *http.Cookie
	for _, c := range r.Cookies() {
		ck = c
	}
	if ck == nil || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Name != "__Host-remohop" || ck.Path != "/" {
		t.Fatalf("session cookie %+v", ck)
	}

	csrf := map[string]string{hdrCSRF: sess.CSRF}
	if r, _ := call("GET", "api/v1/peers", nil, nil); r.StatusCode != 200 {
		t.Errorf("GET with cookie: %d", r.StatusCode)
	}
	pc := func(name string, hdr map[string]string, want int) {
		t.Helper()
		if r, b := call("POST", "api/v1/ticket", map[string]any{"purpose": "ws"}, hdr); r.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", name, r.StatusCode, b, want)
		}
	}
	pc("cookie + csrf", csrf, 200)
	pc("cookie, no csrf", nil, 403)
	pc("cookie, wrong csrf", map[string]string{hdrCSRF: "x"}, 403)
	pc("foreign origin", map[string]string{hdrCSRF: sess.CSRF, "Origin": "https://evil.example"}, 403)
	// no cookie: another client with a stolen CSRF token
	other, _ := tlsClient()
	b, _ := json.Marshal(map[string]any{"purpose": "ws"})
	rq, _ := http.NewRequest("POST", base+"api/v1/ticket", bytes.NewReader(b))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("Origin", origin)
	rq.Header.Set(hdrCSRF, sess.CSRF)
	if rr, err := other.Do(rq); err != nil || rr.StatusCode != 401 {
		t.Errorf("csrf token without the cookie: %v %v", rr, err)
	}

	// WebSocket: needs the cookie and the ticket and the origin
	_, tb := call("POST", "api/v1/ticket", map[string]any{"purpose": "ws"}, csrf)
	var tk struct{ Ticket string }
	json.Unmarshal(tb, &tk)
	wsu := "wss://" + strings.TrimPrefix(origin, "https://") + "/api/v1/term/ws?c=x&t=" + tk.Ticket
	cfg, _ := websocket.NewConfig(wsu, origin)
	cfg.TlsConfig = hc.Transport.(*http.Transport).TLSClientConfig
	if _, err := websocket.DialConfig(cfg); err == nil {
		t.Errorf("ws without cookie: %v", err) // dial does not carry the jar
	}
	// logout kills the session
	if r, _ := call("POST", "api/v1/logout", map[string]any{}, csrf); r.StatusCode != 200 {
		t.Errorf("logout: %d", r.StatusCode)
	}
	if r, _ := call("GET", "api/v1/peers", nil, nil); r.StatusCode != 401 {
		t.Errorf("after logout: %d", r.StatusCode)
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	for i := 0; i < freeFailures; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatal("locked too early")
		}
		l.fail("a")
	}
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("locked after the free failures")
	}
	l.fail("a")
	ok, wait := l.allow("a")
	if ok || wait <= 0 || wait > baseLockout {
		t.Fatalf("first lockout: %v %v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Error("other source affected")
	}
	now = now.Add(baseLockout + time.Second)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("still locked after the lockout")
	}
	l.fail("a")
	if _, wait = l.allow("a"); wait < 2*baseLockout-time.Second {
		t.Errorf("lockout did not double: %v", wait)
	}
	for i := 0; i < 40; i++ {
		l.fail("x")
		now = now.Add(time.Hour)
		l.success("x")
	}
	// global cap: many sources failing at once
	l2 := newLimiter(func() time.Time { return now })
	for i := 0; i < globalBurst; i++ {
		l2.fail("s" + string(rune('A'+i)))
	}
	if ok, _ := l2.allow("fresh"); ok {
		t.Error("global failure budget not enforced")
	}
	now = now.Add(globalRefill * 2)
	if ok, _ := l2.allow("fresh"); !ok {
		t.Error("global budget did not refill")
	}
	if srcKey("[2001:db8::1]:5") != srcKey("[2001:db8::2]:6") || srcKey("1.2.3.4:5") == srcKey("1.2.3.5:5") {
		t.Error("source keys")
	}
}

func TestHostMatcher(t *testing.T) {
	m := newHostMatcher(8443, true, "localhost", "127.0.0.1", "::1", "Gateway.Example.com", "*.lab.example")
	for h, want := range map[string]bool{
		"localhost:8443": true, "127.0.0.1:8443": true, "[::1]:8443": true, "gateway.example.com:8443": true,
		"GATEWAY.example.com:8443": true, "x.lab.example:8443": true, "a.b.lab.example:8443": false,
		"lab.example:8443": false, "evil.com:8443": false, "localhost:80": false, "localhost": false,
		"127.0.0.1.evil.com:8443": false,
	} {
		if got := m.ok(h); got != want {
			t.Errorf("ok(%q) = %v, want %v", h, got, want)
		}
	}
	if !newHostMatcher(443, true, "a.test").ok("a.test") {
		t.Error("default https port")
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:0": true, "[::1]:80": true, "localhost:1": true, "0.0.0.0:1": false, ":80": false,
		"[::]:80": false, "192.0.2.1:1": false, "127.5.5.5:9": true,
	} {
		got, err := isLoopbackAddr(addr)
		if err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", addr, got, err, want)
		}
	}
	if _, err := isLoopbackAddr("nonsense"); err == nil {
		t.Error("bad address accepted")
	}
}
