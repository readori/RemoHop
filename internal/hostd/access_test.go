package hostd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

const (
	svcShellish = "hostdtest-shell"
	svcScreen   = "hostdtest-screen" // reports whether input is allowed
	svcUnmapped = "hostdtest-unmapped"
)

func init() {
	ok := func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return wire.WriteJSON(s, "ok")
	}
	peer.Handle(svcShellish, ok)
	peer.Handle(svcUnmapped, ok)
	peer.Handle(svcScreen, func(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return wire.WriteJSON(s, hostd.Allowed(c, perm.Input))
	})
	perm.Register(svcShellish, perm.Shell)
	perm.Register(svcScreen, perm.Screen)
}

// ownedHost starts a host whose config directory is also the process's
// (REMOHOP_CONFIG_DIR), so `remohop config` and `remohop host ...` act on it.
func ownedHost(t *testing.T, cfg hostd.Config) (*hostd.Host, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	cfg.Dir, cfg.Listen, cfg.Locals, cfg.Control = dir, "127.0.0.1:0", noLocals, true
	h, err := hostd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	// The control socket comes up asynchronously.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "host.sock")); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return h, dir
}

func set(t *testing.T, key, value string) {
	t.Helper()
	if err := config.Set(key, value); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, conn *peer.Conn, service string) (any, error) {
	t.Helper()
	s, err := conn.Open(testCtx(t), service, nil)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var v any
	return v, wire.ReadJSON(s, &v)
}

func runCmd(name string, stdin string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := cli.Lookup(name).Run(context.Background(), &cli.Env{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb}, args)
	return code, out.String(), errb.String()
}

func TestFeatureSwitches(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Everything is allowed by default; a service in no feature never is.
	if _, err := open(t, conn, svcShellish); err != nil {
		t.Fatalf("default: %v", err)
	}
	if v, err := open(t, conn, svcScreen); err != nil || v != true {
		t.Fatalf("default: input allowed = %v, %v", v, err)
	}
	if _, err := open(t, conn, svcUnmapped); err == nil || !strings.Contains(err.Error(), "not assigned to a permission feature") {
		t.Fatalf("unmapped service: %v", err)
	}
	if _, err := hostd.Ping(testCtx(t), conn, hostd.PingOptions{Count: 1}); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Switching a feature off applies to the session that is already
	// connected, and the refusal says what to do about it.
	set(t, hostd.FeatureSetting(perm.Shell), "off")
	_, err = open(t, conn, svcShellish)
	var refused *peer.RemoteError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), `feature "shell"`) || !strings.Contains(err.Error(), "remohop config feature shell on") {
		t.Fatalf("shell off: %v", err)
	}
	if _, err := open(t, conn, svcScreen); err != nil {
		t.Fatalf("screen must be unaffected by shell: %v", err)
	}
	// Input off: the screen service still opens, and is told no input.
	set(t, hostd.FeatureSetting(perm.Input), "off")
	if v, err := open(t, conn, svcScreen); err != nil || v != false {
		t.Fatalf("input off: view-only = %v, %v", v, err)
	}
	if code, out, _ := runCmd("status", "", "--json"); code != cli.ExitOK || !strings.Contains(out, `"features_off": [`) || !strings.Contains(out, `"shell"`) || !strings.Contains(out, `"input"`) {
		t.Fatalf("status does not list the features that are off:\n%s", out)
	}
	// ping and pairing cannot be switched off.
	for _, f := range perm.Features() {
		set(t, hostd.FeatureSetting(f), "off")
	}
	if _, err := hostd.Ping(testCtx(t), conn, hostd.PingOptions{Count: 1}); err != nil {
		t.Fatalf("ping with every feature off: %v", err)
	}
	// And back on.
	set(t, hostd.FeatureSetting(perm.Shell), "on")
	if _, err := open(t, conn, svcShellish); err != nil {
		t.Fatalf("shell back on: %v", err)
	}
	if err := config.Set(hostd.FeatureSetting(perm.Shell), "maybe"); err == nil {
		t.Fatal("feature accepted a value that is neither on nor off")
	}

	// The audit log has the login and the first use of each feature, once.
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	var events []string
	for {
		entries, err := hostd.ReadAudit(dir, 0)
		if err != nil {
			t.Fatal(err)
		}
		events = events[:0]
		for _, e := range entries {
			events = append(events, e.Event+":"+e.Fields["result"]+e.Fields["feature"])
		}
		if strings.Contains(strings.Join(events, " "), "session-end") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := strings.Join(events, " ")
	for _, want := range []string{"login:accepted", "feature:shell", "feature:screen", "session-end:"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("audit log has %d %q entries, want 1: %s", strings.Count(got, want), want, got)
		}
	}
}

