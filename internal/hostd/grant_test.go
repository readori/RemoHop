package hostd_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/svc/shell"
)

func init() { hostd.SilenceNotifications() }

// redeem issues a grant on the host in dir and logs in with it from a
// fresh client identity. It returns the session (or the login error) and
// the client's config directory, so a test can log in again as the same
// client.
func redeem(t *testing.T, h *hostd.Host, dir string, req hostd.GrantRequest) (*peer.Conn, string, error) {
	t.Helper()
	_, code, err := hostd.IssueGrant(dir, req)
	if err != nil {
		t.Fatalf("issue %s: %v", req.Name, err)
	}
	clientDir := t.TempDir()
	return redeemAs(t, h, clientDir, code)
}

func redeemAs(t *testing.T, h *hostd.Host, clientDir, code string) (*peer.Conn, string, error) {
	t.Helper()
	store, err := dial.OpenStore(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.Redeem(store, h.Addr().String(), code, ""); err != nil {
		t.Fatal(err)
	}
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
	if err == nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, clientDir, err
}

// relogin dials again as an earlier client.
func relogin(t *testing.T, h *hostd.Host, clientDir string) (*peer.Conn, error) {
	t.Helper()
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
	if err == nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, err
}

func run(t *testing.T, conn *peer.Conn, cmd string, tty bool, env ...string) (string, error) {
	t.Helper()
	res, err := shell.Capture(testCtx(t), conn, shell.ExecRequest{Command: cmd, TTY: tty, Env: env, Timeout: 20 * time.Second}, 0)
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

func wantRefusal(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("not refused; want %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("refusal %q lacks %q", err, p)
		}
	}
}

func waitClosed(t *testing.T, conn *peer.Conn, within time.Duration) {
	t.Helper()
	select {
	case <-conn.Done():
	case <-time.After(within):
		t.Fatalf("session still open after %s", within)
	}
}

func TestGrantServicesAndCommands(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "claude-session", For: time.Hour,
		Allow: []string{"shell:run-only"}, Commands: []string{"echo", "git status"}})
	if err != nil {
		t.Fatal(err)
	}

	if out, err := run(t, conn, "echo granted", false); err != nil || !strings.Contains(out, "granted") {
		t.Fatalf("allowed command: %q %v", out, err)
	}
	_, err = run(t, conn, "whoami", false)
	wantRefusal(t, err, `command "whoami" is not allowed by grant claude-session`, "allowed commands: echo; git status")
	_, err = run(t, conn, "echo a; whoami", false)
	wantRefusal(t, err, "is not allowed by grant claude-session", "shell operator")
	_, err = run(t, conn, "echo tty", true)
	wantRefusal(t, err, "a terminal (tty) for a command is not allowed by grant claude-session")
	_, err = run(t, conn, "echo env", false, "FOO=bar")
	wantRefusal(t, err, "setting environment variables is not allowed by grant claude-session")
	if _, err := shell.Spawn(testCtx(t), conn, shell.SpawnRequest{Command: "uptime"}); err == nil || !strings.Contains(err.Error(), "is not allowed by grant") {
		t.Fatalf("spawn of a command off the list: %v", err)
	}

	// Services outside the grant are refused at the stream, with the
	// grant named; the host's own feature switches still apply on top.
	_, err = open(t, conn, svcShellish)
	wantRefusal(t, err, "not allowed by grant claude-session")
	_, err = shell.Sessions(testCtx(t), conn)
	wantRefusal(t, err, "shell (interactive terminal) is not allowed by grant claude-session")
	_, err = rfs.NewClient(testCtx(t), conn)
	wantRefusal(t, err, "files:read (fs) is not allowed by grant claude-session")
	if _, err := hostd.Pair(testCtx(t), conn, hostd.PairRequest{Name: "escape"}); err == nil || !strings.Contains(err.Error(), "pairing is not allowed by grant claude-session") {
		t.Fatalf("pairing from a granted session: %v", err)
	}
	if _, err := hostd.Ping(testCtx(t), conn, hostd.PingOptions{Count: 1}); err != nil {
		t.Fatalf("ping: %v", err)
	}
	set(t, hostd.FeatureSetting(perm.Shell), "off")
	_, err = run(t, conn, "echo off", false)
	wantRefusal(t, err, "feature \"shell\" is switched off")
	set(t, hostd.FeatureSetting(perm.Shell), "on")

	l, err := hostd.GetLimits(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	if l.Grant != "claude-session" || l.Shell != "run-only" || len(l.Commands) != 2 || l.Expires == nil || l.Approve != "none" {
		t.Fatalf("limits: %+v", l)
	}
	if d := strings.Join(l.Denied, ","); !strings.Contains(d, "files") || !strings.Contains(d, "interactive shell") {
		t.Fatalf("denied: %s", d)
	}
}

