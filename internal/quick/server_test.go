package quick

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a Controller that records what the page asked for.
type fake struct {
	mu    sync.Mutex
	st    State
	calls []string
	fail  error
	subs  map[chan struct{}]struct{}
}

func newFake() *fake {
	return &fake{subs: map[chan struct{}]struct{}{}, st: State{Phase: PhaseReady, ID: "123 456 789", Password: "abcd-efgh",
		Online: true, Server: "rendezvous.example.net", Transport: "udp", Relay: true, Options: DefaultOptions(),
		AskTimeout: 60, Version: "dev", Requests: []Request{}, Sessions: []SessionInfo{}}}
}

func (f *fake) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.st
	st.Requests = append([]Request{}, f.st.Requests...)
	st.Sessions = append([]SessionInfo{}, f.st.Sessions...)
	return st
}

func (f *fake) set(change func(*State)) {
	f.mu.Lock()
	change(&f.st)
	for ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	f.mu.Unlock()
}

func (f *fake) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}
}

func (f *fake) call(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
	return f.fail
}

func (f *fake) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "; ")
}

func (f *fake) Regenerate() error {
	err := f.call("regenerate")
	f.set(func(s *State) { s.Password = "wxyz-2345" })
	return err
}
func (f *fake) Decide(n int, allow bool) error {
	err := f.call("decide %d %v", n, allow)
	f.set(func(s *State) {
		for i, r := range s.Requests {
			if r.N == n {
				s.Requests = append(s.Requests[:i:i], s.Requests[i+1:]...)
				if allow {
					s.Sessions = append(s.Sessions, SessionInfo{ID: n, Name: r.Name, Device: r.Device, Identity: r.Identity, From: r.From, Path: "punched"})
				}
				break
			}
		}
	})
	return err
}
func (f *fake) Disconnect(id int) error {
	err := f.call("disconnect %d", id)
	f.set(func(s *State) {
		for i, x := range s.Sessions {
			if x.ID == id {
				s.Sessions = append(s.Sessions[:i:i], s.Sessions[i+1:]...)
				break
			}
		}
	})
	return err
}
func (f *fake) SetOption(name string, on bool) error {
	err := f.call("option %s %v", name, on)
	f.set(func(s *State) {
		switch name {
		case "input":
			s.Options.Input = on
		case "files":
			s.Options.Files = on
		case "advanced":
			s.Options.Advanced = on
		}
	})
	return err
}
func (f *fake) Configure(mode, server string) error {
	call := "configure " + server
	if mode != "" && mode != "custom" {
		call = "configure " + mode
	}
	if err := f.call("%s", call); err != nil {
		return err
	}
	f.set(func(s *State) { s.Phase, s.Server = PhaseConnecting, server })
	return nil
}
func (f *fake) Stop() {
	f.call("stop")
	f.set(func(s *State) { s.Phase = PhaseStopped })
}

