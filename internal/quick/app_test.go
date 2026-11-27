package quick

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/wire"
)

// Stand-ins for real services, one per kind of permission.
const (
	svcScreen = "quicktest-screen" // answers whether input is allowed
	svcShell  = "quicktest-shell"
	svcFile   = "quicktest-file"
)

func init() {
	ok := func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return wire.WriteJSON(s, "ok")
	}
	peer.Handle(svcShell, ok)
	peer.Handle(svcFile, ok)
	peer.Handle(svcScreen, func(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return wire.WriteJSON(s, hostd.Allowed(c, perm.Input))
	})
	perm.Register(svcShell, perm.Shell)
	perm.Register(svcFile, perm.File)
	perm.Register(svcScreen, perm.Screen)
}

func noLocals() []netip.Addr { return nil }

// startApp runs a quick-support host on loopback without a rendezvous
// server, on a config directory of its own.
func startApp(t *testing.T, cfg AppConfig) (*App, *hostd.Host) {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	cfg.NoSignal = true
	var host *hostd.Config
	cfg.Host = func(hc *hostd.Config) {
		hc.Listen, hc.Locals, hc.Control = "127.0.0.1:0", noLocals, false
		host = hc
	}
	a := NewApp(cfg)
	a.Start()
	t.Cleanup(a.Stop)
	if st := a.State(); st.Phase != PhaseReady {
		t.Fatalf("app did not start: %+v", st)
	}
	_ = host
	h, err := a.running()
	if err != nil {
		t.Fatal(err)
	}
	return a, h
}

type dialResult struct {
	conn *peer.Conn
	err  error
}

// dialAsync logs in from a fresh client identity in the background.
func dialAsync(t *testing.T, h *hostd.Host, password string) <-chan dialResult {
	t.Helper()
	ch := make(chan dialResult, 1)
	dir := t.TempDir()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := dial.Dial(ctx, h.Addr().String(), dial.Options{Dir: dir, Password: password, Locals: noLocals})
		ch <- dialResult{c, err}
	}()
	return ch
}

func waitState(t *testing.T, a *App, what string, ok func(State) bool) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := a.State()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s; state %+v", what, st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func result(t *testing.T, ch <-chan dialResult) dialResult {
	t.Helper()
	select {
	case r := <-ch:
		if r.conn != nil {
			t.Cleanup(func() { r.conn.Close() })
		}
		return r
	case <-time.After(25 * time.Second):
		t.Fatal("the login neither succeeded nor failed")
	}
	return dialResult{}
}