func TestGrantInteractiveShell(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	// The owner's own terminal session, started with full access.
	full, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "shelly", For: time.Hour, Allow: []string{"shell"}})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, conn, "echo tty-ok", true); err != nil || !strings.Contains(out, "tty-ok") {
		t.Fatalf("full shell grant, tty command: %q %v", out, err)
	}
	// A granted session sees only terminals it started.
	ownerSess := make(chan error, 1)
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	go func() {
		_, err := shell.Attach(ctx, full, shell.AttachRequest{Name: "owner-term"}, shell.AttachIO{Stdin: blockingReader{ctx}, Stdout: discard{}})
		ownerSess <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		list, err := shell.Sessions(testCtx(t), full)
		if err == nil && len(list) == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	list, err := shell.Sessions(testCtx(t), conn)
	if err != nil || len(list) != 0 {
		t.Fatalf("granted session lists the owner's terminals: %v %v", list, err)
	}
	if err := shell.KillSession(testCtx(t), conn, "owner-term"); err == nil {
		t.Fatal("granted session killed the owner's terminal")
	}
	_, err = shell.Attach(testCtx(t), conn, shell.AttachRequest{Name: "owner-term"}, shell.AttachIO{Stdin: strings.NewReader(""), Stdout: discard{}})
	wantRefusal(t, err, "not allowed by grant shelly")
	cancel()
}

type blockingReader struct{ ctx context.Context }

func (b blockingReader) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestGrantFiles(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	base := t.TempDir()
	proj, out, secret := filepath.Join(base, "project"), filepath.Join(base, "project", "out"), filepath.Join(base, "secret")
	for _, d := range []string{out, secret} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(proj, "README"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(secret, "key"), []byte("s3cret"), 0o600)
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "reader", For: time.Hour,
		Allow: []string{"files:read:" + proj, "files:write:" + out}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rfs.NewClient(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := testCtx(t)
	if b, err := c.ReadFile(ctx, filepath.Join(proj, "README")); err != nil || string(b) != "hello" {
		t.Fatalf("read in root: %q %v", b, err)
	}
	if _, err := c.ReadDir(ctx, proj); err != nil {
		t.Fatalf("list root: %v", err)
	}
	_, err = c.ReadFile(ctx, filepath.Join(secret, "key"))
	wantRefusal(t, err, "files:read is not allowed by grant reader", "outside the readable roots")
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("refusal should be a permission error: %v", err)
	}
	err = c.WriteFile(ctx, filepath.Join(proj, "README"), []byte("changed"), 0o644)
	wantRefusal(t, err, "files:write is not allowed by grant reader", "outside the writable roots")
	if err := c.WriteFile(ctx, filepath.Join(out, "result.txt"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write in write root: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "result.txt")); string(b) != "done" {
		t.Fatalf("written: %q", b)
	}
	wantRefusal(t, c.Remove(ctx, filepath.Join(proj, "README")), "files:write is not allowed by grant reader")
	wantRefusal(t, c.Rename(ctx, filepath.Join(out, "result.txt"), filepath.Join(proj, "moved")), "files:write is not allowed by grant reader")
	wantRefusal(t, c.Link(ctx, filepath.Join(secret, "key"), filepath.Join(out, "hard")), "is not allowed by grant reader")
	if runtime.GOOS != "windows" {
		// A link out of the writable root cannot be made there, and
		// one planted on the host cannot be read through.
		wantRefusal(t, c.Symlink(ctx, secret, filepath.Join(out, "esc")), "must point inside its root")
		if err := os.Symlink(secret, filepath.Join(out, "esc")); err != nil {
			t.Fatal(err)
		}
		_, err = c.ReadFile(ctx, filepath.Join(out, "esc", "key"))
		wantRefusal(t, err, "files:read is not allowed by grant reader")
	}
	// Bulk transfers are checked too.
	if r, err := c.Download(ctx, filepath.Join(secret, "key"), 0); err == nil {
		r.Close()
		t.Fatal("download outside the roots")
	} else {
		wantRefusal(t, err, "files:read is not allowed by grant reader")
	}
}

