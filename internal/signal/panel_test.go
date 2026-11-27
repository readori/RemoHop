package signal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cfaccess/cfaccesstest"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/nat/vnet"
)

const (
	panelTeam = "example-team.cloudflareaccess.com"
	panelAUD  = "aud-of-the-panel-application"
)

func newTestPanel(t *testing.T, cfg PanelConfig) *panel {
	t.Helper()
	srv, err := NewServer(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8810"
	}
	p, err := newPanel(srv, cfg, newLogRing())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type panelReq struct {
	method, path, remote, host string
	header                     map[string]string
	cookie                     string
	user, pass                 string
}

func (p *panel) do(r panelReq) *httptest.ResponseRecorder {
	if r.method == "" {
		r.method = "GET"
	}
	if r.path == "" {
		r.path = "/api/overview"
	}
	req := httptest.NewRequest(r.method, r.path, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	if r.remote != "" {
		req.RemoteAddr = r.remote
	}
	req.Host = "127.0.0.1:8810"
	if r.host != "" {
		req.Host = r.host
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: r.cookie})
	}
	if r.user != "" || r.pass != "" {
		req.SetBasicAuth(r.user, r.pass)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	return w
}

func accessClaims(mod func(map[string]any)) map[string]any {
	now := time.Now().Unix()
	c := map[string]any{"aud": []string{panelAUD}, "iss": "https://" + panelTeam, "exp": now + 600, "iat": now - 5,
		"email": "Ops@Example.com", "sub": "user-1"}
	if mod != nil {
		mod(c)
	}
	return c
}

func TestPanelLoopbackNeedsNoLogin(t *testing.T) {
	p := newTestPanel(t, PanelConfig{})
	for _, path := range []string{"/", "/panel.js", "/panel.css", "/icon.svg", "/api/overview", "/api/history?range=7d",
		"/api/relay", "/api/limits", "/api/health", "/healthz"} {
		w := p.do(panelReq{path: path})
		if w.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	w := p.do(panelReq{host: "localhost:8810"})
	if w.Code != http.StatusOK {
		t.Fatalf("Host localhost: %d", w.Code)
	}
	h := w.Header()
	for k, want := range map[string]string{"X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store",
		"X-Content-Type-Options": "nosniff"} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "script-src 'self'") ||
		strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP %q", csp)
	}
	var ov struct {
		Viewer viewer `json:"viewer"`
	}
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov.Viewer.Via != "loopback" || ov.Viewer.Email != "" {
		t.Errorf("viewer %+v", ov.Viewer)
	}
}

func TestPanelRefusesOtherHostNames(t *testing.T) {
	// A page on a name that resolves to 127.0.0.1 (DNS rebinding) must not
	// read the panel.
	p := newTestPanel(t, PanelConfig{})
	if w := p.do(panelReq{host: "rebind.attacker.example:8810"}); w.Code != http.StatusForbidden {
		t.Fatalf("rebinding Host: %d", w.Code)
	}
	if w := p.do(panelReq{remote: "192.0.2.7:4000"}); w.Code != http.StatusForbidden {
		t.Fatalf("a request from another machine without a password: %d", w.Code)
	}
}

func TestPanelReadOnly(t *testing.T) {
	p := newTestPanel(t, PanelConfig{})
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if w := p.do(panelReq{method: m}); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, w.Code)
		}
	}
	if w := p.do(panelReq{header: map[string]string{"Sec-Fetch-Site": "cross-site"}}); w.Code != http.StatusForbidden {
		t.Errorf("cross-site API read: %d", w.Code)
	}
	if w := p.do(panelReq{header: map[string]string{"Sec-Fetch-Site": "same-origin"}}); w.Code != http.StatusOK {
		t.Errorf("same-origin API read: %d", w.Code)
	}
}

func TestPanelCloudflareHeadersNeedAToken(t *testing.T) {
	ks := cfaccesstest.New(t)
	p := newTestPanel(t, PanelConfig{CFTeam: panelTeam, CFAUD: panelAUD, CFCertsURL: ks.Server(t)})
	for _, h := range []string{"Cf-Ray", "Cf-Connecting-Ip"} {
		w := p.do(panelReq{header: map[string]string{h: "x"}})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s without a token: %d", h, w.Code)
		}
	}
	// Even on loopback: cloudflared runs on this machine.
	if w := p.do(panelReq{header: map[string]string{"Cf-Ray": "abc-LHR"}, host: "panel.example.com"}); w.Code != http.StatusForbidden {
		t.Errorf("tunnel request without a token: %d", w.Code)
	}
}

