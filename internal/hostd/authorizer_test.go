package hostd_test

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

// testAuthz is what the test authorizer admits: grants by client key.
var testAuthz struct {
	mu     sync.Mutex
	grants map[string]grant.Grant
}

func setAuthz(key ed25519.PublicKey, g *grant.Grant) {
	testAuthz.mu.Lock()
	defer testAuthz.mu.Unlock()
	if testAuthz.grants == nil {
		testAuthz.grants = map[string]grant.Grant{}
	}
	if g == nil {
		delete(testAuthz.grants, string(key))
	} else {
		testAuthz.grants[string(key)] = *g
	}
}

func init() {
	hostd.RegisterAuthorizer(hostd.Authorizer{Name: "test-team", Grant: func(_ *hostd.Host, key ed25519.PublicKey) (grant.Grant, string, bool) {
		testAuthz.mu.Lock()
		defer testAuthz.mu.Unlock()
		g, ok := testAuthz.grants[string(key)]
		return g, "member " + auth.Fingerprint(key)[:9], ok
	}})
}

// keyClient makes a client identity that logs in to h with its key alone,
// pinning h's key as `remohop peers trust` would.
func keyClient(t *testing.T, h *hostd.Host) (string, ed25519.PublicKey) {
	t.Helper()
	dir := t.TempDir()
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := dial.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(h.Addr().String(), func(p *dial.Peer) error {
		p.HostKey, p.KeyLogin = auth.EncodeKey(h.PublicKey()), true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir, id.Public()
}

func keyLogin(t *testing.T, h *hostd.Host, dir string) (*peer.Conn, error) {
	t.Helper()
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: dir, Locals: noLocals})
	if err == nil {
		t.Cleanup(func() { conn.Close() })
	}
	return conn, err
}

func TestAuthorizer(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	clientDir, key := keyClient(t, h)

	// Unknown to the authorizer: refused like any unknown key.
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("a key nobody vouches for logged in")
	}

	g := &grant.Grant{Name: "team.ops", Allow: []string{"shell:run-only", "info"}, Commands: []string{"echo"}, MaxSessions: 1}
	setAuthz(key, g)
	defer setAuthz(key, nil)
	conn, err := keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatal(err)
	}
	// The grant is enforced as an owner's grant would be.
	if out, err := run(t, conn, "echo team-ok", false); err != nil || !strings.Contains(out, "team-ok") {
		t.Fatalf("allowed command: %q %v", out, err)
	}
	_, err = run(t, conn, "whoami", false)
	wantRefusal(t, err, "team.ops")
	if _, err := open(t, conn, svcScreen); err == nil {
		t.Fatal("screen outside the grant was allowed")
	}
	sessions := h.Sessions()
	if len(sessions) != 1 || sessions[0].Grant() != "team.ops" || sessions[0].Auth.Scope != hostd.AuthorizerScope("test-team") {
		t.Fatalf("session: %+v", sessions)
	}
	// MaxSessions applies.
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("second session beyond max_sessions")
	}

	// A changed grant ends the session at its next use.
	setAuthz(key, &grant.Grant{Name: "team.other", Allow: []string{"info"}})
	_, err = run(t, conn, "echo again", false)
	wantRefusal(t, err, "changed from team.ops to team.other")

	// The new grant applies to a new login; withdrawal ends it too.
	setAuthz(key, g)
	conn, err = keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatal(err)
	}
	setAuthz(key, nil)
	_, err = run(t, conn, "echo gone", false)
	wantRefusal(t, err, "withdrawn")
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("withdrawn key logged in")
	}

	// An expired grant admits nothing.
	exp := *g
	exp.Expires = time.Now().Add(-time.Minute)
	setAuthz(key, &exp)
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("expired grant admitted a login")
	}

	// The audit log names the grant.
	entries, err := hostd.ReadAudit(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, e := range entries {
		if e.Event == "grant-action" && e.Fields["grant"] == "team.ops" && strings.Contains(e.Fields["command"], "team-ok") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no audit entry for the team grant's command: %+v", entries)
	}
}

// Sessions an authorizer admitted show it and the grant in `status` and the
// control listing, `host panic` ends them, and `host disconnect`'s control
// op ends them too.
func TestAuthorizerSessionListing(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	clientDir, key := keyClient(t, h)
	setAuthz(key, &grant.Grant{Name: "team.ops", Allow: []string{"info"}, MaxSessions: 2})
	defer setAuthz(key, nil)
	conn, err := keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatal(err)
	}

	// A paired (password) session shows neither field.
	pw, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()

	code, out, errs := runCmd("status", "", "--json")
	if code != 0 {
		t.Fatalf("status --json: %d %s", code, errs)
	}
	var st hostd.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil || len(st.Clients) != 2 {
		t.Fatalf("status json: %v %s", err, out)
	}
	var viaAuthz, plain int
	for _, c := range st.Clients {
		if c.Authorizer == "test-team" && c.Grant == "team.ops" {
			viaAuthz++
		} else if c.Authorizer == "" && c.Grant == "" {
			plain++
		}
	}
	if viaAuthz != 1 || plain != 1 {
		t.Fatalf("status clients: %+v", st.Clients)
	}
	if strings.Count(out, `"authorizer"`) != 1 {
		t.Fatalf("authorizer should be omitted for the plain session: %s", out)
	}

	_, out, _ = runCmd("status", "")
	if !strings.Contains(out, "via test-team (team.ops)") || strings.Count(out, "via ") != 1 {
		t.Fatalf("status text: %s", out)
	}

	ss, err := hostd.ControlSessions(dir)
	if err != nil || len(ss) != 2 {
		t.Fatalf("ControlSessions: %+v %v", ss, err)
	}
	var authzID int
	for _, s := range ss {
		if s.Authorizer == "test-team" && s.Grant == "team.ops" {
			authzID = s.ID
		} else if s.Authorizer != "" || s.Grant != "" {
			t.Fatalf("plain session labelled: %+v", s)
		}
	}
	if authzID == 0 {
		t.Fatalf("no authorizer session listed: %+v", ss)
	}

	code, out, errs = runCmd("host", "", "sessions")
	if code != 0 || strings.Count(out, "via test-team (team.ops)") != 1 || strings.Count(out, "via ") != 1 {
		t.Fatalf("host sessions text: %d %s %s", code, out, errs)
	}
	code, out, errs = runCmd("host", "", "sessions", "--json")
	var js []hostd.ControlSession
	if err := json.Unmarshal([]byte(out), &js); code != 0 || err != nil || len(js) != 2 || strings.Count(out, `"authorizer"`) != 1 || strings.Count(out, `"grant"`) != 1 {
		t.Fatalf("host sessions json: %d %v %s %s", code, err, out, errs)
	}

	// Disconnect ends just that session.
	if n, err := hostd.ControlDisconnect(dir, authzID); err != nil || n != 1 {
		t.Fatalf("ControlDisconnect: %d %v", n, err)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not end the authorizer session")
	}

	// Panic ends a fresh one.
	conn, err = keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runCmd("host", "", "panic"); code != 0 || !strings.Contains(out, "ended 1 granted") {
		t.Fatalf("host panic: %d %s %s", code, out, errs)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host panic did not end the authorizer session")
	}
	select {
	case <-pw.Done():
		t.Fatal("host panic ended the paired session")
	default:
	}
}