func TestApproval(t *testing.T) {
	asked := make(chan hostd.Approval, 8)
	h, dir := ownedHost(t, hostd.Config{AskTimeout: 700 * time.Millisecond, OnApproval: func(a hostd.Approval) { asked <- a }})
	clientDir := t.TempDir()
	var waiting atomic.Int32
	opts := dial.Options{Dir: clientDir, Password: h.Password(), Locals: noLocals,
		Notify: func(msg string) {
			if strings.Contains(msg, "waiting for approval") {
				waiting.Add(1)
			}
		}}
	login := func(o dial.Options) (*peer.Conn, error) { return dial.Dial(testCtx(t), h.Addr().String(), o) }

	// ask off: nobody is asked.
	conn, err := login(opts)
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
	if len(asked) != 0 || waiting.Load() != 0 {
		t.Fatal("approval requested with ask off")
	}

	set(t, hostd.SettingAsk, "on")

	// Approved by the owner.
	go func() {
		a := <-asked
		if a.Auth != auth.KindPassword || a.Identity == "" || !a.From.Addr().IsLoopback() {
			t.Errorf("approval request %+v", a)
		}
		h.Decide(a.N, true)
	}()
	conn, err = login(opts)
	if err != nil {
		t.Fatalf("approved login: %v", err)
	}
	conn.Close()
	if waiting.Load() != 1 {
		t.Fatalf("client was told to wait %d times, want 1", waiting.Load())
	}

	// Denied.
	go func() { h.Decide((<-asked).N, false) }()
	if _, err = login(opts); auth.CodeOf(err) != auth.CodeDenied || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("denied login: %v", err)
	}

	// Nobody answers: refused after the timeout, and it shows as pending
	// meanwhile.
	start := time.Now()
	go func() {
		<-asked
		if n := len(h.Pending()); n != 1 {
			t.Errorf("%d pending approvals, want 1", n)
		}
	}()
	if _, err = login(opts); auth.CodeOf(err) != auth.CodeDenied {
		t.Fatalf("unanswered login: %v", err)
	}
	if took := time.Since(start); took < 600*time.Millisecond || took > 3*time.Second {
		t.Fatalf("unanswered login was refused after %s, want about 0.7 s", took)
	}
	if len(h.Pending()) != 0 {
		t.Fatal("timed-out approval still pending")
	}
	// A wrong password is refused without bothering the owner.
	bad := opts
	bad.Password = "not-the-password"
	if _, err := login(bad); auth.CodeOf(err) != auth.CodeAuthFailed || len(asked) != 0 {
		t.Fatalf("wrong password with ask on: %v (asked: %d)", err, len(asked))
	}

	// Paired clients are not asked unless ask-paired is on.
	tokenOnly := opts
	tokenOnly.Password = ""
	conn, err = login(tokenOnly)
	if err != nil || len(asked) != 0 {
		t.Fatalf("paired login with ask on: %v (asked: %d)", err, len(asked))
	}
	conn.Close()
	set(t, hostd.SettingAskPaired, "on")

	// From another terminal on the host: list, then approve by number.
	result := make(chan error, 1)
	go func() {
		c, err := login(tokenOnly)
		if err == nil {
			c.Close()
		}
		result <- err
	}()
	a := <-asked
	if a.Name != "laptop" || a.Auth != auth.KindToken {
		t.Fatalf("approval request for the paired client: %+v", a)
	}
	code, out, errOut := runCmd("host", "", "approve")
	if code != cli.ExitOK || !strings.Contains(out, "laptop") || !strings.Contains(out, "127.0.0.1:") {
		t.Fatalf("host approve (list): %d\n%s%s", code, out, errOut)
	}
	code, out, _ = runCmd("host", "", "approve", "--json")
	var listed []hostd.Approval
	if json.Unmarshal([]byte(out), &listed) != nil || len(listed) != 1 || listed[0].N != a.N {
		t.Fatalf("host approve --json: %d %s", code, out)
	}
	if code, _, errOut := runCmd("host", "", "deny", "999"); code != cli.ExitError || !strings.Contains(errOut, "no pending connection 999") {
		t.Fatalf("deny of an unknown request: %d %q", code, errOut)
	}
	if code, out, errOut := runCmd("host", "", "approve", strconv.Itoa(a.N)); code != cli.ExitOK || !strings.Contains(out, "approved") {
		t.Fatalf("host approve N: %d %s%s", code, out, errOut)
	}
	if err := <-result; err != nil {
		t.Fatalf("login approved from the command line: %v", err)
	}

	// The decisions are in the audit log.
	entries, _ := hostd.ReadAudit(dir, 0)
	var decisions []string
	for _, e := range entries {
		if e.Event == "approval" {
			decisions = append(decisions, e.Fields["decision"])
		}
	}
	if got := strings.Join(decisions, ","); got != "allowed,denied,timeout,allowed" {
		t.Fatalf("approval decisions in the audit log: %s", got)
	}
}