func TestPanelCloudflareToken(t *testing.T) {
	ks := cfaccesstest.New(t)
	p := newTestPanel(t, PanelConfig{CFTeam: panelTeam, CFAUD: panelAUD, CFCertsURL: ks.Server(t),
		CFEmails: []string{"ops@example.com"}})
	tok := ks.Sign(t, "k1", accessClaims(nil))
	w := p.do(panelReq{host: "panel.example.com", header: map[string]string{"Cf-Ray": "abc", "Cf-Access-Jwt-Assertion": tok}})
	if w.Code != http.StatusOK {
		t.Fatalf("valid token in the header: %d %s", w.Code, w.Body)
	}
	var ov struct {
		Viewer viewer `json:"viewer"`
	}
	json.Unmarshal(w.Body.Bytes(), &ov)
	if ov.Viewer.Via != "cloudflare" || ov.Viewer.Email != "ops@example.com" {
		t.Errorf("viewer %+v", ov.Viewer)
	}
	if w := p.do(panelReq{host: "panel.example.com", header: map[string]string{"Cf-Connecting-Ip": "198.51.100.9"}, cookie: tok}); w.Code != http.StatusOK {
		t.Fatalf("valid token in the cookie: %d %s", w.Code, w.Body)
	}

	refused := map[string]string{
		"wrong aud":         ks.Sign(t, "k1", accessClaims(func(c map[string]any) { c["aud"] = []string{"other-app"} })),
		"wrong issuer":      ks.Sign(t, "k1", accessClaims(func(c map[string]any) { c["iss"] = "https://other.cloudflareaccess.com" })),
		"expired":           ks.Sign(t, "k1", accessClaims(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() })),
		"unknown kid":       ks.Sign(t, "k-unknown", accessClaims(nil)),
		"email not allowed": ks.Sign(t, "k1", accessClaims(func(c map[string]any) { c["email"] = "intruder@example.com" })),
		"forged signature":  ks.SignWithStranger(t, "k1", accessClaims(nil)),
		"no exp":            ks.Sign(t, "k1", accessClaims(func(c map[string]any) { delete(c, "exp") })),
	}
	for name, tok := range refused {
		w := p.do(panelReq{host: "panel.example.com", header: map[string]string{"Cf-Ray": "abc", "Cf-Access-Jwt-Assertion": tok}})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
}

func TestPanelCloudflareFailsClosed(t *testing.T) {
	// A tunnel to a panel without Access configured must not open it.
	ks := cfaccesstest.New(t)
	p := newTestPanel(t, PanelConfig{})
	tok := ks.Sign(t, "k1", accessClaims(nil))
	for _, h := range []map[string]string{{"Cf-Ray": "abc"}, {"Cf-Connecting-Ip": "198.51.100.9"}, {"Cf-Access-Jwt-Assertion": tok}} {
		if w := p.do(panelReq{header: h}); w.Code != http.StatusForbidden {
			t.Errorf("%v: %d", h, w.Code)
		}
	}
	// /healthz says nothing and stays open.
	if w := p.do(panelReq{path: "/healthz", header: map[string]string{"Cf-Ray": "abc"}}); w.Code != http.StatusOK {
		t.Errorf("/healthz through Cloudflare: %d", w.Code)
	}
}