func TestGrantRedeemBinding(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	_, code, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "once", For: time.Hour, Allow: []string{"info"}})
	if err != nil {
		t.Fatal(err)
	}
	first, firstDir, err := redeemAs(t, h, t.TempDir(), code)
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	first.Close()
	// Somebody else holding the same code gets nowhere: it is bound now.
	if _, _, err := redeemAs(t, h, t.TempDir(), code); !tokenRefused(err) {
		t.Fatalf("second identity with the same code: %v", err)
	}
	if conn, err := relogin(t, h, firstDir); err != nil {
		t.Fatalf("the redeemer again: %v", err)
	} else {
		conn.Close()
	}

	// Bound to a key in advance: only that key gets in.
	id, err := auth.LoadIdentity(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	_, code, err = hostd.IssueGrant(dir, hostd.GrantRequest{Name: "keyed", For: time.Hour, Allow: []string{"info"}, ClientKey: id.Public()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := redeemAs(t, h, t.TempDir(), code); !tokenRefused(err) {
		t.Fatalf("other identity with a key-bound code: %v", err)
	}
	if _, _, err := redeemAs(t, h, firstDir, code); err != nil {
		t.Fatalf("the bound identity: %v", err)
	}
	// An ordinary pairing of the same identity does not disturb its grants.
	toks, _ := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)).List()
	n := 0
	for _, tk := range toks {
		if tk.Grant != "" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("grant tokens: %d", n)
	}
}

func TestGrantRevokeMidSession(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, clientDir, err := redeem(t, h, dir, hostd.GrantRequest{Name: "temp", For: time.Hour, Allow: []string{"shell:run-only"}})
	if err != nil {
		t.Fatal(err)
	}
	// A long command is running when the grant goes.
	done := make(chan error, 1)
	go func() {
		_, err := shell.Exec(context.Background(), conn, shell.ExecRequest{Command: sleepCmd(30)})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	code, out, errOut := runCmd("host", "", "revoke", "temp")
	if code != cli.ExitOK || !strings.Contains(out, "revoked grant temp") || !strings.Contains(out, "ended 1 live session") {
		t.Fatalf("remohop host revoke: %d %q %q", code, out, errOut)
	}
	waitClosed(t, conn, 3*time.Second)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the running command survived the revocation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the running command was not ended")
	}
	if _, err := relogin(t, h, clientDir); err == nil {
		t.Fatal("login after revocation")
	}
	if gs, _ := hostd.OpenGrantStore(dir).List(); len(gs) != 0 {
		t.Fatalf("grant still stored: %+v", gs)
	}
}

