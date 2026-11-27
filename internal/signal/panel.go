package signal

// The web panel (`remohop signal --panel ADDR`): a read-only dashboard of
// the server for its operator, served from the binary (panelui/).
//
// Who may see it:
//
//   - By default it listens on loopback, and a request from loopback that
//     did not come through Cloudflare needs no login: the operator reaches
//     it over an SSH tunnel (ssh -L 8810:127.0.0.1:8810 server). Such a
//     request must name a loopback host, so that a web page cannot reach
//     the panel by DNS rebinding.
//   - A request that came through Cloudflare (it carries Cf-Ray,
//     Cf-Connecting-Ip or Cf-Access-Jwt-Assertion) must carry a valid
//     Cloudflare Access token for the configured team and application
//     (internal/cfaccess), from an allowed email if a list is set. Without
//     REMOHOP_PANEL_CF_TEAM and REMOHOP_PANEL_CF_AUD such requests are
//     refused: a tunnel without Access in front never opens the panel.
//   - With REMOHOP_PANEL_PASSWORD set, every request except /healthz needs
//     HTTP Basic authentication with it (any user name). Listening beyond
//     loopback is refused without it.
//
// The panel shows counts and aggregates only. It never lists an address, a
// host ID, an account's email or a key, as the server's own summaries do
// not; log records are scrubbed of them (panel_log.go). It is read-only:
// only GET and HEAD are served, and the API refuses requests a browser
// marks as cross-site.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/cfaccess"
	"github.com/readori/RemoHop/internal/cli"
)

//go:embed panelui
var panelFiles embed.FS

// Environment variables of the panel; secrets never go on a command line.
const (
	EnvPanelPassword = "REMOHOP_PANEL_PASSWORD"
	EnvPanelCFTeam   = "REMOHOP_PANEL_CF_TEAM"
	EnvPanelCFAUD    = "REMOHOP_PANEL_CF_AUD"
	EnvPanelCFEmails = "REMOHOP_PANEL_CF_EMAILS"
)

// PanelConfig configures the web panel.
type PanelConfig struct {
	Listen   string
	Password string
	// Cloudflare Access: the team domain, the application's AUD tag and
	// the allowed emails (empty: any the Access policy lets in).
	CFTeam   string
	CFAUD    string
	CFEmails []string
	// CFCertsURL replaces https://TEAM/cdn-cgi/access/certs (tests).
	CFCertsURL string

	// What the overview says about the server's other sockets.
	UDP, Alt, WS, RelayPorts string
}

// panelConfigFromEnv reads the panel's settings from the environment.
func panelConfigFromEnv(listen string, getenv func(string) string) PanelConfig {
	c := PanelConfig{Listen: listen, Password: getenv(EnvPanelPassword), CFTeam: strings.TrimSpace(getenv(EnvPanelCFTeam)),
		CFAUD: strings.TrimSpace(getenv(EnvPanelCFAUD))}
	for _, e := range strings.Split(getenv(EnvPanelCFEmails), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			c.CFEmails = append(c.CFEmails, e)
		}
	}
	return c
}

// check refuses a configuration that would expose the panel.
func (c PanelConfig) check() error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return fmt.Errorf("--panel %q: want HOST:PORT, e.g. 127.0.0.1:8810", c.Listen)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("--panel %q: bad port", c.Listen)
	}
	if !loopbackListen(c.Listen) && c.Password == "" {
		where := host
		if where == "" {
			where = "all interfaces"
		}
		return fmt.Errorf("--panel %s listens beyond loopback (%s); set %s to require a password there, "+
			"or keep it on 127.0.0.1 and reach it over an SSH tunnel or a Cloudflare Tunnel with Access", c.Listen, where, EnvPanelPassword)
	}
	if (c.CFTeam == "") != (c.CFAUD == "") {
		return fmt.Errorf("Cloudflare Access needs both %s and %s", EnvPanelCFTeam, EnvPanelCFAUD)
	}
	if len(c.CFEmails) > 0 && c.CFTeam == "" {
		return fmt.Errorf("%s needs %s and %s", EnvPanelCFEmails, EnvPanelCFTeam, EnvPanelCFAUD)
	}
	return nil
}