func TestOneTimePasswords(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	login := func(pw string) error {
		conn, err := dialHost(t, h, pw)
		if err == nil {
			conn.Close()
		}
		return err
	}
	first := h.Password()
	// Off (the default): the temporary password keeps working.
	for i := 0; i < 2; i++ {
		if err := login(first); err != nil {
			t.Fatal(err)
		}
	}
	if h.PasswordMode() != "temporary" || h.Password() != first {
		t.Fatalf("mode %s", h.PasswordMode())
	}

	set(t, hostd.SettingOTP, "on")
	if err := login(first); err != nil {
		t.Fatalf("first use of the one-time password: %v", err)
	}
	second := h.Password()
	if second == first || h.PasswordMode() != "one-time" {
		t.Fatalf("password not replaced after use (mode %s)", h.PasswordMode())
	}
	if err := login(first); auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("spent one-time password: %v", err)
	}
	if h.Password() != second {
		t.Fatal("a failed login replaced the password")
	}
	// `remohop host otp` prints it, `otp new` replaces it.
	code, out, errOut := runCmd("host", "", "otp")
	if code != cli.ExitOK || auth.NormalizePassword(out) != second {
		t.Fatalf("host otp: %d %q %q", code, out, errOut)
	}
	code, out, _ = runCmd("host", "", "otp", "new")
	third := auth.NormalizePassword(out)
	if code != cli.ExitOK || third == second || third != h.Password() {
		t.Fatalf("host otp new: %d %q", code, out)
	}
	if err := login(second); auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("rotated-away password: %v", err)
	}

	// A fixed password works alongside and is not consumed; set and
	// cleared with `remohop host passwd`, live.
	if code, _, errOut := runCmd("host", "tea time at four\ntea time at five\n", "passwd"); code == cli.ExitOK || !strings.Contains(errOut, "do not match") {
		t.Fatalf("passwd with a mismatch: %d %q", code, errOut)
	}
	if code, _, errOut := runCmd("host", "short\nshort\n", "passwd"); code == cli.ExitOK || !strings.Contains(errOut, "too short") {
		t.Fatalf("passwd with a short password: %d %q", code, errOut)
	}
	if code, _, errOut := runCmd("host", "tea time at four\ntea time at four\n", "passwd"); code != cli.ExitOK {
		t.Fatalf("passwd: %d %q", code, errOut)
	}
	if h.PasswordMode() != "fixed+one-time" {
		t.Fatalf("mode %s", h.PasswordMode())
	}
	for i := 0; i < 2; i++ {
		if err := login("tea-time at four"); err != nil {
			t.Fatalf("fixed password, login %d: %v", i+1, err)
		}
	}
	if h.Password() != third {
		t.Fatal("a fixed-password login consumed the one-time password")
	}
	if err := login(third); err != nil {
		t.Fatalf("one-time password next to a fixed one: %v", err)
	}
	if h.Password() == third {
		t.Fatal("one-time password not replaced")
	}
	// Status never shows the fixed password.
	if _, out, _ := runCmd("status", "", "--json", "--show-password"); strings.Contains(out, "tea") || !strings.Contains(out, `"password_mode": "fixed+one-time"`) {
		t.Fatalf("status:\n%s", out)
	}
	// otp off with a fixed password: only the fixed one.
	set(t, hostd.SettingOTP, "off")
	if err := login(h.Password()); err != nil || h.PasswordMode() != "fixed" {
		t.Fatalf("fixed only: %v, mode %s", err, h.PasswordMode())
	}
	if code, _, errOut := runCmd("host", "", "otp"); code == cli.ExitOK || !strings.Contains(errOut, "no temporary password") {
		t.Fatalf("host otp in fixed mode: %d %q", code, errOut)
	}
	if code, _, _ := runCmd("host", "", "passwd", "--clear"); code != cli.ExitOK || h.PasswordMode() != "temporary" {
		t.Fatalf("passwd --clear: mode %s", h.PasswordMode())
	}
	// Wrong guesses are still counted and locked out as always.
	for i := 0; i < 3; i++ {
		login("wrong-guess-here")
	}
	if err := login(h.Password()); auth.CodeOf(err) != auth.CodeLockedOut {
		t.Fatalf("after three wrong guesses: %v", err)
	}
}