func startServer(t *testing.T, ctl Controller) *Server {
	t.Helper()
	s, err := NewServer(ctl, "en", nil)
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(s.Close)
	return s
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt  { return func(r *http.Request) { r.Header.Set(k, v) } }
func hostHeader(h string) reqOpt { return func(r *http.Request) { r.Host = h } }

// do sends a request to the server's real address. path is relative to the
// token prefix unless it starts with "/".
func do(t *testing.T, s *Server, method, path, body string, opts ...reqOpt) (int, string, http.Header) {
	t.Helper()
	u := s.URL() + path
	if strings.HasPrefix(path, "/") {
		u = "http://127.0.0.1:" + s.port + path
	}
	req, err := http.NewRequest(method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	c := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func hello(t *testing.T, s *Server) string {
	t.Helper()
	code, body, _ := do(t, s, "POST", "api/hello", "{}")
	if code != 200 {
		t.Fatalf("hello: %d %s", code, body)
	}
	var h struct {
		CSRF  string `json:"csrf"`
		Lang  string `json:"lang"`
		State State  `json:"state"`
	}
	if err := json.Unmarshal([]byte(body), &h); err != nil || h.CSRF == "" || h.Lang != "en" || h.State.ID != "123 456 789" {
		t.Fatalf("hello answered %s (%v)", body, err)
	}
	return h.CSRF
}

func TestTokenIsTheLogin(t *testing.T) {
	s := startServer(t, newFake())
	if len(s.token) < 22 || s.token == s.csrf {
		t.Fatalf("token %q, csrf %q", s.token, s.csrf)
	}
	for _, path := range []string{"/", "/api/state", "/wrong-token/", "/wrong-token/api/state", "/" + s.token[:len(s.token)-1] + "/",
		"/assets/app.js", "//" + s.token + "/"} {
		if code, body, _ := do(t, s, "GET", path, ""); code != http.StatusNotFound {
			t.Errorf("GET %s without the token: %d %s", path, code, body)
		}
	}
	if code, body, _ := do(t, s, "POST", "/api/stop", "{}"); code != http.StatusNotFound {
		t.Errorf("POST without the token: %d %s", code, body)
	}
	if code, _, h := do(t, s, "GET", "/"+s.token, ""); code != http.StatusTemporaryRedirect || h.Get("Location") != "/"+s.token+"/" {
		t.Errorf("token without slash: %d, Location %q", code, h.Get("Location"))
	}
	code, body, h := do(t, s, "GET", "", "")
	if code != 200 || !strings.Contains(body, "assets/app.js") {
		t.Fatalf("index: %d", code)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers: %v", h)
	}
	for _, name := range []string{"app.js", "style.css", "icon.svg"} {
		if code, _, _ := do(t, s, "GET", "assets/"+name, ""); code != 200 {
			t.Errorf("asset %s: %d", name, code)
		}
	}
}

// The listener is on 127.0.0.1 and nowhere else: the UI cannot be reached
// from another machine.
func TestLoopbackOnly(t *testing.T) {
	s := startServer(t, newFake())
	tcp := s.ln.Addr().(*net.TCPAddr)
	if !tcp.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("listening on %v", tcp)
	}
	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL %s", s.URL())
	}
	addrs, _ := net.InterfaceAddrs()
	tried := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		tried++
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), s.port), 500*time.Millisecond)
		if err == nil {
			c.Close()
			t.Errorf("the UI port answers on %s", ipn.IP)
		}
	}
	t.Logf("checked %d non-loopback addresses", tried)
}

// A hostile name that resolves to 127.0.0.1 (DNS rebinding) arrives with
// that name in the Host header.
func TestHostHeader(t *testing.T) {
	s := startServer(t, newFake())
	for _, h := range []string{"evil.example:" + s.port, "evil.example", "127.0.0.1", "127.0.0.1:1", "127.0.0.1.evil.example:" + s.port,
		"192.168.1.10:" + s.port, "[::ffff:192.168.1.10]:" + s.port} {
		if code, _, _ := do(t, s, "GET", "", "", hostHeader(h)); code != http.StatusForbidden && code != http.StatusBadRequest {
			t.Errorf("Host %q: %d", h, code)
		}
		if code, _, _ := do(t, s, "POST", "api/hello", "{}", hostHeader(h)); code != http.StatusForbidden && code != http.StatusBadRequest {
			t.Errorf("POST with Host %q: %d", h, code)
		}
	}
	for _, h := range []string{"127.0.0.1:" + s.port, "localhost:" + s.port, "LOCALHOST:" + s.port, "[::1]:" + s.port} {
		if code, _, _ := do(t, s, "GET", "", "", hostHeader(h)); code != 200 {
			t.Errorf("Host %q refused: %d", h, code)
		}
	}
}