// panel is the running web panel.
type panel struct {
	srv  *Server
	cfg  PanelConfig
	cf   *cfaccess.Verifier // nil: Cloudflare requests are refused
	hist *history
	logs *logRing // nil: no log records
	pass [32]byte // SHA-256 of the password
	mux  *http.ServeMux
	now  func() time.Time

	mu   sync.Mutex
	prev [numStats]int64 // the counters at the last sample

	fails *rateTable[netip.Addr] // wrong passwords per client
}

// newPanel makes the panel's handler. logs may be nil.
func newPanel(srv *Server, cfg PanelConfig, logs *logRing) (*panel, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	p := &panel{srv: srv, cfg: cfg, logs: logs, now: time.Now, mux: http.NewServeMux(),
		fails: newRateTable[netip.Addr](10, 6*time.Second)}
	if cfg.Password != "" {
		p.pass = sha256.Sum256([]byte(cfg.Password))
	}
	if cfg.CFTeam != "" {
		v, err := cfaccess.New(cfaccess.Config{Team: cfg.CFTeam, AUD: cfg.CFAUD, Emails: cfg.CFEmails, CertsURL: cfg.CFCertsURL})
		if err != nil {
			return nil, fmt.Errorf("%s: %v", EnvPanelCFTeam, err)
		}
		p.cf = v
	}
	h, err := loadHistory(srv.DataDir(), time.Now())
	if err != nil && h == nil {
		return nil, err
	}
	if err != nil {
		srv.log.Warn("panel history", "err", err)
	}
	p.hist = h
	// p.prev starts at zero: the first sample counts everything since the
	// server started.

	static, _ := fs.Sub(panelFiles, "panelui")
	file := func(name, ctype string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := fs.ReadFile(static, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", ctype)
			w.Write(b)
		}
	}
	p.mux.HandleFunc("GET /{$}", file("index.html", "text/html; charset=utf-8"))
	p.mux.HandleFunc("GET /panel.css", file("panel.css", "text/css; charset=utf-8"))
	p.mux.HandleFunc("GET /panel.js", file("panel.js", "text/javascript; charset=utf-8"))
	p.mux.HandleFunc("GET /icon.svg", file("icon.svg", "image/svg+xml"))
	api := func(pattern string, f func(r *http.Request) any) {
		p.mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			// Read-only, but still not for other sites' pages.
			if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" && s != "none" {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(f(r))
		})
	}
	api("/api/overview", p.apiOverview)
	api("/api/history", p.apiHistory)
	api("/api/relay", p.apiRelay)
	api("/api/limits", p.apiLimits)
	api("/api/health", p.apiHealth)
	return p, nil
}

// startPanel serves the panel until ctx ends or stop is called, and
// samples the server every minute meanwhile.
func startPanel(ctx context.Context, srv *Server, cfg PanelConfig, logs *logRing, out io.Writer) (stop func(), err error) {
	p, err := newPanel(srv, cfg, logs)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("--panel: %w", err)
	}
	hs := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			srv.log.Error("panel stopped", "err", err)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.run(ctx)
	}()
	fmt.Fprintf(out, "panel: http://%s/ (%s)\n", ln.Addr(), p.accessLine())
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			hs.Close()
			<-done
			// What happened since the last sample counts too.
			p.sample(time.Now())
			if err := p.hist.save(); err != nil {
				srv.log.Error("cannot save the panel history", "err", err)
			}
		})
	}, nil
}

func (p *panel) accessLine() string {
	var parts []string
	if loopbackListen(p.cfg.Listen) {
		parts = append(parts, "loopback only")
	}
	if p.cfg.Password != "" {
		parts = append(parts, "password required")
	}
	if p.cf != nil {
		parts = append(parts, "Cloudflare Access for "+p.cfg.CFTeam)
	} else {
		parts = append(parts, "requests through Cloudflare refused: no Access configured")
	}
	return strings.Join(parts, ", ")
}

// run samples once a minute, on the minute, and saves every few minutes.
func (p *panel) run(ctx context.Context) {
	lastSave := time.Now()
	for {
		now := time.Now()
		next := now.Truncate(fineStep).Add(fineStep)
		t := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		p.sample(time.Now())
		if time.Since(lastSave) >= historySave {
			lastSave = time.Now()
			if err := p.hist.save(); err != nil {
				p.srv.log.Error("cannot save the panel history", "err", err)
			}
		}
	}
}