func TestKeyLogin(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	clientDir := t.TempDir()
	addr := h.Addr().String()
	opts := dial.Options{Dir: clientDir, Locals: noLocals}

	clientID, err := auth.LoadIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	line := auth.FormatKeyLine(clientID.Public(), "work laptop")

	// The host's owner authorises the client's key, out of band.
	code, out, errOut := runCmd("host", "", "authorize", line, "--name", "alice")
	if code != cli.ExitOK || !strings.Contains(out, "remohop peers trust") {
		t.Fatalf("host authorize: %d\n%s%s", code, out, errOut)
	}
	if code, _, errOut := runCmd("host", "", "authorize", "remohop-key not-a-key"); code != cli.ExitUsage || !strings.Contains(errOut, "remohop key export") {
		t.Fatalf("authorize with garbage: %d %q", code, errOut)
	}
	// The printed command carries the host's key line.
	i := strings.Index(out, `"remohop-key `)
	hostLine := strings.Trim(strings.TrimSpace(out[i:]), `"`)
	if key, _, err := auth.ParseKeyLine(hostLine); err != nil || !key.Equal(h.PublicKey()) {
		t.Fatalf("host key line %q: %v", hostLine, err)
	}

	// Authorised, but the client does not know the host yet: its key is
	// not offered to whoever answers.
	if _, err := dial.Dial(testCtx(t), addr, opts); !errors.Is(err, dial.ErrNoCredential) {
		t.Fatalf("key login to an unknown host: %v", err)
	}
	store, _ := dial.OpenStore(clientDir)
	if err := store.Update(addr, func(p *dial.Peer) error {
		p.HostKey, p.KeyLogin = auth.EncodeKey(h.PublicKey()), true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := dial.Dial(testCtx(t), addr, opts)
	if err != nil {
		t.Fatalf("key login: %v", err)
	}
	if info := dial.InfoOf(conn); info.Auth != auth.KindKey {
		t.Fatalf("auth kind %s", info.Auth)
	}
	if _, err := hostd.Ping(testCtx(t), conn, hostd.PingOptions{Count: 1}); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runCmd("status", "", "--json"); code != cli.ExitOK || !strings.Contains(out, `"peer": "alice"`) || !strings.Contains(out, `"auth": "key"`) || !strings.Contains(out, `"path": "lan"`) {
		t.Fatalf("status with a key session:\n%s", out)
	}
	if _, out, _ := runCmd("host", "", "paired"); !strings.Contains(out, "alice") || !strings.Contains(out, "key") {
		t.Fatalf("paired:\n%s", out)
	}

	// Another identity is not let in on the strength of that entry.
	strangerDir := t.TempDir()
	sstore, _ := dial.OpenStore(strangerDir)
	sstore.Update(addr, func(p *dial.Peer) error {
		p.HostKey, p.KeyLogin = auth.EncodeKey(h.PublicKey()), true
		return nil
	})
	_, err = dial.Dial(testCtx(t), addr, dial.Options{Dir: strangerDir, Locals: noLocals})
	if !errors.Is(err, dial.ErrNoCredential) || !strings.Contains(err.Error(), "does not accept this machine's key") {
		t.Fatalf("unauthorised key: %v", err)
	}

	// Revoking closes the session and ends key logins.
	if code, _, _ := runCmd("host", "", "revoke", "alice"); code != cli.ExitOK {
		t.Fatal("revoke failed")
	}
	select {
	case <-conn.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("session survived the revocation of its key")
	}
	if _, err := dial.Dial(testCtx(t), addr, opts); !errors.Is(err, dial.ErrNoCredential) {
		t.Fatalf("key login after revocation: %v", err)
	}
	// Password logins are unaffected by any of this.
	withPW := opts
	withPW.Password = h.Password()
	conn, err = dial.Dial(testCtx(t), addr, withPW)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestAuditLog(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	clientDir := t.TempDir()
	opts := dial.Options{Dir: clientDir, Password: h.Password(), Locals: noLocals}
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), opts)
	if err != nil {
		t.Fatal(err)
	}
	// A pairing name is client-supplied text that ends up in the log.
	pairing, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "evil\x1b[2J\x1b]0;owned\x07name"})
	if err != nil {
		t.Fatal(err)
	}
	dial.SavePairing(dial.InfoOf(conn), pairing, "", clientDir)
	conn.Close()
	tokenOnly := opts
	tokenOnly.Password = ""
	conn, err = dial.Dial(testCtx(t), h.Addr().String(), tokenOnly)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	bad := opts
	bad.Dir, bad.Password = t.TempDir(), "wrong-password"
	dial.Dial(testCtx(t), h.Addr().String(), bad)

	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	for {
		raw, _ = os.ReadFile(filepath.Join(dir, hostd.AuditFile))
		refusedRaw, _ := os.ReadFile(filepath.Join(dir, hostd.AuditRefusedFile))
		raw = append(raw, refusedRaw...)
		if bytes.Count(raw, []byte(`"session-end"`)) == 2 && bytes.Contains(raw, []byte("auth_failed")) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if bytes.ContainsAny(raw, "\x1b\x07") || bytes.Contains(raw, []byte(`\u001b`)) {
		t.Fatalf("escape sequence in the audit log:\n%s", raw)
	}
	// Readable by the host's user only: mode bits on Unix, the config
	// directory's access-control list on Windows.
	for _, name := range []string{hostd.AuditFile, hostd.AuditRefusedFile} {
		if private, err := config.IsPrivate(filepath.Join(dir, name)); err != nil || !private {
			t.Fatalf("%s is not private to the user (%v)", name, err)
		}
	}
	code, out, _ := runCmd("log", "", "--json")
	var entries []hostd.AuditEntry
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &entries) != nil {
		t.Fatalf("remohop log --json: %d %s", code, out)
	}
	var refused, token bool
	for _, e := range entries {
		refused = refused || e.Event == "login" && e.Fields["result"] == "refused" && e.Fields["reason"] == "auth_failed" && e.Fields["from"] != ""
		token = token || e.Event == "login" && e.Fields["auth"] == "token" && strings.Contains(e.Fields["name"], "name")
	}
	if !refused || !token {
		t.Fatalf("audit log lacks the refused login or the token login:\n%s", out)
	}
	if code, out, _ := runCmd("log", "", "2"); code != cli.ExitOK || strings.Count(out, "\n") != 2 {
		t.Fatalf("remohop log 2:\n%s", out)
	}
	if code, _, _ := runCmd("log", "", "zero"); code != cli.ExitUsage {
		t.Fatal("remohop log accepted a non-number")
	}
}