func open(conn *peer.Conn, service string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := conn.Open(ctx, service, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var v any
	return v, wire.ReadJSON(s, &v)
}

// The right password is not enough: a login waits for the user, has no
// session (and so no way to send input) until allowed, and is listed with
// the client's identity.
func TestApprovalGatesTheSession(t *testing.T) {
	a, h := startApp(t, AppConfig{})
	st := a.State()
	if st.Password == "" || st.Password != mustTemp(h) || !st.Options.Input || !st.Options.Files || st.Options.Advanced || st.AskTimeout != 60 {
		t.Fatalf("initial state: %+v", st)
	}

	login := dialAsync(t, h, h.Password())
	st = waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	r := st.Requests[0]
	if r.N == 0 || len(r.Identity) < 20 || r.From != "127.0.0.1" || r.Auth != "password" || r.Left < 50 || r.Left > 60 {
		t.Fatalf("request: %+v", r)
	}
	// Nothing exists for the client to use while it waits.
	time.Sleep(150 * time.Millisecond)
	if n := len(h.Sessions()); n != 0 || len(a.State().Sessions) != 0 {
		t.Fatalf("%d sessions before approval", n)
	}
	select {
	case res := <-login:
		t.Fatalf("login finished before approval: %+v", res)
	default:
	}

	if err := a.Decide(0, true); err == nil {
		t.Fatal("an answer without a request number was accepted")
	}
	if err := a.Decide(r.N+1, true); err == nil {
		t.Fatal("an answer to an unknown request was accepted")
	}
	if err := a.Decide(r.N, true); err != nil {
		t.Fatal(err)
	}
	res := result(t, login)
	if res.err != nil {
		t.Fatalf("allowed login failed: %v", res.err)
	}
	st = waitState(t, a, "the session", func(s State) bool { return len(s.Sessions) == 1 && len(s.Requests) == 0 })
	if s := st.Sessions[0]; s.Identity != r.Identity || s.Path != "lan" || s.ID == 0 {
		t.Fatalf("session: %+v", s)
	}

	// Disconnect ends it on both sides.
	if err := a.Disconnect(st.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-res.conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client is still connected after Disconnect")
	}
	waitState(t, a, "no sessions", func(s State) bool { return len(s.Sessions) == 0 })
	if err := a.Disconnect(st.Sessions[0].ID); err == nil {
		t.Fatal("disconnecting a session that is gone reported success")
	}
}

func mustTemp(h *hostd.Host) string {
	pw, _ := h.TempPassword()
	return pw
}

func TestDenyAndTimeout(t *testing.T) {
	a, h := startApp(t, AppConfig{AskTimeout: 400 * time.Millisecond})
	login := dialAsync(t, h, h.Password())
	st := waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	if err := a.Decide(st.Requests[0].N, false); err != nil {
		t.Fatal(err)
	}
	if res := result(t, login); res.err == nil {
		t.Fatal("a denied login succeeded")
	}

	// Nobody answers: refused when the time is up.
	login = dialAsync(t, h, h.Password())
	waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	start := time.Now()
	if res := result(t, login); res.err == nil {
		t.Fatal("an unanswered login succeeded")
	} else if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("refusal took %v", d)
	}
	waitState(t, a, "the request to go", func(s State) bool { return len(s.Requests) == 0 })
	if n := len(h.Sessions()); n != 0 {
		t.Fatalf("%d sessions after a denial and a timeout", n)
	}
	// A wrong password never gets as far as asking.
	if res := result(t, dialAsync(t, h, "wrong-password-1")); res.err == nil {
		t.Fatal("wrong password accepted")
	}
	if n := len(a.State().Requests); n != 0 {
		t.Fatalf("a wrong password produced %d requests", n)
	}
}

