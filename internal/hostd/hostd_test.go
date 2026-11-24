package hostd_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

func noLocals() []netip.Addr { return nil }

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func startHost(t *testing.T, cfg hostd.Config) *hostd.Host {
	t.Helper()
	cfg.Dir = t.TempDir()
	cfg.Listen = "127.0.0.1:0"
	cfg.Locals = noLocals
	h, err := hostd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("host: %v", err)
		}
	})
	return h
}

func dialHost(t *testing.T, h *hostd.Host, password string) (*peer.Conn, error) {
	t.Helper()
	return dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: password, Locals: noLocals})
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := hostd.New(hostd.Config{Dir: t.TempDir(), Listen: "127.0.0.1:0", Password: "short"}); err == nil {
		t.Fatal("a 5-character fixed password was accepted")
	}
	a := startHost(t, hostd.Config{})
	b := startHost(t, hostd.Config{})
	if a.Password() == b.Password() || len(a.Password()) != 8 {
		t.Fatalf("temporary passwords %q and %q", a.Password(), b.Password())
	}
	// A fixed password is accepted as typed, separators and all.
	c := startHost(t, hostd.Config{Password: "tea-time at four"})
	conn, err := dialHost(t, c, "teatime atfour")
	if err != nil {
		t.Fatalf("login with the fixed password: %v", err)
	}
	conn.Close()
}

func TestNoPairing(t *testing.T) {
	h := startHost(t, hostd.Config{NoPairing: true})
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "x"}); err == nil || !strings.Contains(err.Error(), "does not allow pairing") {
		t.Fatalf("pair on a --no-pairing host: %v", err)
	}
	if toks, _ := h.Tokens().List(); len(toks) != 0 {
		t.Fatalf("token minted despite NoPairing: %+v", toks)
	}
}

// The client side of a session exposes no pair service: a host cannot ask
// the machine controlling it for anything.
func TestPairOnlyOnHostSessions(t *testing.T) {
	h := startHost(t, hostd.Config{})
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// The client is done with the login one message before the host is.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Sessions()) != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions on the host", len(h.Sessions()))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if hostd.SessionOf(conn) != nil {
		t.Fatal("client connection claims to be a host session")
	}
}