func TestDeviceNameAndAliases(t *testing.T) {
	asked := make(chan hostd.Approval, 1)
	h, _ := ownedHost(t, hostd.Config{OnApproval: func(a hostd.Approval) { asked <- a }})
	if err := config.Set(hostd.SettingName, "bad\x1bname"); err == nil {
		t.Fatal("a name with an escape character was accepted")
	}
	set(t, hostd.SettingName, "office-pc")

	// The client (sharing this config dir) sends the name; the host shows
	// it in approval requests and in its session list.
	set(t, hostd.SettingAsk, "on")
	go func() { a := <-asked; h.Decide(a.N, a.Device == "office-pc") }()
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatalf("login with a device name: %v", err)
	}
	defer conn.Close()
	res, err := hostd.Ping(testCtx(t), conn, hostd.PingOptions{Count: 1})
	if err != nil || res.Info.Name != "office-pc" {
		t.Fatalf("ping info %+v, %v", res, err)
	}
	if code, out, _ := runCmd("status", "", "--json"); code != cli.ExitOK || !strings.Contains(out, `"device": "office-pc"`) || !strings.Contains(out, `"name": "office-pc"`) {
		t.Fatalf("status:\n%s", out)
	}
	set(t, hostd.SettingAsk, "off")

	// Top-level short forms do what the `remohop host` subcommands do.
	set(t, hostd.SettingOTP, "on")
	code, out, _ := runCmd("otp", "")
	if code != cli.ExitOK || auth.NormalizePassword(out) != h.Password() {
		t.Fatalf("remohop otp: %d %q", code, out)
	}
	if code, out, _ := runCmd("otp", "", "new"); code != cli.ExitOK || auth.NormalizePassword(out) != h.Password() {
		t.Fatalf("remohop otp new: %d %q", code, out)
	}
	if code, _, errOut := runCmd("passwd", "tea time at four\ntea time at four\n"); code != cli.ExitOK || h.PasswordMode() != "fixed+one-time" {
		t.Fatalf("remohop passwd: %d %q", code, errOut)
	}
	if code, _, _ := runCmd("passwd", "", "--clear"); code != cli.ExitOK {
		t.Fatal("remohop passwd --clear")
	}
	other, _ := auth.NewIdentity()
	if code, out, _ := runCmd("authorize", "", auth.FormatKeyLine(other.Public(), "tablet")); code != cli.ExitOK || !strings.Contains(out, "tablet") {
		t.Fatalf("remohop authorize: %d %q", code, out)
	}
	if _, ok, _ := h.Tokens().AuthorizedKey(other.Public()); !ok {
		t.Fatal("remohop authorize did not authorise the key")
	}
	// Help says what is true now: connections may be relayed.
	if c := cli.Lookup("host"); !strings.Contains(c.Usage, "relays") || !strings.Contains(c.Usage, "remohop host passwd") {
		t.Fatal("remohop help host is out of date")
	}
}