// sample records the last minute: the gauges now and how far each counter
// moved.
func (p *panel) sample(now time.Time) {
	live := p.srv.liveCounts()
	// p.mu is held until the sample is in the history, so that today()
	// never sees the counters moved on but the sample not yet added.
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.srv.stats.snapshot()
	prev := p.prev
	p.prev = cur
	s := sample{T: now.Add(-fineStep / 2).Truncate(fineStep).Unix(), Hosts: live.Hosts, Conns: live.Conns, WS: live.WS, Relays: live.Relays}
	for i, v := range cur {
		if d := v - prev[i]; d > 0 {
			if s.C == nil {
				s.C = map[string]int64{}
			}
			s.C[statNames[i]] = d
		}
	}
	p.hist.add(s)
}

// ---- HTTP -------------------------------------------------------------

type viewer struct {
	Via   string `json:"via"` // loopback, password or cloudflare
	Email string `json:"email,omitempty"`
}

type viewerKey struct{}

func setPanelHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
}

func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPanelHeaders(w)
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "ok\n")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// The panel is read-only; a form or script elsewhere has nothing
		// to post to.
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	v, ok := p.authorize(w, r)
	if !ok {
		return
	}
	p.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewerKey{}, v)))
}

// authorize lets a request in or answers it with a refusal.
func (p *panel) authorize(w http.ResponseWriter, r *http.Request) (viewer, bool) {
	var v viewer
	remote := remoteIP(r)
	if cfaccess.FromCloudflare(r) {
		if p.cf == nil {
			http.Error(w, "This panel does not accept requests through Cloudflare: Cloudflare Access is not configured "+
				"("+EnvPanelCFTeam+" and "+EnvPanelCFAUD+").", http.StatusForbidden)
			return v, false
		}
		c, err := p.cf.Verify(r.Context(), cfaccess.TokenFrom(r))
		if err != nil {
			p.srv.log.Debug("panel: Cloudflare Access token refused", "err", err)
			http.Error(w, "Cloudflare Access: sign in through your Access application first.", http.StatusForbidden)
			return v, false
		}
		v = viewer{Via: "cloudflare", Email: c.Email}
	} else {
		if !remote.IsLoopback() && p.cfg.Password == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return v, false
		}
		if p.cfg.Password == "" && !loopbackHost(r.Host) {
			// DNS rebinding: a page on some other name that resolves to
			// 127.0.0.1 must not read the panel.
			http.Error(w, "Open the panel as http://127.0.0.1:PORT/ or http://localhost:PORT/.", http.StatusForbidden)
			return v, false
		}
		v = viewer{Via: "loopback"}
	}
	if p.cfg.Password != "" {
		if ok, _ := p.fails.peek(remote); !ok {
			http.Error(w, "too many wrong passwords; try again in a minute", http.StatusTooManyRequests)
			return v, false
		}
		_, pw, _ := r.BasicAuth()
		sum := sha256.Sum256([]byte(pw))
		if subtle.ConstantTimeCompare(sum[:], p.pass[:]) != 1 {
			if pw != "" {
				p.fails.allow(remote)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="RemoHop signal panel", charset="UTF-8"`)
			http.Error(w, "password required", http.StatusUnauthorized)
			return v, false
		}
		if v.Via == "loopback" {
			v.Via = "password"
		}
	}
	return v, true
}

func remoteIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// loopbackHost reports whether a Host header names this machine's
// loopback.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// ---- what the server holds now ----------------------------------------

// liveCounts is the server's state at one moment, read under its locks.
type liveCounts struct {
	Hosts, Conns, Relays, WS int
	IDs                      int
	MaxConnsSource           int // the most connections one source has
	MaxConnsNet              int
	MaxRelaySource           int
	MaxRelayHost             int
	MaxWSSource              int
	ExtActive                int64
	Accounts, Devices        int // -1 without accounts
}

func (s *Server) liveCounts() liveCounts {
	var l liveCounts
	s.mu.Lock()
	l.Hosts, l.Conns, l.Relays = len(s.online), s.conns, s.relays
	for _, n := range s.perSource {
		l.MaxConnsSource = max(l.MaxConnsSource, n)
	}
	for _, n := range s.perNet {
		l.MaxConnsNet = max(l.MaxConnsNet, n)
	}
	for _, n := range s.relaySources {
		l.MaxRelaySource = max(l.MaxRelaySource, n)
	}
	for _, n := range s.relayHosts {
		l.MaxRelayHost = max(l.MaxRelayHost, n)
	}
	s.mu.Unlock()
	s.ws.mu.Lock()
	l.WS = s.ws.conns
	for _, n := range s.ws.perSource {
		l.MaxWSSource = max(l.MaxWSSource, n)
	}
	s.ws.mu.Unlock()
	l.IDs = s.store.count()
	l.ExtActive = s.extActive.Load()
	l.Accounts, l.Devices = -1, -1
	if s.accounts != nil {
		st := s.accounts.store
		st.mu.Lock()
		l.Accounts, l.Devices = len(st.byEmail), len(st.byMember)
		st.mu.Unlock()
	}
	return l
}

// ---- API --------------------------------------------------------------

type listenInfo struct {
	Port  int    `json:"port"`
	Scope string `json:"scope"` // all, loopback or address (one address of this machine; not shown)
}

// describeListen tells what a listen address reaches without showing the
// address itself: the panel shows none.
func describeListen(a string) *listenInfo {
	if a == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return nil
	}
	n, _ := strconv.Atoi(port)
	li := &listenInfo{Port: n, Scope: "address"}
	if ip, err := netip.ParseAddr(host); host == "" || (err == nil && ip.IsUnspecified()) {
		li.Scope = "all"
	} else if loopbackHost(host) {
		li.Scope = "loopback"
	}
	return li
}

func totalsMap(v [numStats]int64) map[string]int64 {
	m := make(map[string]int64, numStats)
	for i, n := range v {
		m[statNames[i]] = n
	}
	return m
}

// today returns today's counter totals: what the history recorded plus
// what has not been sampled yet.
func (p *panel) today(now time.Time) map[string]int64 {
	p.mu.Lock()
	d := p.hist.day(dayOf(now))
	cur := p.srv.stats.snapshot()
	prev := p.prev
	p.mu.Unlock()
	if d == nil {
		d = map[string]int64{}
	}
	for i, v := range cur {
		if delta := v - prev[i]; delta > 0 {
			d[statNames[i]] += delta
		}
	}
	return d
}

type memInfo struct {
	RSS        uint64 `json:"rss"`
	RSSExact   bool   `json:"rss_exact"` // false: the Go runtime's own total, the best estimate without /proc
	Heap       uint64 `json:"heap"`
	Goroutines uint64 `json:"goroutines"`
	Limit      int64  `json:"limit,omitempty"` // the GC's soft limit (GOMEMLIMIT), if any
}

func readMemory() memInfo {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/memory/classes/heap/released:bytes"},
		{Name: "/sched/goroutines:goroutines"},
	}
	metrics.Read(samples)
	u := func(i int) uint64 {
		if samples[i].Value.Kind() == metrics.KindUint64 {
			return samples[i].Value.Uint64()
		}
		return 0
	}
	m := memInfo{Heap: u(0), Goroutines: u(3)}
	if total, rel := u(1), u(2); total > rel {
		m.RSS = total - rel
	}
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 1 {
			if pages, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				m.RSS, m.RSSExact = pages*uint64(os.Getpagesize()), true
			}
		}
	}
	if l := debug.SetMemoryLimit(-1); l != math.MaxInt64 {
		m.Limit = l
	}
	return m
}

// budgetState names what the relay budget does to new sessions now.
func budgetState(u *relayUsage, used int64) string {
	if u == nil || u.budget <= 0 {
		return "none"
	}
	switch u.level(used) {
	case 110:
		return "all_refused"
	case 100:
		return "anonymous_refused"
	case 80:
		return "half_rate"
	}
	return "normal"
}

func (p *panel) apiOverview(r *http.Request) any {
	now := p.now()
	s := p.srv
	live := s.liveCounts()
	v, _ := r.Context().Value(viewerKey{}).(viewer)
	out := map[string]any{
		"now":      now.Unix(),
		"version":  cli.Version,
		"started":  s.started.Unix(),
		"uptime_s": int64(now.Sub(s.started).Seconds()),
		"viewer":   v,
		"listen": map[string]*listenInfo{"udp": describeListen(p.cfg.UDP), "alt": describeListen(p.cfg.Alt),
			"ws": describeListen(p.cfg.WS), "panel": describeListen(p.cfg.Listen)},
		"relay_ports": p.cfg.RelayPorts,
		"features":    map[string]bool{"relay": s.cfg.Relay != nil, "accounts": s.accounts != nil, "ws": p.cfg.WS != ""},
		"live": map[string]any{"hosts": live.Hosts, "ids": live.IDs, "conns": live.Conns, "ws": live.WS, "relays": live.Relays,
			"ext_active": live.ExtActive, "max_conns": s.cfg.Limits.MaxConns, "max_ids": s.cfg.Limits.MaxIDs},
		"totals": totalsMap(s.stats.snapshot()),
		"today":  p.today(now),
		"memory": readMemory(),
	}
	if live.Accounts >= 0 {
		out["accounts"] = map[string]int{"accounts": live.Accounts, "devices": live.Devices}
	}
	if rc := s.cfg.Relay; rc != nil {
		rel := map[string]any{"active": live.Relays, "max": rc.MaxSessions}
		if u := s.relayUsage; u != nil {
			used := u.current()
			rel["month_bytes"], rel["budget_bytes"], rel["budget_state"] = used, u.budget, budgetState(u, used)
		}
		out["relay"] = rel
	}
	_, pts := p.hist.series("24h")
	out["recent"] = tail(pts, 60)
	return out
}

func tail[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (p *panel) apiHistory(r *http.Request) any {
	rng := r.URL.Query().Get("range")
	if rng != "7d" {
		rng = "24h"
	}
	step, pts := p.hist.series(rng)
	return map[string]any{"range": rng, "step": int(step.Seconds()), "now": p.now().Unix(), "points": pts}
}

func (p *panel) apiRelay(r *http.Request) any {
	s := p.srv
	now := p.now().UTC()
	rc := s.cfg.Relay
	out := map[string]any{"on": rc != nil, "now": now.Unix()}
	today := p.today(now)
	totals := s.stats.snapshot()
	refusedTotal, refusedToday := map[string]int64{}, map[string]int64{}
	for _, k := range []statKey{stRelayPerSource, stRelayMode, stRelayDeny, stRelayBudget, stRelayFull} {
		name := strings.TrimPrefix(statNames[k], "relay_")
		refusedTotal[name] = totals[k]
		refusedToday[name] = today[statNames[k]]
	}
	out["refused"] = map[string]any{"total": refusedTotal, "today": refusedToday}
	out["sessions"] = map[string]int64{"total": totals[stRelaySessions], "today": today[statNames[stRelaySessions]]}
	out["bytes"] = map[string]int64{"total": totals[stRelayBytes], "today": today[statNames[stRelayBytes]]}
	if rc == nil {
		return out
	}
	live := s.liveCounts()
	pol := map[string]any{
		"mode": "all", "rate_kbps": rc.Rate / 125, "account_kbps": rc.AccountRate / 125, "total_kbps": rc.TotalRate / 125,
		"max_sessions": rc.MaxSessions, "idle_s": int(rc.Idle.Seconds()), "ports": p.cfg.RelayPorts,
		"deny_list": rc.DenyFile != "", "monthly_budget_bytes": rc.MonthlyBytes,
	}
	if rc.ForAccounts {
		pol["mode"] = "accounts"
	}
	if rc.PerSource > 0 && rc.PerSource < int(^uint(0)>>1) {
		pol["per_source"], pol["host_cap"] = rc.PerSource, hostRelayCap(rc.PerSource)
	}
	if d := s.relayDeny.list(); d != nil {
		pol["deny_entries"] = len(d.prefixes) + len(d.keys)
	}
	out["policy"] = pol
	out["active"] = live.Relays
	if u := s.relayUsage; u != nil {
		used := u.current()
		usage := map[string]any{"month": monthOf(now), "bytes": used, "budget": u.budget, "state": budgetState(u, used)}
		if u.budget > 0 {
			usage["pct"] = float64(used) * 100 / float64(u.budget)
		}
		out["usage"] = usage
	}
	// This month by day: what the panel recorded, and for today also what
	// it has not sampled yet. Days before the panel ran are not recorded.
	_, days := p.hist.dayList()
	var month []map[string]any
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for d := first; !d.After(now); d = d.AddDate(0, 0, 1) {
		key := dayOf(d)
		c, rec := days[key]
		if key == dayOf(now) {
			c, rec = today, true
		}
		month = append(month, map[string]any{"day": key, "recorded": rec,
			"bytes": c[statNames[stRelayBytes]], "sessions": c[statNames[stRelaySessions]]})
	}
	out["days"] = month
	return out
}

func (p *panel) apiLimits(r *http.Request) any {
	s := p.srv
	l := s.cfg.Limits
	live := s.liveCounts()
	_, pts := p.hist.series("24h")
	refused24 := map[string]int64{}
	for _, pt := range pts {
		for k, v := range pt.C {
			refused24[k] += v
		}
	}
	gauge := func(key string, v, max int) map[string]any {
		return map[string]any{"key": key, "value": v, "max": max}
	}
	gauges := []map[string]any{
		gauge("conns", live.Conns, l.MaxConns),
		gauge("conns_source", live.MaxConnsSource, l.MaxConnsPerSource),
		gauge("conns_net", live.MaxConnsNet, l.MaxConnsPerNet),
		gauge("ids", live.IDs, l.MaxIDs),
		gauge("ext_active", int(live.ExtActive), s.cfg.Ext.MaxConcurrent),
	}
	if rc := s.cfg.Relay; rc != nil {
		gauges = append(gauges, gauge("relays", live.Relays, rc.MaxSessions))
		if rc.PerSource > 0 && rc.PerSource < int(^uint(0)>>1) {
			gauges = append(gauges, gauge("relay_source", live.MaxRelaySource, rc.PerSource),
				gauge("relay_host", live.MaxRelayHost, hostRelayCap(rc.PerSource)))
		}
	}
	var ws map[string]any
	if c := s.ws.cfg.Load(); c != nil {
		gauges = append(gauges, gauge("ws", live.WS, c.MaxConns), gauge("ws_source", live.MaxWSSource, c.MaxConnsPerSource))
		ws = map[string]any{"max_conns": c.MaxConns, "max_conns_per_source": c.MaxConnsPerSource, "rate": c.Rate,
			"total_rate": c.TotalRate, "idle_s": int(c.Idle.Seconds()), "real_ip_header": c.RealIPHeader != ""}
	}
	rate := func(key string, burst int, every time.Duration, refused ...statKey) map[string]any {
		var n int64
		for _, k := range refused {
			n += refused24[statNames[k]]
		}
		return map[string]any{"key": key, "burst": burst, "every_ms": every.Milliseconds(), "refused_24h": n}
	}
	ext := s.cfg.Ext
	rates := []map[string]any{
		rate("register", l.RegisterBurst, l.RegisterEvery, stRateRegister),
		rate("new_id", l.NewIDBurst, l.NewIDEvery, stRateNewID),
		rate("new_id_net", l.NewIDNetBurst, l.NewIDNetEvery),
		rate("new_id_all", l.NewIDGlobalBurst, l.NewIDGlobalEvery),
		rate("connect", l.ConnectBurst, l.ConnectEvery, stRateConnect),
		rate("connect_net", l.ConnectNetBurst, l.ConnectNetEvery),
		rate("miss", l.MissBurst, l.MissEvery, stRateMiss),
		rate("offer", l.OfferBurst, l.OfferEvery, stRateOffer),
		rate("ext", ext.Burst, time.Second/time.Duration(ext.RefillPerSecond), stExtLimited),
	}
	return map[string]any{
		"gauges": gauges,
		"rates":  rates,
		"limits": map[string]any{"max_conns": l.MaxConns, "max_conns_per_source": l.MaxConnsPerSource,
			"max_conns_per_net": l.MaxConnsPerNet, "max_ids": l.MaxIDs, "id_expiry_days": int(l.IDExpiry.Hours() / 24),
			"client_lifetime_s": int(l.ClientLifetime.Seconds())},
		"ext": map[string]any{"burst": ext.Burst, "refill_per_second": ext.RefillPerSecond, "max_concurrent": ext.MaxConcurrent,
			"per_source_concurrent": ext.PerSourceConcurrent},
		"ws":          ws,
		"refused_24h": refused24,
	}
}

func (p *panel) apiHealth(r *http.Request) any {
	var warn, info []logRecord
	if p.logs != nil {
		warn, info = p.logs.records()
	}
	hour := p.now().Add(-time.Hour).UnixMilli()
	var errs, warns int
	for _, rec := range warn {
		if rec.T >= hour {
			if rec.Level == "error" {
				errs++
			} else {
				warns++
			}
		}
	}
	return map[string]any{"warnings": warn, "info": info, "errors_1h": errs, "warnings_1h": warns,
		"logging": p.logs != nil, "keep": logKeep, "now": p.now().Unix()}
}