func sleepCmd(s int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + itoa(s)
	}
	return "sleep " + itoa(s)
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestGrantExpiry(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, clientDir, err := redeem(t, h, dir, hostd.GrantRequest{Name: "brief", For: 1500 * time.Millisecond, Allow: []string{"info", "shell:run-only"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, conn, "echo still", false); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	time.Sleep(1600 * time.Millisecond)
	// New streams are refused at once, before the periodic check runs.
	_, err = run(t, conn, "echo late", false)
	wantRefusal(t, err, "grant brief expired at")
	waitClosed(t, conn, 8*time.Second)
	if _, err := relogin(t, h, clientDir); auth.CodeOf(err) != auth.CodeDenied {
		t.Fatalf("login after expiry: %v", err)
	}
	gs, _ := hostd.OpenGrantStore(dir).List()
	if len(gs) != 1 || gs[0].StateAt(time.Now()) != grant.StateExpired {
		t.Fatalf("expired grant: %+v", gs)
	}
}

func TestGrantSessionLimit(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, clientDir, err := redeem(t, h, dir, hostd.GrantRequest{Name: "solo", For: time.Hour, Allow: []string{"info"}, MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relogin(t, h, clientDir); auth.CodeOf(err) != auth.CodeDenied {
		t.Fatalf("second concurrent session: %v", err)
	}
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Sessions()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := relogin(t, h, clientDir); err != nil {
		t.Fatalf("after the first session ended: %v", err)
	}
}

func TestGrantPanic(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	full, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	a, aDir, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent-a", For: time.Hour, Allow: []string{"info"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent-b", For: time.Hour, Allow: []string{"info"}})
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCmd("host", "", "panic")
	if code != cli.ExitOK || !strings.Contains(out, "suspended 2 grant(s); ended 2 granted session(s)") {
		t.Fatalf("remohop host panic: %d %q", code, out)
	}
	waitClosed(t, a, 3*time.Second)
	waitClosed(t, b, 3*time.Second)
	select {
	case <-full.Done():
		t.Fatal("panic without --all ended a full-access session")
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := relogin(t, h, aDir); auth.CodeOf(err) != auth.CodeDenied {
		t.Fatalf("login with a suspended grant: %v", err)
	}
	if code, out, _ := runCmd("host", "", "grants"); code != cli.ExitOK || strings.Count(out, "suspended") != 2 {
		t.Fatalf("remohop host grants:\n%s", out)
	}
	if code, out, _ := runCmd("host", "", "resume", "agent-a"); code != cli.ExitOK || !strings.Contains(out, "resumed agent-a") {
		t.Fatalf("remohop host resume: %q", out)
	}
	if _, err := relogin(t, h, aDir); err != nil {
		t.Fatalf("login after resume: %v", err)
	}
	if code, out, _ := runCmd("host", "", "panic", "--all"); code != cli.ExitOK || !strings.Contains(out, "session(s)") {
		t.Fatalf("remohop host panic --all: %q", out)
	}
	waitClosed(t, full, 3*time.Second)
}

func TestGrantApproval(t *testing.T) {
	asked := make(chan hostd.Approval, 4)
	h, dir := ownedHost(t, hostd.Config{AskTimeout: 700 * time.Millisecond, OnApproval: func(a hostd.Approval) { asked <- a }})
	base := t.TempDir()
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "careful", For: time.Hour,
		Allow: []string{"shell:run-only", "files:write:" + base}, Approve: grant.ApproveEach})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(allow bool) {
		select {
		case a := <-asked:
			if a.Grant == "" || a.Action == "" {
				t.Errorf("approval request: %+v", a)
			}
			if pend := h.Pending(); len(pend) != 1 || pend[0].Action != a.Action {
				t.Errorf("pending: %+v", pend)
			}
			h.Decide(a.N, allow)
		case <-time.After(10 * time.Second):
			t.Error("nobody was asked")
		}
	}

	go answer(true)
	if out, err := run(t, conn, "echo approved", false); err != nil || !strings.Contains(out, "approved") {
		t.Fatalf("approved command: %q %v", out, err)
	}
	go answer(false)
	_, err = run(t, conn, "echo denied", false)
	wantRefusal(t, err, "the host's owner denied: exec: echo denied")
	// Nobody answers: refused once the wait times out.
	_, err = run(t, conn, "echo unanswered", false)
	wantRefusal(t, err, "nobody approved", "in time")
	<-asked

	// File changes: one approval covers the steps of one write.
	c, err := rfs.NewClient(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go answer(true)
	target := filepath.Join(base, "f.txt")
	if err := c.WriteFile(testCtx(t), target, []byte("x"), 0o644); err != nil {
		t.Fatalf("approved write: %v", err)
	}
	if err := c.Chmod(testCtx(t), target, 0o600); err != nil {
		t.Fatalf("second change to the same path within a minute: %v", err)
	}
	select {
	case a := <-asked:
		t.Fatalf("asked again for the same path: %+v", a)
	default:
	}
	// Reading needs no approval.
	if _, err := c.ReadFile(testCtx(t), target); err != nil {
		t.Fatalf("read: %v", err)
	}

	// "first": one approval per session.
	conn2, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "once-asked", For: time.Hour, Allow: []string{"shell:run-only"}, Approve: grant.ApproveFirst})
	if err != nil {
		t.Fatal(err)
	}
	go answer(true)
	if _, err := run(t, conn2, "echo one", false); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, conn2, "echo two", false); err != nil {
		t.Fatalf("second command under approve=first: %v", err)
	}

	var decisions []string
	entries, _ := hostd.GrantAudit(dir, "careful", 0)
	for _, e := range entries {
		if e.Event == "approval" {
			decisions = append(decisions, e.Fields["decision"])
		}
	}
	if strings.Join(decisions, ",") != "allowed,denied,timeout,allowed" {
		t.Fatalf("approval audit: %v", decisions)
	}
}