func TestTermKeep(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	if err := config.Set(hostd.SettingTermKeep, "0s"); err == nil {
		t.Fatal("term-keep accepted 0s")
	}
	if err := config.Set(hostd.SettingTermKeep, "forever"); err == nil {
		t.Fatal("term-keep accepted a non-duration")
	}
}

// A client let in by an authorised key (by hand, or by account trust) can
// pair, but the pairing is the key's child: revoking the key removes it and
// ends the sessions that stand on it, so pairing is no way to outlive a
// revocation.
func TestPairingFromKeyGoesWithKey(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	clientDir := t.TempDir()
	addr := h.Addr().String()
	opts := dial.Options{Dir: clientDir, Locals: noLocals}
	clientID, err := auth.LoadIdentity(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errOut := runCmd("host", "", "authorize", auth.FormatKeyLine(clientID.Public(), "laptop"), "--name", "account:laptop"); code != cli.ExitOK {
		t.Fatalf("host authorize: %d\n%s%s", code, out, errOut)
	}
	store, _ := dial.OpenStore(clientDir)
	if err := store.Update(addr, func(p *dial.Peer) error {
		p.HostKey, p.KeyLogin = auth.EncodeKey(h.PublicKey()), true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := dial.Dial(testCtx(t), addr, opts)
	if err != nil {
		t.Fatalf("key login: %v", err)
	}
	p, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "vm"})
	if err != nil {
		t.Fatalf("pair from a key session: %v", err)
	}
	if err := dial.SavePairing(dial.InfoOf(conn), p, "", clientDir); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := runCmd("host", "", "paired"); !strings.Contains(out, "pairing (via account:laptop)") {
		t.Fatalf("paired does not say which key minted the pairing:\n%s", out)
	}
	// The pairing works on its own while the key is authorised.
	store.Update(addr, func(p *dial.Peer) error { p.KeyLogin = false; return nil })
	tokConn, err := dial.Dial(testCtx(t), addr, opts)
	if err != nil {
		t.Fatalf("token login: %v", err)
	}
	if info := dial.InfoOf(tokConn); info.Auth != auth.KindToken {
		t.Fatalf("auth kind %s", info.Auth)
	}

	code, out, _ := runCmd("host", "", "revoke", "account:laptop")
	if code != cli.ExitOK || !strings.Contains(out, "(2 pairing(s))") {
		t.Fatalf("revoke: %d %q", code, out)
	}
	for _, c := range []*peer.Conn{conn, tokConn} {
		select {
		case <-c.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("a session survived the revocation of the key its pairing came from")
		}
	}
	if _, err := dial.Dial(testCtx(t), addr, opts); err == nil {
		t.Fatal("the key's pairing still logs in after the key was revoked")
	}
	if toks, _ := h.Tokens().List(); len(toks) != 0 {
		t.Fatalf("entries left after revoking the key: %+v", toks)
	}
}