func TestCrossSiteRequestsRefused(t *testing.T) {
	f := newFake()
	s := startServer(t, f)
	csrf := hello(t, s)
	own := "http://127.0.0.1:" + s.port
	cross := [][]reqOpt{
		{header("Origin", "https://evil.example")},
		{header("Origin", "http://localhost:"+s.port)}, // another origin, even though it is this machine
		{header("Origin", "https://127.0.0.1:"+s.port)},
		{header("Origin", "null")},
		{header("Origin", own+"/x")},
		{header("Sec-Fetch-Site", "cross-site")},
		{header("Sec-Fetch-Site", "same-site")},
		{header("Sec-Fetch-Site", "none")},
		{header("Origin", own), header("Sec-Fetch-Site", "cross-site")},
	}
	for i, opts := range cross {
		for _, path := range []string{"api/hello", "api/stop", "api/decide", "api/regenerate", "api/bye"} {
			o := append([]reqOpt{header(hdrCSRF, csrf)}, opts...)
			if code, body, _ := do(t, s, "POST", path, `{"n":1,"allow":true}`, o...); code != http.StatusForbidden || !strings.Contains(body, "bad_origin") {
				t.Errorf("case %d POST %s: %d %s", i, path, code, body)
			}
		}
		for _, path := range []string{"api/state", "api/events"} {
			if code, body, _ := do(t, s, "GET", path, "", opts...); code != http.StatusForbidden {
				t.Errorf("case %d GET %s: %d %s", i, path, code, body)
			}
		}
	}
	if got := f.log(); got != "" {
		t.Fatalf("cross-site requests reached the controller: %s", got)
	}
	// The page itself.
	if code, body, _ := do(t, s, "POST", "api/regenerate", "{}", header(hdrCSRF, csrf), header("Origin", own), header("Sec-Fetch-Site", "same-origin")); code != 200 {
		t.Fatalf("same-origin POST: %d %s", code, body)
	}
}

func TestCSRFAndContentType(t *testing.T) {
	f := newFake()
	s := startServer(t, f)
	csrf := hello(t, s)
	for _, path := range []string{"api/regenerate", "api/decide", "api/disconnect", "api/option", "api/configure", "api/stop", "api/bye"} {
		if code, body, _ := do(t, s, "POST", path, `{"n":1,"allow":true}`); code != http.StatusForbidden || !strings.Contains(body, "bad_csrf") {
			t.Errorf("%s without CSRF token: %d %s", path, code, body)
		}
		if code, _, _ := do(t, s, "POST", path, `{}`, header(hdrCSRF, csrf+"x")); code != http.StatusForbidden {
			t.Errorf("%s with a wrong CSRF token: %d", path, code)
		}
		// What a cross-site HTML form can send.
		for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
			if code, _, _ := do(t, s, "POST", path, `{}`, header(hdrCSRF, csrf), header("Content-Type", ct)); code != http.StatusUnsupportedMediaType {
				t.Errorf("%s as %s: %d", path, ct, code)
			}
		}
		// State never changes on GET.
		if code, _, _ := do(t, s, "GET", path, "", header(hdrCSRF, csrf)); code == 200 {
			t.Errorf("GET %s succeeded", path)
		}
	}
	if code, _, _ := do(t, s, "POST", "api/hello", "{}", header("Content-Type", "text/plain")); code != http.StatusUnsupportedMediaType {
		t.Errorf("hello as text/plain: %d", code)
	}
	if got := f.log(); got != "" {
		t.Fatalf("unauthorised requests reached the controller: %s", got)
	}
}

