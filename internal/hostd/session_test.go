package hostd_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/wire"
)

// svcProbe reports how the session it runs on looks to a service handler.
const svcProbe = "hostdtest-probe"

type probeReport struct {
	// Ready is false when the stream was served while the host was still
	// setting the session up (see sessionSetup).
	Ready   bool   `json:"ready"`
	Session bool   `json:"session"`
	Guard   string `json:"guard"` // grant.Of(c).Name(), "" for none
}

// sessionSetup is what the probe compares against: tests set ready once
// the host has finished setting a session up, and probed is signalled when
// a probe stream is served.
var sessionSetup struct {
	ready  atomic.Bool
	probed chan struct{}
}

func init() {
	sessionSetup.probed = make(chan struct{}, 64)
	peer.Handle(svcProbe, func(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		r := probeReport{Ready: sessionSetup.ready.Load(), Session: hostd.SessionOf(c) != nil}
		if g := grant.Of(c); g != nil {
			r.Guard = g.Name()
		}
		select {
		case sessionSetup.probed <- struct{}{}:
		default:
		}
		return wire.WriteJSON(s, r)
	})
	perm.Register(svcProbe, perm.Always)
}

func probe(t *testing.T, conn *peer.Conn) (probeReport, error) {
	t.Helper()
	var r probeReport
	s, err := conn.Open(testCtx(t), svcProbe, nil)
	if err != nil {
		return r, err
	}
	defer s.Close()
	return r, wire.ReadJSON(s, &r)
}

// Finding 1: a stream the client opens the moment its login is accepted
// must not be served before the host has finished setting the session up
// (session, grant guard, admission). The hook stands in for the host being
// descheduled at the worst moment.
func TestNoStreamBeforeSessionIsReady(t *testing.T) {
	sessionSetup.ready.Store(false)
	restore := hostd.SetSessionBuiltHook(func() {
		// Give a stream every chance to be served now.
		select {
		case <-sessionSetup.probed:
		case <-time.After(500 * time.Millisecond):
		}
		sessionSetup.ready.Store(true)
	})
	t.Cleanup(restore)
	h, dir := ownedHost(t, hostd.Config{})
	_, code, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "narrow", For: time.Hour, Allow: []string{"info"}})
	if err != nil {
		t.Fatal(err)
	}
	clientDir := t.TempDir()
	store, err := dial.OpenStore(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.Redeem(store, h.Addr().String(), code, ""); err != nil {
		t.Fatal(err)
	}
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r, err := probe(t, conn)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Ready || !r.Session || r.Guard != "narrow" {
		t.Fatalf("a stream was served before the session was set up: %+v (want ready, a session and guard \"narrow\")", r)
	}
}

// Finding 1: logins that race past the grant's session limit get no
// service and no "accepted" record; only one of them gets in.
func TestOverLimitLoginsAreRefusedBeforeService(t *testing.T) {
	const n = 4
	var mu sync.Mutex
	var hold atomic.Bool
	arrived := 0
	all := make(chan struct{})
	restore := hostd.SetSessionBuiltHook(func() {
		if !hold.Load() {
			return
		}
		mu.Lock()
		if arrived++; arrived == n {
			close(all)
		}
		mu.Unlock()
		// Hold every racing login here until all of them have passed the
		// early limit check (or the host proves it never lets them).
		select {
		case <-all:
		case <-time.After(1500 * time.Millisecond):
		}
	})
	t.Cleanup(restore)
	h, dir := ownedHost(t, hostd.Config{})
	_, code, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "solo", For: time.Hour, Allow: []string{"info"}, MaxSessions: 1})
	if err != nil {
		t.Fatal(err)
	}
	clientDir := t.TempDir()
	store, err := dial.OpenStore(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.Redeem(store, h.Addr().String(), code, ""); err != nil {
		t.Fatal(err)
	}
	// Bind the credential to this client with a first login, then let it go.
	first, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Sessions()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	hold.Store(true)

	type result struct {
		conn *peer.Conn
		err  error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			c, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
			results <- result{c, err}
		}()
	}
	ok, denied := 0, 0
	for range n {
		r := <-results
		switch {
		case r.err == nil:
			ok++
			defer r.conn.Close()
		case auth.CodeOf(r.err) == auth.CodeDenied:
			denied++
		default:
			t.Errorf("login: %v", r.err)
		}
	}
	if ok != 1 || denied != n-1 {
		t.Errorf("%d logins accepted and %d refused, want 1 and %d", ok, denied, n-1)
	}
	time.Sleep(200 * time.Millisecond)
	entries, err := hostd.ReadAudit(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, e := range entries {
		if e.Event == "login" && e.Fields["result"] == "accepted" {
			accepted++
		}
	}
	// The binding login and the one that won.
	if accepted != 2 {
		t.Errorf("audit log has %d accepted logins, want 2", accepted)
	}
}