func TestPanelConfigCheck(t *testing.T) {
	for _, c := range []PanelConfig{
		{Listen: "0.0.0.0:8810"}, {Listen: ":8810"}, {Listen: "[::]:8810"}, {Listen: "192.0.2.1:8810"},
		{Listen: "8810"}, {Listen: "127.0.0.1:8810", CFTeam: panelTeam},
		{Listen: "127.0.0.1:8810", CFAUD: panelAUD}, {Listen: "127.0.0.1:8810", CFEmails: []string{"a@b.c"}},
	} {
		if err := c.check(); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	for _, c := range []PanelConfig{
		{Listen: "127.0.0.1:8810"}, {Listen: "localhost:8810"}, {Listen: "[::1]:8810"},
		{Listen: "0.0.0.0:8810", Password: "x"}, {Listen: "127.0.0.1:8810", CFTeam: panelTeam, CFAUD: panelAUD},
	} {
		if err := c.check(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	env := map[string]string{EnvPanelPassword: "pw", EnvPanelCFTeam: " t.cloudflareaccess.com ", EnvPanelCFAUD: "aud",
		EnvPanelCFEmails: "A@example.com, b@example.com,,"}
	c := panelConfigFromEnv("127.0.0.1:1", func(k string) string { return env[k] })
	if c.Password != "pw" || c.CFTeam != "t.cloudflareaccess.com" || c.CFAUD != "aud" || strings.Join(c.CFEmails, " ") != "a@example.com b@example.com" {
		t.Errorf("from env: %+v", c)
	}
}

func TestPanelNonLoopbackWithoutPasswordRefusesToStart(t *testing.T) {
	t.Setenv(EnvPanelPassword, "")
	ctx := testCtx(t)
	code, _, errOut := runSignalCmd(t, ctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", t.TempDir(), "--panel", "0.0.0.0:0")
	if code != cli.ExitUsage || !strings.Contains(errOut, EnvPanelPassword) {
		t.Fatalf("panel on all interfaces without a password: %d %q", code, errOut)
	}
	t.Setenv(EnvPanelCFTeam, panelTeam)
	code, _, errOut = runSignalCmd(t, ctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", t.TempDir(), "--panel", "127.0.0.1:0")
	if code != cli.ExitUsage || !strings.Contains(errOut, EnvPanelCFAUD) {
		t.Fatalf("Cloudflare team without AUD: %d %q", code, errOut)
	}
}

func TestPanelStartsWithTheServer(t *testing.T) {
	t.Setenv(EnvPanelPassword, "")
	t.Setenv(EnvPanelCFTeam, "")
	t.Setenv(EnvPanelCFAUD, "")
	data := t.TempDir()
	sctx, cancel := context.WithCancel(testCtx(t))
	done := make(chan string, 1)
	go func() {
		_, out, errOut := runSignalCmd(t, sctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", data, "--panel", "127.0.0.1:0")
		done <- out + errOut
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	out := <-done
	if !regexp.MustCompile(`panel: http://127\.0\.0\.1:\d+/ \(loopback only`).MatchString(out) {
		t.Fatalf("no panel line in the output:\n%s", out)
	}
}

func TestPanelBasicAuth(t *testing.T) {
	p := newTestPanel(t, PanelConfig{Listen: "0.0.0.0:8810", Password: "correct horse"})
	w := p.do(panelReq{})
	if w.Code != http.StatusUnauthorized || !strings.HasPrefix(w.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("no password: %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	if w := p.do(panelReq{path: "/", user: "admin", pass: "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	for _, user := range []string{"admin", "", "anyone"} {
		if w := p.do(panelReq{user: user, pass: "correct horse", remote: "192.0.2.7:4000", host: "203.0.113.5:8810"}); w.Code != http.StatusOK {
			t.Fatalf("right password as %q from another machine: %d", user, w.Code)
		}
	}
	if w := p.do(panelReq{path: "/healthz", remote: "192.0.2.7:4000"}); w.Code != http.StatusOK {
		t.Fatalf("/healthz without a password: %d", w.Code)
	}
	// Guessing is slowed down per address.
	for range 20 {
		p.do(panelReq{remote: "192.0.2.66:4000", pass: "guess"})
	}
	if w := p.do(panelReq{remote: "192.0.2.66:4000", pass: "correct horse"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after 20 wrong guesses: %d", w.Code)
	}
	if w := p.do(panelReq{remote: "192.0.2.67:4000", pass: "correct horse"}); w.Code != http.StatusOK {
		t.Fatalf("another address after someone else's guesses: %d", w.Code)
	}
}

func TestPanelPasswordAndCloudflare(t *testing.T) {
	// With both, a request through Cloudflare needs the token and the
	// password.
	ks := cfaccesstest.New(t)
	p := newTestPanel(t, PanelConfig{Password: "pw", CFTeam: panelTeam, CFAUD: panelAUD, CFCertsURL: ks.Server(t)})
	tok := ks.Sign(t, "k1", accessClaims(nil))
	h := map[string]string{"Cf-Ray": "abc", "Cf-Access-Jwt-Assertion": tok}
	if w := p.do(panelReq{header: h}); w.Code != http.StatusUnauthorized {
		t.Fatalf("token without password: %d", w.Code)
	}
	if w := p.do(panelReq{header: h, pass: "pw"}); w.Code != http.StatusOK {
		t.Fatalf("token and password: %d", w.Code)
	}
	if w := p.do(panelReq{header: map[string]string{"Cf-Ray": "abc"}, pass: "pw"}); w.Code != http.StatusForbidden {
		t.Fatalf("password without token through Cloudflare: %d", w.Code)
	}
}

// ipLike finds anything an IPv4 or IPv6 address could be.
func findAddrs(s string) []string {
	var out []string
	for _, m := range addrCandidate.FindAllString(s, -1) {
		if isAddr(m) {
			out = append(out, m)
		}
	}
	return out
}

func TestPanelAPIShowsCountsNotAddresses(t *testing.T) {
	logs := newLogRing()
	logger := slog.New(logs.handler(slog.NewTextHandler(io.Discard, nil)))
	tn := &testNet{t: t, net: vnet.New(), dir: t.TempDir(), logger: logger}
	tn.net.Host("198.51.100.1")
	tn.start(Limits{})
	ctx := testCtx(t)

	var ids []string
	var keys []string
	for i, pub := range []string{"203.0.113.20", "203.0.113.21"} {
		h := tn.net.NAT(pub, vnet.PortRestrictedCone).Host("10.0.0." + string(rune('1'+i)))
		hc := tn.dial(tn.socket(h), Options{})
		id := newIdentity(t)
		reg, err := hc.Register(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		go hc.ServeOffers(ctx, func(Offer) Answer { return Answer{NAT: nat.TypeCone} })
		ids = append(ids, reg.ID)
		keys = append(keys, base64.StdEncoding.EncodeToString(id.Public()))
	}
	cc := tn.dial(tn.socket(tn.net.NAT("203.0.113.30", vnet.PortRestrictedCone).Host("192.168.1.30")), Options{})
	if _, err := cc.Connect(ctx, ids[0], nil, nat.TypeCone); err != nil {
		t.Fatal(err)
	}
	cc.Connect(ctx, "123456789", nil, nat.TypeCone) // unknown

	p, err := newPanel(tn.srv, PanelConfig{Listen: "127.0.0.1:8810", UDP: "0.0.0.0:21116", WS: "127.0.0.1:8080"}, logs)
	if err != nil {
		t.Fatal(err)
	}
	p.sample(time.Now())
	logger.Warn("test warning", "client", "203.0.113.99:4000", "err", "dial udp 198.51.100.77:21116: refused by host 987654321")

	var all strings.Builder
	get := func(path string, v any) {
		w := p.do(panelReq{path: path})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		all.Write(w.Body.Bytes())
		if v != nil {
			if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
	}
	var ov struct {
		Live   map[string]int64       `json:"live"`
		Totals map[string]int64       `json:"totals"`
		Today  map[string]int64       `json:"today"`
		Listen map[string]*listenInfo `json:"listen"`
	}
	get("/api/overview", &ov)
	get("/api/history?range=24h", nil)
	get("/api/history?range=7d", nil)
	get("/api/relay", nil)
	get("/api/limits", nil)
	var health struct {
		Warnings []logRecord `json:"warnings"`
		Info     []logRecord `json:"info"`
	}
	get("/api/health", &health)

	if ov.Live["hosts"] != 2 || ov.Live["ids"] != 2 || ov.Live["conns"] != 3 {
		t.Errorf("live %v, want 2 hosts, 2 IDs, 3 connections", ov.Live)
	}
	if ov.Totals["registrations"] != 2 || ov.Totals["new_ids"] != 2 || ov.Totals["lookups"] != 2 || ov.Totals["introductions"] != 1 {
		t.Errorf("totals %v", ov.Totals)
	}
	if ov.Today["registrations"] != 2 {
		t.Errorf("today %v", ov.Today)
	}
	if l := ov.Listen["udp"]; l == nil || l.Port != 21116 || l.Scope != "all" {
		t.Errorf("udp listen %+v", l)
	}
	if l := ov.Listen["ws"]; l == nil || l.Port != 8080 || l.Scope != "loopback" {
		t.Errorf("ws listen %+v", l)
	}
	if len(health.Warnings) != 1 || len(health.Info) < 2 {
		t.Errorf("health: %d warnings, %d info records", len(health.Warnings), len(health.Info))
	}

	body := all.String()
	if a := findAddrs(body); len(a) > 0 {
		t.Errorf("the API shows addresses: %v", a)
	}
	for _, id := range append(ids, "987654321") {
		if strings.Contains(body, id) {
			t.Errorf("the API shows host ID %s", id)
		}
	}
	for _, k := range keys {
		if strings.Contains(body, k) || strings.Contains(body, strings.TrimRight(k, "=")) {
			t.Errorf("the API shows a key")
		}
	}
	if !strings.Contains(body, "test warning") {
		t.Error("the warning is not in the health records")
	}
}

func TestLiveCountsOfRelayAndWS(t *testing.T) {
	p := newTestPanel(t, PanelConfig{})
	p.srv.stats.add(stRateConnect)
	p.srv.refuseRelay(refusedBudget)
	p.sample(time.Now())
	var lim struct {
		Gauges []struct {
			Key        string
			Value, Max int
		} `json:"gauges"`
		Refused map[string]int64 `json:"refused_24h"`
	}
	json.Unmarshal(p.do(panelReq{path: "/api/limits"}).Body.Bytes(), &lim)
	if lim.Refused["rate_connect"] != 1 || lim.Refused["relay_budget"] != 1 {
		t.Errorf("refusals %v", lim.Refused)
	}
	if len(lim.Gauges) == 0 || lim.Gauges[0].Key != "conns" || lim.Gauges[0].Max != 50000 {
		t.Errorf("gauges %+v", lim.Gauges)
	}
	// The relay's refusal also reaches the hourly summary.
	if _, refused := p.srv.relayCounters.summary(); refused[refusedBudget] != 1 {
		t.Errorf("summary refusals %v", refused)
	}
}