func TestGrantAuditAndRecording(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "watched", For: time.Hour, Allow: []string{"shell:run-only", "info"},
		Commands: []string{"echo"}, Record: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, conn, "echo recorded-output", false); err != nil {
		t.Fatal(err)
	}
	run(t, conn, "whoami", false)
	open(t, conn, svcScreen)
	conn.Close()

	var events []string
	deadline := time.Now().Add(5 * time.Second)
	for {
		events = events[:0]
		entries, _ := hostd.GrantAudit(dir, "watched", 0)
		for _, e := range entries {
			if e.Fields["grant"] != "watched" {
				t.Fatalf("entry without the grant: %+v", e)
			}
			s := e.Event
			if e.Fields["command"] != "" {
				s += ":" + e.Fields["command"]
			}
			events = append(events, s)
		}
		if strings.Contains(strings.Join(events, ","), "session-end") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := strings.Join(events, ",")
	for _, want := range []string{"login", "grant-action:echo recorded-output", "grant-recording", "grant-refused:whoami", "grant-refused", "session-end"} {
		if !strings.Contains(got, want) {
			t.Fatalf("audit of the grant lacks %s: %s", want, got)
		}
	}
	code, out, _ := runCmd("host", "", "audit", "--grant", "watched")
	if code != cli.ExitOK || !strings.Contains(out, "echo recorded-output") || !strings.Contains(out, "recordings") {
		t.Fatalf("remohop host audit:\n%s", out)
	}

	casts, err := hostd.Recordings(dir, "watched")
	if err != nil || len(casts) != 1 {
		t.Fatalf("recordings: %v %v", casts, err)
	}
	f, err := os.Open(casts[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan()
	var hdr struct {
		Version int    `json:"version"`
		Title   string `json:"title"`
	}
	if json.Unmarshal(sc.Bytes(), &hdr) != nil || hdr.Version != 2 || !strings.Contains(hdr.Title, "echo recorded-output") {
		t.Fatalf("cast header: %s", sc.Bytes())
	}
	var body strings.Builder
	for sc.Scan() {
		body.Write(sc.Bytes())
	}
	if !strings.Contains(body.String(), "recorded-output") || !strings.Contains(body.String(), `"m", "exit 0"`) {
		t.Fatalf("cast body: %s", body.String())
	}
}

func TestGrantInputFeature(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	view, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "viewer", For: time.Hour, Allow: []string{"screen"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := open(t, view, svcScreen); err != nil || v != false {
		t.Fatalf("screen grant: input allowed = %v, %v", v, err)
	}
	ctl, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "driver", For: time.Hour, Allow: []string{"desktop"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := open(t, ctl, svcScreen); err != nil || v != true {
		t.Fatalf("desktop grant: input allowed = %v, %v", v, err)
	}
}

func TestGrantCLI(t *testing.T) {
	_, dir := ownedHost(t, hostd.Config{})
	code, out, errOut := runCmd("host", "", "grant", "--name", "claude-session", "--for", "2h",
		"--allow", "shell:run-only,files:read:"+dir+",screenshot", "--approve", "first", "--record")
	if code != cli.ExitOK {
		t.Fatalf("remohop host grant: %d %s", code, errOut)
	}
	for _, want := range []string{"granted claude-session until", "shell:run-only", "files:read under " + dir, "approve  first", "remohopg1-", "remohop peers redeem", "claude mcp add remohop -- remohop mcp --target"} {
		if !strings.Contains(out, want) {
			t.Fatalf("grant output lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range [][]string{
		{"grant", "x", "--allow", "everything"},
		{"grant", "bad name", "--allow", "info"},
		{"grant", "x"},
		{"grant", "x", "--allow", "info", "--for", "-1h"},
		{"grant", "x", "--allow", "info", "--approve", "maybe"},
		{"grant", "x", "--allow", "shell", "--cmd", "git"},
	} {
		if code, _, _ := runCmd("host", "", bad...); code == cli.ExitOK {
			t.Errorf("remohop host %q succeeded", bad)
		}
	}
	code, out, _ = runCmd("host", "", "grants", "--json")
	var rows []hostd.GrantStatus
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Fatalf("remohop host grants --json: %s", out)
	}
	if r := rows[0]; r.Name != "claude-session" || r.State != grant.StateActive || r.Approve != "first" || !r.Record || r.Identity != "" || r.MaxSessions != grant.DefaultMaxSessions {
		t.Fatalf("grant row: %+v", r)
	}
	if strings.Contains(out, "remohopg1-") {
		t.Fatal("remohop host grants shows the credential")
	}
	if code, out, _ := runCmd("host", "", "revoke", "claude-session"); code != cli.ExitOK || !strings.Contains(out, "revoked grant claude-session") {
		t.Fatalf("revoke: %q", out)
	}
	if code, out, _ := runCmd("host", "", "grants"); code != cli.ExitOK || !strings.Contains(out, "no grants") {
		t.Fatalf("after revoke: %q", out)
	}
}

// tokenRefused reports a login the host refused because it does not accept
// the saved token (the dialler then reports that no credential is left).
func tokenRefused(err error) bool {
	return auth.CodeOf(err) == auth.CodeUnknownToken || errors.Is(err, dial.ErrNoCredential) && strings.Contains(err.Error(), "no longer works")
}

// A session under a grant sees and handles only the background jobs started
// under that grant: the owner's jobs, and their logs, are not its business
// (security audit finding 10).
func TestGrantSeesOnlyItsOwnJobs(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	full, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	ownerJob, err := shell.Spawn(testCtx(t), full, shell.SpawnRequest{Command: "echo owner-secret", Name: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shell.Wait(testCtx(t), full, ownerJob.ID); err != nil {
		t.Fatal(err)
	}

	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent", For: time.Hour,
		Allow: []string{"shell:run-only"}, Commands: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := shell.Spawn(testCtx(t), conn, shell.SpawnRequest{Command: "echo agent-output"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shell.Wait(testCtx(t), conn, mine.ID); err != nil {
		t.Fatal(err)
	}
	js, err := shell.Jobs(testCtx(t), conn)
	if err != nil || len(js) != 1 || js[0].ID != mine.ID || js[0].Grant != "agent" {
		t.Fatalf("jobs seen by the grant: %+v %v", js, err)
	}
	for _, ref := range []string{ownerJob.ID, "backup"} {
		if r, err := shell.Logs(testCtx(t), conn, shell.LogsRequest{Job: ref}); err == nil {
			b, err := io.ReadAll(r)
			r.Close()
			if err == nil || strings.Contains(string(b), "owner-secret") {
				t.Fatalf("the grant read the owner's job log %s: %q %v", ref, b, err)
			}
		}
		if _, err := shell.Kill(testCtx(t), conn, ref, ""); err == nil || !strings.Contains(err.Error(), "no job") {
			t.Fatalf("kill of the owner's job %s: %v", ref, err)
		}
		if _, err := shell.Wait(testCtx(t), conn, ref); err == nil {
			t.Fatalf("wait on the owner's job %s succeeded", ref)
		}
		if _, err := shell.RemoveJobs(testCtx(t), conn, shell.RemoveRequest{Jobs: []string{ref}}); err == nil {
			t.Fatalf("remove of the owner's job %s succeeded", ref)
		}
	}
	r, err := shell.Logs(testCtx(t), conn, shell.LogsRequest{Job: mine.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if !strings.Contains(string(b), "agent-output") {
		t.Fatalf("own job log: %q", b)
	}
	removed, err := shell.RemoveJobs(testCtx(t), conn, shell.RemoveRequest{Exited: true})
	if err != nil || len(removed) != 1 || removed[0] != mine.ID {
		t.Fatalf("remove --exited under the grant: %v %v", removed, err)
	}
	// Full access still sees the owner's job.
	if js, err := shell.Jobs(testCtx(t), full); err != nil || len(js) != 1 || js[0].ID != ownerJob.ID {
		t.Fatalf("jobs seen with full access: %+v %v", js, err)
	}
}