// The config file cannot weaken a quick-support host: ask stays on, a fixed
// password is ignored, pairing is refused.
func TestConfigFileCannotWeaken(t *testing.T) {
	dir := t.TempDir()
	conf := `{"ask":"off","ask-paired":"off","host-password":"fixed-password-123","feature.file":"off"}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	a, h := startApp(t, AppConfig{Dir: dir})
	if st := a.State(); st.Password == "" || strings.Contains(st.Password, "fixed") {
		t.Fatalf("password shown: %q", st.Password)
	}
	if res := result(t, dialAsync(t, h, "fixed-password-123")); res.err == nil {
		t.Fatal("the config file's fixed password logged in")
	}
	if n := len(a.State().Requests); n != 0 {
		t.Fatal("the fixed password got as far as an approval request")
	}

	login := dialAsync(t, h, h.Password())
	st := waitState(t, a, "a request despite ask=off", func(s State) bool { return len(s.Requests) == 1 })
	a.Decide(st.Requests[0].N, true)
	res := result(t, login)
	if res.err != nil {
		t.Fatal(res.err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hostd.Pair(ctx, res.conn, hostd.PairRequest{Name: "helper"}); err == nil {
		t.Fatal("a quick-support host minted a pairing")
	}
	// A feature the owner switched off stays off although the page's
	// option allows it.
	if _, err := open(res.conn, svcFile); err == nil {
		t.Fatal("file access allowed although feature.file is off in the config file")
	}
}

func TestOptions(t *testing.T) {
	a, h := startApp(t, AppConfig{})
	login := dialAsync(t, h, h.Password())
	st := waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	a.Decide(st.Requests[0].N, true)
	res := result(t, login)
	if res.err != nil {
		t.Fatal(res.err)
	}
	conn := res.conn

	// At start: screen with input, files; no shell.
	if v, err := open(conn, svcScreen); err != nil || v != true {
		t.Fatalf("screen: %v %v", v, err)
	}
	if _, err := open(conn, svcFile); err != nil {
		t.Fatalf("files: %v", err)
	}
	if _, err := open(conn, svcShell); err == nil {
		t.Fatal("shell allowed without the advanced option")
	}
	// Options apply to the session that is already connected.
	for _, o := range []struct {
		name string
		on   bool
	}{{"input", false}, {"files", false}, {"advanced", true}} {
		if err := a.SetOption(o.name, o.on); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := open(conn, svcScreen); err != nil || v != false {
		t.Fatalf("screen with input off: %v %v (the screen must still show, input must be refused)", v, err)
	}
	if _, err := open(conn, svcFile); err == nil {
		t.Fatal("files allowed after being switched off")
	}
	if _, err := open(conn, svcShell); err != nil {
		t.Fatalf("shell with the advanced option: %v", err)
	}
	if err := a.SetOption("screen", false); err == nil {
		t.Fatal("unknown option accepted")
	}
	if st := a.State(); st.Options != (Options{Advanced: true}) {
		t.Fatalf("options: %+v", st.Options)
	}
}

func TestRegenerate(t *testing.T) {
	a, h := startApp(t, AppConfig{AskTimeout: 300 * time.Millisecond})
	old := h.Password()
	if err := a.Regenerate(); err != nil {
		t.Fatal(err)
	}
	if st := a.State(); st.Password == "" || h.Password() == old {
		t.Fatalf("password did not change: %q", st.Password)
	}
	if res := result(t, dialAsync(t, h, old)); res.err == nil {
		t.Fatal("the old password still logs in")
	}
	if n := len(a.State().Requests); n != 0 {
		t.Fatal("the old password got as far as an approval request")
	}
}

// The native dialog is a second place to answer; whichever answers first
// decides, and the dialog is withdrawn when the page answered.
func TestNativePrompt(t *testing.T) {
	var shown, withdrawn atomic.Int32
	answer := make(chan bool, 1)
	a, h := startApp(t, AppConfig{Prompt: func(ctx context.Context, r Request) (bool, bool) {
		shown.Add(1)
		select {
		case v := <-answer:
			return v, true
		case <-ctx.Done():
			withdrawn.Add(1)
			return false, false
		}
	}})

	login := dialAsync(t, h, h.Password())
	waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	answer <- true
	if res := result(t, login); res.err != nil {
		t.Fatalf("login allowed in the dialog failed: %v", res.err)
	}
	waitState(t, a, "the session", func(s State) bool { return len(s.Sessions) == 1 })

	// Answered in the page: the dialog goes away and its answer is void.
	login = dialAsync(t, h, h.Password())
	st := waitState(t, a, "a second request", func(s State) bool { return len(s.Requests) == 1 })
	deadline := time.Now().Add(5 * time.Second)
	for shown.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if err := a.Decide(st.Requests[0].N, false); err != nil {
		t.Fatal(err)
	}
	if res := result(t, login); res.err == nil {
		t.Fatal("login denied in the page succeeded")
	}
	for withdrawn.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if shown.Load() != 2 || withdrawn.Load() != 1 {
		t.Fatalf("dialog shown %d times, withdrawn %d times", shown.Load(), withdrawn.Load())
	}
}

func TestAlreadyRunningAndStop(t *testing.T) {
	dir := t.TempDir()
	a, h := startApp(t, AppConfig{Dir: dir})
	login := dialAsync(t, h, h.Password())
	st := waitState(t, a, "a request", func(s State) bool { return len(s.Requests) == 1 })
	a.Decide(st.Requests[0].N, true)
	res := result(t, login)
	if res.err != nil {
		t.Fatal(res.err)
	}

	// A second host on the same identity is refused, and says why.
	b := NewApp(AppConfig{Dir: dir, NoSignal: true, Host: func(hc *hostd.Config) { hc.Listen, hc.Locals = "127.0.0.1:0", noLocals }})
	b.Start()
	defer b.Stop()
	if st := b.State(); st.Phase != PhaseError || st.ErrorCode != ErrAlreadyRunning || st.Password != "" {
		t.Fatalf("second app: %+v", st)
	}
	if err := b.Regenerate(); err == nil {
		t.Fatal("an app without a host accepted an action")
	}

	// Stop sharing: the helper is cut off, the host is gone, the status
	// file with it.
	a.Stop()
	select {
	case <-a.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	select {
	case <-res.conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client is still connected after Stop")
	}
	if st := a.State(); st.Phase != PhaseStopped || st.Password != "" || len(st.Sessions) != 0 {
		t.Fatalf("state after Stop: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "host.json")); !os.IsNotExist(err) {
		t.Fatalf("status file after Stop: %v", err)
	}
	if err := a.Decide(1, true); err == nil {
		t.Fatal("a stopped app accepted an answer")
	}
	a.Stop() // again: harmless
}

func TestSetupPhase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	t.Setenv("REMOHOP_SIGNAL", "")
	t.Setenv("REMOHOP_SIGNAL_WS", "")
	a := NewApp(AppConfig{Dir: dir, Host: func(hc *hostd.Config) { hc.Listen, hc.Locals = "127.0.0.1:0", noLocals }})
	a.Start()
	defer a.Stop()
	if st := a.State(); st.Phase != PhaseSetup {
		t.Skipf("this build has a default rendezvous server (phase %s)", st.Phase)
	}
	for _, bad := range []string{"", "has spaces", "host:notaport"} {
		if err := a.Configure("custom", bad); err == nil {
			t.Errorf("server address %q accepted", bad)
		}
	}
	// 127.0.0.1:9 answers nothing: the host starts and keeps trying.
	if err := a.Configure("custom", "127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	st := a.State()
	if st.Phase != PhaseConnecting || st.Server != "127.0.0.1:9" || st.Password == "" || st.ID != "" {
		t.Fatalf("after configure: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); !strings.Contains(string(b), "127.0.0.1:9") {
		t.Fatalf("server not stored: %s", b)
	}
	// A server that does not answer (a typo, say) can be changed while the
	// host is still trying: it restarts with the new one.
	if err := a.Configure("custom", "127.0.0.1:8"); err != nil {
		t.Fatalf("changing a server that does not answer: %v", err)
	}
	if st := a.State(); st.Phase != PhaseConnecting || st.Server != "127.0.0.1:8" || st.Password == "" {
		t.Fatalf("after changing the server: %+v", st)
	}
	// So can "no server", which then wins over the address typed before.
	if err := a.Configure("none", ""); err != nil {
		t.Fatal(err)
	}
	if st := a.State(); st.Phase != PhaseReady || st.Server != "" {
		t.Fatalf("after changing to no server: %+v", st)
	}
	// Once running, the choice stays.
	if err := a.Configure("custom", "127.0.0.1:7"); err == nil {
		t.Fatal("the server could be changed from the page once the host was ready")
	}
}

// The server choice of the setup phase: the official server, or none (by
// address), stored in the settings the desktop app shares, so a choice made
// in either is not asked again.
func TestSetupChoices(t *testing.T) {
	newApp := func(dir string) *App {
		a := NewApp(AppConfig{Dir: dir, Host: func(hc *hostd.Config) { hc.Listen, hc.Locals = "127.0.0.1:0", noLocals }})
		a.Start()
		t.Cleanup(a.Stop)
		return a
	}
	t.Setenv("REMOHOP_SIGNAL", "")
	t.Setenv("REMOHOP_SIGNAL_WS", "")

	// None: running at once, reachable by address, and remembered.
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	a := newApp(dir)
	if st := a.State(); st.Phase != PhaseSetup {
		t.Skipf("this build has a default rendezvous server (phase %s)", st.Phase)
	}
	if err := a.Configure("bogus", ""); err == nil {
		t.Fatal("an unknown choice was accepted")
	}
	if err := a.Configure("none", ""); err != nil {
		t.Fatal(err)
	}
	st := a.State()
	// (No addresses here: the test host has no network interfaces to offer.)
	if st.Phase != PhaseReady || st.Server != "" || st.Password == "" {
		t.Fatalf("after choosing no server: %+v", st)
	}
	a.Stop()
	if st := newApp(dir).State(); st.Phase != PhaseReady {
		t.Fatalf("no server was chosen before, yet the next start is in phase %s", st.Phase)
	}

	// Official: the address is stored like `remohop config set signal`.
	dir = t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	a = newApp(dir)
	if err := a.Configure("official", ""); err != nil {
		t.Fatal(err)
	}
	if st := a.State(); st.Server != signal.OfficialAddr {
		t.Fatalf("after choosing the official server: server %q", st.Server)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.json")); !strings.Contains(string(b), signal.OfficialAddr) {
		t.Fatalf("server not stored: %s", b)
	}
}