func TestActions(t *testing.T) {
	f := newFake()
	f.set(func(s *State) {
		s.Requests = []Request{{N: 7, Device: "helper-laptop", Identity: "aaaa:bbbb:cccc:dddd:eeee", From: "203.0.113.5", Auth: "password", Left: 55}}
	})
	s := startServer(t, f)
	csrf := hello(t, s)
	post := func(path, body string) (int, string) {
		code, out, _ := do(t, s, "POST", path, body, header(hdrCSRF, csrf))
		return code, out
	}
	for _, c := range []struct{ path, body string }{
		{"api/decide", `{"n":7,"allow":true}`},
		{"api/option", `{"name":"input","on":false}`},
		{"api/option", `{"name":"advanced","on":true}`},
		{"api/regenerate", ``},
		{"api/disconnect", `{"id":7}`},
		{"api/configure", `{"server":"  support.example.net  "}`},
	} {
		if code, out := post(c.path, c.body); code != 200 {
			t.Fatalf("%s: %d %s", c.path, code, out)
		}
	}
	want := "decide 7 true; option input false; option advanced true; regenerate; disconnect 7; configure support.example.net"
	if got := f.log(); got != want {
		t.Fatalf("controller saw:\n %s\nwant:\n %s", got, want)
	}
	code, body, _ := do(t, s, "GET", "api/state", "")
	var st State
	if err := json.Unmarshal([]byte(body), &st); code != 200 || err != nil || st.Password != "wxyz-2345" || st.Options.Input || !st.Options.Advanced {
		t.Fatalf("state: %d %s", code, body)
	}

	// A refusal by the controller reaches the page as text.
	f.mu.Lock()
	f.fail = errors.New("no pending connection 9 (it may have timed out)")
	f.mu.Unlock()
	if code, out := post("api/decide", `{"n":9,"allow":true}`); code != http.StatusConflict || !strings.Contains(out, "may have timed out") {
		t.Fatalf("failed decide: %d %s", code, out)
	}
	if code, out := post("api/decide", `{"n":`); code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d %s", code, out)
	}
	if code, out := post("api/decide", `{"n":1,"pad":"`+strings.Repeat("x", 8000)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d %s", code, out)
	}
	if code, _ := post("api/nothing", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown call: %d", code)
	}

	f.mu.Lock()
	f.fail = nil
	f.mu.Unlock()
	if code, out := post("api/stop", `{}`); code != 200 {
		t.Fatalf("stop: %d %s", code, out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for f.State().Phase != PhaseStopped {
		if time.Now().After(deadline) {
			t.Fatal("stop did not reach the controller")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// events reads server-sent events into a channel of states.
func events(t *testing.T, s *Server) (<-chan State, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL()+"api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := make(chan State, 16)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var st State
				if json.Unmarshal([]byte(data), &st) == nil {
					ch <- st
				}
			}
		}
	}()
	return ch, cancel
}

func next(t *testing.T, ch <-chan State) State {
	t.Helper()
	select {
	case st, ok := <-ch:
		if !ok {
			t.Fatal("event stream ended")
		}
		return st
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return State{}
}

func waitPresence(t *testing.T, s *Server, ok func(Presence) bool) Presence {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p := s.Presence()
		if ok(p) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("presence stuck at %+v", p)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEventsAndPresence(t *testing.T) {
	f := newFake()
	s := startServer(t, f)
	csrf := hello(t, s)
	if p := s.Presence(); p.Viewers != 0 || p.Seen {
		t.Fatalf("presence before any page: %+v", p)
	}
	ch, closeStream := events(t, s)
	if st := next(t, ch); st.Phase != PhaseReady || st.Password != "abcd-efgh" {
		t.Fatalf("first event: %+v", st)
	}
	waitPresence(t, s, func(p Presence) bool { return p.Viewers == 1 && p.Seen && !p.Bye })

	// A change is pushed at once, not on the next poll.
	start := time.Now()
	f.set(func(st *State) { st.Requests = []Request{{N: 3, Device: "x", Identity: "id", Left: 60}} })
	if st := next(t, ch); len(st.Requests) != 1 || st.Requests[0].N != 3 {
		t.Fatalf("second event: %+v", st)
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("change took %v to arrive", d)
	}

	// A second page, then both go; the first says goodbye.
	_, closeSecond := events(t, s)
	waitPresence(t, s, func(p Presence) bool { return p.Viewers == 2 })
	if code, _, _ := do(t, s, "POST", "api/bye", "{}", header(hdrCSRF, csrf)); code != 200 {
		t.Fatal("bye refused")
	}
	closeStream()
	waitPresence(t, s, func(p Presence) bool { return p.Viewers == 1 && p.Bye })
	closeSecond()
	p := waitPresence(t, s, func(p Presence) bool { return p.Viewers == 0 })
	if !p.Seen || !p.Bye || time.Since(p.Gone) > 5*time.Second {
		t.Fatalf("presence after the pages left: %+v", p)
	}
	// A page that comes back (reload) cancels the goodbye.
	_, closeThird := events(t, s)
	defer closeThird()
	waitPresence(t, s, func(p Presence) bool { return p.Viewers == 1 && !p.Bye })
}

func TestLifecycle(t *testing.T) {
	l := life{First: 80 * time.Millisecond, Bye: 60 * time.Millisecond, Lost: 150 * time.Millisecond}
	var mu sync.Mutex
	var p Presence
	busy := false
	presence := func() Presence { mu.Lock(); defer mu.Unlock(); return p }
	isBusy := func() bool { mu.Lock(); defer mu.Unlock(); return busy }
	set := func(np Presence, b bool) { mu.Lock(); p, busy = np, b; mu.Unlock() }
	run := func() (string, time.Duration) {
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		defer cancel()
		start := time.Now()
		return l.watch(ctx, presence, make(chan struct{}), isBusy), time.Since(start)
	}

	set(Presence{}, false)
	if r, _ := run(); r != endNoWindow {
		t.Errorf("no page ever: %q", r)
	}
	set(Presence{Viewers: 1, Seen: true}, false)
	if r, _ := run(); r != "" {
		t.Errorf("a page is showing: %q", r)
	}
	set(Presence{Seen: true, Bye: true, Gone: time.Now()}, true)
	if r, d := run(); r != endClosed || d > 600*time.Millisecond {
		t.Errorf("window closed with a session open: %q after %v", r, d)
	}
	set(Presence{Seen: true, Gone: time.Now()}, false)
	if r, _ := run(); r != endLost {
		t.Errorf("page vanished, nobody connected: %q", r)
	}
	// A vanished page does not end a session; the host stops when the
	// session does.
	set(Presence{Seen: true, Gone: time.Now()}, true)
	go func() {
		time.Sleep(350 * time.Millisecond)
		set(Presence{Seen: true, Gone: time.Now().Add(-time.Second)}, false)
	}()
	if r, d := run(); r != endLost || d < 300*time.Millisecond {
		t.Errorf("page vanished during a session: %q after %v", r, d)
	}
}

// TestLanguageChoice: the page's language switch reaches the process (which
// saves it and uses it for the native dialogs) and the next page starts in
// it; it needs the CSRF token like every other change.
func TestLanguageChoice(t *testing.T) {
	s := startServer(t, newFake())
	var got []string
	s.OnLang(func(l string) { got = append(got, l) })
	csrf := hello(t, s)
	if code, _, _ := do(t, s, "POST", "api/lang", `{"lang":"zh"}`); code != http.StatusForbidden {
		t.Fatalf("api/lang without the CSRF token: %d", code)
	}
	if code, body, _ := do(t, s, "POST", "api/lang", `{"lang":"de"}`, header(hdrCSRF, csrf)); code != http.StatusConflict || !strings.Contains(body, "bad_request") {
		t.Fatalf("api/lang de: %d %s", code, body)
	}
	if code, body, _ := do(t, s, "POST", "api/lang", `{"lang":"zh-CN"}`, header(hdrCSRF, csrf)); code != 200 {
		t.Fatalf("api/lang zh-CN: %d %s", code, body)
	}
	if len(got) != 1 || got[0] != "zh" {
		t.Fatalf("OnLang saw %q", got)
	}
	_, body, _ := do(t, s, "POST", "api/hello", "{}")
	if !strings.Contains(body, `"lang":"zh"`) {
		t.Fatalf("hello after the switch: %s", body)
	}
}

func TestSavedLang(t *testing.T) {
	dir := t.TempDir()
	if l := savedLang(dir); l != "" {
		t.Fatalf("nothing saved: %q", l)
	}
	for in, want := range map[string]string{"zh\n": "zh", "en": "en", "fr\n": "", "zh-Hans": "zh"} {
		if err := os.WriteFile(filepath.Join(dir, langFile), []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		if l := savedLang(dir); l != want {
			t.Errorf("saved %q: got %q, want %q", in, l, want)
		}
	}
}