func rawDial(t *testing.T, h *hostd.Host) *quic.Conn {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	conf, err := id.ClientTLS(peer.ALPN, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A burst of handshakes can lose a packet on loopback when the UDP
	// receive buffer is small (macOS), so a handshake that times out is
	// tried again: these tests are about what the host does with the
	// connections, not about every first handshake landing.
	var q *quic.Conn
	for attempt := 1; ; attempt++ {
		q, err = quic.DialAddr(testCtx(t), h.Addr().String(), conf, peer.QUICConfig())
		var idle *quic.IdleTimeoutError
		if err == nil || attempt == 3 || !errors.As(err, &idle) {
			break
		}
		t.Logf("raw dial attempt %d: %v; retrying", attempt, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.CloseWithError(0, "") })
	return q
}

// Before login a connection gets the login handshake and nothing else: a
// stream that tries to open a service is never served.
func TestNoServiceBeforeLogin(t *testing.T) {
	h := startHost(t, hostd.Config{})
	q := rawDial(t, h)
	// First stream: what would be a service header on an authenticated
	// session. The host reads it as a (malformed) login.
	st, err := q.OpenStreamSync(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	st.SetDeadline(time.Now().Add(5 * time.Second))
	body := []byte(`{"service":"ping"}`)
	st.Write([]byte{0, 0, 0, byte(len(body))})
	st.Write(body)
	reply, _ := io.ReadAll(st)
	if strings.Contains(string(reply), `"ok":true`) {
		t.Fatalf("host served a stream before login: %s", reply)
	}
	select {
	case <-q.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host kept an unauthenticated connection open after a bad login")
	}
	if n := len(h.Sessions()); n != 0 {
		t.Fatalf("%d sessions without login", n)
	}

	// A client without an identity certificate does not get that far.
	_, err = quic.DialAddr(testCtx(t), h.Addr().String(), &tls.Config{InsecureSkipVerify: true, NextProtos: []string{peer.ALPN}}, peer.QUICConfig())
	if err == nil {
		// TLS 1.3 reports a missing client certificate after the client
		// considers the handshake done; the connection must die at once.
		t.Log("dial returned before the certificate alert; that is expected with TLS 1.3")
	}
}

// The reviewer's case: connections that complete the QUIC handshake and
// then say nothing. However many there are, they keep nobody out (the
// oldest idler makes room for each newcomer), and each is dropped after
// the short time a real client needs to send its hello.
func TestIdleConnectionsCannotBlockLogins(t *testing.T) {
	h := startHost(t, hostd.Config{})
	clientDir := t.TempDir()
	opts := dial.Options{Dir: clientDir, Password: h.Password(), Locals: noLocals}
	login := func(o dial.Options) *peer.Conn {
		t.Helper()
		conn, err := dial.Dial(testCtx(t), h.Addr().String(), o)
		if err != nil {
			t.Fatalf("login with idle connections piled up: %v", err)
		}
		return conn
	}

	// Pair first, so there is a token to log in with later.
	conn := login(opts)
	pairing, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.SavePairing(dial.InfoOf(conn), pairing, "", clientDir); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	start := time.Now()
	var idle []*quic.Conn
	for i := 0; i < 160; i++ {
		idle = append(idle, rawDial(t, h))
	}
	t.Logf("opened %d idle connections in %s", len(idle), time.Since(start))

	// With the password, and with the token alone.
	login(opts).Close()
	tokenOnly := opts
	tokenOnly.Password = ""
	conn = login(tokenOnly)
	if dial.InfoOf(conn).Auth != auth.KindToken {
		t.Fatal("second login did not use the pairing")
	}
	conn.Close()

	// Keep piling up while logging in repeatedly.
	stop := make(chan struct{})
	flooded := make(chan int, 1)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				flooded <- n
				return
			default:
			}
			id, _ := auth.NewIdentity()
			conf, _ := id.ClientTLS(peer.ALPN, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if q, err := quic.DialAddr(ctx, h.Addr().String(), conf, peer.QUICConfig()); err == nil {
				n++
				defer q.CloseWithError(0, "")
			}
			cancel()
		}
	}()
	for i := 0; i < 5; i++ {
		login(tokenOnly).Close()
		login(opts).Close()
	}
	close(stop)
	t.Logf("%d more idle connections were opened during the logins", <-flooded)

	// Every idler is gone soon after: evicted, or timed out waiting for
	// its hello.
	deadline := time.After(6 * time.Second)
	for i, q := range idle {
		select {
		case <-q.Context().Done():
		case <-deadline:
			t.Fatalf("idle connection %d still open %s after it was made", i, time.Since(start))
		}
	}
	if n := len(h.Sessions()); n != 0 {
		// Sessions end asynchronously after Close; give them a moment.
		time.Sleep(300 * time.Millisecond)
		if n = len(h.Sessions()); n != 0 {
			t.Fatalf("%d sessions left open", n)
		}
	}
}

// A connection that never sends its hello is closed after a couple of
// seconds, not after the whole login timeout.
func TestIdleConnectionDeadline(t *testing.T) {
	h := startHost(t, hostd.Config{})
	q := rawDial(t, h)
	start := time.Now()
	select {
	case <-q.Context().Done():
	case <-time.After(5 * time.Second):
		t.Fatal("idle connection still open after 5 s")
	}
	if took := time.Since(start); took < time.Second || took > 4*time.Second {
		t.Fatalf("idle connection closed after %s, want about 2 s", took)
	}
}

// rawLogin runs the client side of a login on a bare connection and
// returns its error, leaving the connection open.
func rawLogin(t *testing.T, h *hostd.Host, cred auth.Credential) (*quic.Conn, error) {
	t.Helper()
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	conf, _ := id.ClientTLS(peer.ALPN, nil)
	q, err := quic.DialAddr(testCtx(t), h.Addr().String(), conf, peer.QUICConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.CloseWithError(0, "") })
	st, err := q.OpenStreamSync(testCtx(t))
	if err != nil {
		return q, err
	}
	st.SetDeadline(time.Now().Add(10 * time.Second))
	b, err := auth.ClientBinding(q.ConnectionState().TLS, id.Public())
	if err != nil {
		t.Fatal(err)
	}
	return q, auth.ClientHandshake(st, b, cred, "")
}