// Finding 8: reissuing a grant under the same name with wider rules must
// not widen sessions on the old credential, even if nothing tells the
// running host (no control socket call, no periodic sweep yet).
func TestReissuedGrantDoesNotWidenOldSessions(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	base := t.TempDir()
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent", For: time.Hour, Allow: []string{"files:read:" + base}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rfs.NewClient(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	target := filepath.Join(base, "f.txt")
	if err := c.WriteFile(testCtx(t), target, []byte("x"), 0o644); err == nil {
		t.Fatal("write allowed under files:read")
	}

	// Reissued with more, as `remohop host grant` does when the control
	// socket cannot be reached.
	_, newCode, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "agent", For: time.Hour,
		Allow: []string{"files:write:" + base, "shell"}})
	if err != nil {
		t.Fatal(err)
	}
	// The file stream opened before the reissue must not pick up the new
	// rules.
	if err := c.WriteFile(testCtx(t), target, []byte("x"), 0o644); err == nil {
		t.Fatal("an old session wrote a file under the reissued grant's rules")
	}
	// Nor may new streams of the old session.
	if out, err := run(t, conn, "echo widened", false); err == nil {
		t.Fatalf("an old session ran a command under the reissued grant's rules: %q", out)
	}
	if l, err := hostd.GetLimits(testCtx(t), conn); err == nil && l.Shell != "none" {
		t.Fatalf("an old session sees the reissued grant's limits: %+v", l)
	}
	// It is over, without waiting for the periodic check.
	waitClosed(t, conn, 2*time.Second)

	// The new credential gets the new rules.
	conn2, _, err := redeemAs(t, h, t.TempDir(), newCode)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, conn2, "echo widened", false); err != nil || !strings.Contains(out, "widened") {
		t.Fatalf("new credential: %q %v", out, err)
	}
}

// Finding 8: a login on the old credential that is still waiting for the
// owner's approval when the grant is reissued is not admitted.
func TestReissueDuringApprovalWait(t *testing.T) {
	asked := make(chan hostd.Approval, 4)
	h, dir := ownedHost(t, hostd.Config{AskTimeout: 5 * time.Second, OnApproval: func(a hostd.Approval) { asked <- a }})
	set(t, hostd.SettingAsk, "on")
	set(t, hostd.SettingAskPaired, "on")
	_, code, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "waiting", For: time.Hour, Allow: []string{"info"}})
	if err != nil {
		t.Fatal(err)
	}
	clientDir := t.TempDir()
	store, err := dial.OpenStore(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dial.Redeem(store, h.Addr().String(), code, ""); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		c, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: clientDir, Locals: noLocals})
		if err == nil {
			defer c.Close()
			_, err = run(t, c, "echo widened", false)
		}
		result <- err
	}()
	a := <-asked
	if _, _, err := hostd.IssueGrant(dir, hostd.GrantRequest{Name: "waiting", For: time.Hour, Allow: []string{"shell"}}); err != nil {
		t.Fatal(err)
	}
	h.Decide(a.N, true)
	if err := <-result; err == nil {
		t.Fatal("a login on the replaced credential was admitted and ran a command under the new rules")
	}
}