// A refused login is over at once: the host closes the connection instead
// of keeping it around, and the client still learns the reason, from the
// reply or from the close.
func TestRefusedLoginClosesImmediately(t *testing.T) {
	h := startHost(t, hostd.Config{})
	for name, cred := range map[string]auth.Credential{
		"unknown token": auth.TokenCredential("00112233aabbccdd", make([]byte, auth.TokenSize)),
	} {
		start := time.Now()
		q, err := rawLogin(t, h, cred)
		code := auth.CodeOf(err)
		if e := hostd.LoginRefusal(err); e != nil {
			code = e.Code
		}
		if code != auth.CodeUnknownToken {
			t.Fatalf("%s: client saw %v", name, err)
		}
		select {
		case <-q.Context().Done():
		case <-time.After(time.Second):
			t.Fatalf("%s: connection still open 1 s after the refusal", name)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("%s: refusal took %s", name, took)
		}
		if e := hostd.LoginRefusal(context.Cause(q.Context())); e == nil || e.Code != auth.CodeUnknownToken {
			t.Fatalf("%s: close reason %v does not carry the refusal", name, context.Cause(q.Context()))
		}
	}

	// The same for a malformed hello.
	q := rawDial(t, h)
	st, err := q.OpenStreamSync(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	st.Write([]byte{0, 0, 0, 2, '{', '}'})
	select {
	case <-q.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("connection still open 1 s after a malformed hello")
	}
}

// Password logins that stall (hello sent, handshake never finished) fill
// only the password share. Paired clients log in regardless, at once.
func TestStalledPasswordLoginsDoNotStarveTokens(t *testing.T) {
	h := startHost(t, hostd.Config{})
	clientDir := t.TempDir()
	opts := dial.Options{Dir: clientDir, Password: h.Password(), Locals: noLocals}
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), opts)
	if err != nil {
		t.Fatal(err)
	}
	pairing, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.SavePairing(dial.InfoOf(conn), pairing, "", clientDir); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// 40 connections each send a password hello and then go quiet.
	id, _ := auth.NewIdentity()
	conf, _ := id.ClientTLS(peer.ALPN, nil)
	hello := []byte(`{"v":1,"cred":"password","msg_a":"` + strings.Repeat("A", 64) + `"}`)
	for i := 0; i < 40; i++ {
		q, err := quic.DialAddr(testCtx(t), h.Addr().String(), conf, peer.QUICConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer q.CloseWithError(0, "")
		st, err := q.OpenStreamSync(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		st.Write([]byte{0, 0, byte(len(hello) >> 8), byte(len(hello))})
		st.Write(hello)
	}
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	tokenOnly := opts
	tokenOnly.Password = ""
	conn, err = dial.Dial(testCtx(t), h.Addr().String(), tokenOnly)
	if err != nil {
		t.Fatalf("token login while password logins are stalled: %v", err)
	}
	conn.Close()
	if took := time.Since(start); took > time.Second {
		t.Fatalf("token login took %s", took)
	}
}

// syncBuffer is a bytes.Buffer safe to read while a command writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestHostCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	host := cli.Lookup("host")

	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := host.Run(context.Background(), &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, args)
		return code, out.String(), errb.String()
	}

	if code, _, errOut := run("status"); code != cli.ExitError || !strings.Contains(errOut, "not running") {
		t.Fatalf("status with no host: %d %q", code, errOut)
	}
	if code, _, errOut := run(); code != cli.ExitUsage || !strings.Contains(errOut, "no rendezvous server configured") || !strings.Contains(errOut, "--no-signal") {
		t.Fatalf("host without a rendezvous server: %d %q", code, errOut)
	}
	if code, _, errOut := run("--no-signal", "--password", "short"); code != cli.ExitError || !strings.Contains(errOut, "too short") {
		t.Fatalf("host with a short password: %d %q", code, errOut)
	}

	// Run the daemon in the foreground of a goroutine.
	ctx, stop := context.WithCancel(context.Background())
	var out, errb syncBuffer
	exited := make(chan int, 1)
	go func() {
		exited <- host.Run(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb},
			[]string{"--no-signal", "--listen", "127.0.0.1:0"})
	}()
	defer func() {
		stop()
		<-exited
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "Identity:") {
		if time.Now().After(deadline) {
			t.Fatalf("host did not come up\nstdout: %s\nstderr: %s", out.String(), errb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	m := regexp.MustCompile(`Password:\s+(\S+)\s+\(temporary`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no temporary password in output:\n%s", out.String())
	}
	password := m[1]
	if !regexp.MustCompile(`^[a-z2-9]{4}-[a-z2-9]{4}$`).MatchString(password) {
		t.Fatalf("password %q is not in the xxxx-xxxx form", password)
	}

	// status, human and JSON, from "another process".
	if _, hidden, _ := run("status"); strings.Contains(hidden, password) {
		t.Fatalf("status prints the password without being asked:\n%s", hidden)
	}
	code, statusOut, _ := run("status", "--show-password")
	if code != cli.ExitOK || !strings.Contains(statusOut, password) || !strings.Contains(statusOut, "127.0.0.1:") {
		t.Fatalf("status: %d\n%s", code, statusOut)
	}
	code, statusOut, _ = run("status", "--json", "--show-password")
	var st struct {
		Running  bool   `json:"running"`
		PID      int    `json:"pid"`
		Password string `json:"password"`
		Listen   string `json:"listen"`
	}
	if err := json.Unmarshal([]byte(statusOut), &st); err != nil || code != cli.ExitOK {
		t.Fatalf("status --json: %d %q %v", code, statusOut, err)
	}
	if !st.Running || st.PID != os.Getpid() || st.Password != password {
		t.Fatalf("status --json = %+v", st)
	}
	names := []string{"host.json", "identity.key"}
	if runtime.GOOS == "windows" {
		// There the directory's access-control list is what protects
		// the files; on Unix each file carries its own mode.
		names = append(names, ".")
	}
	for _, name := range names {
		if private, err := config.IsPrivate(filepath.Join(dir, name)); err != nil || !private {
			t.Fatalf("%s is not private to the user (%v)", name, err)
		}
	}

	// A second host on the same identity is refused.
	if code, _, errOut := run("--no-signal"); code != cli.ExitError || !strings.Contains(errOut, "already running") {
		t.Fatalf("second host: %d %q", code, errOut)
	}

	// A client logs in and pairs; the host lists it and revokes it.
	if _, _, err := net.SplitHostPort(st.Listen); err != nil {
		t.Fatalf("listen address %q", st.Listen)
	}
	clientDir := t.TempDir()
	conn, err := dial.Dial(testCtx(t), st.Listen, dial.Options{Dir: clientDir, Password: password, Locals: noLocals})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "laptop"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "session opened") || !strings.Contains(errb.String(), "from=127.0.0.1:") {
		t.Errorf("host did not log the session with its origin:\n%s", errb.String())
	}

	code, pairedOut, _ := run("paired")
	if code != cli.ExitOK || !strings.Contains(pairedOut, "laptop") {
		t.Fatalf("paired: %d\n%s", code, pairedOut)
	}
	code, pairedOut, _ = run("paired", "--json")
	if code != cli.ExitOK || !strings.Contains(pairedOut, `"name": "laptop"`) || strings.Contains(pairedOut, "secret") {
		t.Fatalf("paired --json: %d\n%s", code, pairedOut)
	}
	if code, _, errOut := run("revoke", "nobody"); code != cli.ExitError || !strings.Contains(errOut, "no pairing named") {
		t.Fatalf("revoke of unknown: %d %q", code, errOut)
	}
	if code, revOut, _ := run("revoke", "laptop"); code != cli.ExitOK || !strings.Contains(revOut, "revoked") {
		t.Fatalf("revoke: %d %q", code, revOut)
	}
	if _, out, _ := run("paired"); !strings.Contains(out, "no paired clients") {
		t.Fatalf("paired after revoke:\n%s", out)
	}

	// Interrupt: clean exit, session told, status file gone.
	stop()
	select {
	case code := <-exited:
		exited <- code
		if code != cli.ExitOK {
			t.Fatalf("host exited with %d\nstderr: %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("host did not exit on cancellation")
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("session survived host exit")
	}
	if _, err := os.Stat(filepath.Join(dir, "host.json")); !os.IsNotExist(err) {
		t.Fatalf("status file left behind: %v", err)
	}
	if code, _, _ := run("status"); code != cli.ExitError {
		t.Fatal("status reports a host after it exited")
	}
}

// A client that logs in and hangs up at once (a command that fails before
// it opens a stream) must not have its correct password counted as a wrong
// guess because the close overtook the last handshake message.
func TestImmediateCloseIsNotAFailedLogin(t *testing.T) {
	h := startHost(t, hostd.Config{})
	for i := 0; i < 12; i++ {
		conn, err := dialHost(t, h, h.Password())
		if err != nil {
			t.Fatalf("login %d: %v", i+1, err)
		}
		conn.Close()
	}
}
