package signal

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
)

// fakeMailer captures login codes instead of mailing them.
type fakeMailer struct {
	mu    sync.Mutex
	codes map[string]string
	sent  int
}

func (m *fakeMailer) SendCode(_ context.Context, to, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.codes == nil {
		m.codes = map[string]string{}
	}
	m.codes[to] = code
	m.sent++
	return nil
}

func (m *fakeMailer) code(t *testing.T, email string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		c, ok := m.codes[email]
		delete(m.codes, email)
		m.mu.Unlock()
		if ok {
			return c
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no code mailed to %s", email)
	return ""
}

type acctServer struct {
	t      *testing.T
	dir    string
	addr   string
	srv    *Server
	mailer *fakeMailer
	stop   func()
}

// generous are limits that stay out of the way of tests not about them.
func generous(cfg AccountsConfig) AccountsConfig {
	if cfg.StartBurst == 0 {
		cfg.StartBurst, cfg.EmailBurst, cfg.VerifyBurst, cfg.OpBurst = 1000, 1000, 1000, 1000
	}
	return cfg
}

func startAccounts(t *testing.T, dir string, cfg AccountsConfig) *acctServer {
	t.Helper()
	as := &acctServer{t: t, dir: dir, mailer: &fakeMailer{}}
	if m, ok := cfg.Mailer.(*fakeMailer); ok {
		as.mailer = m
	}
	cfg.Mailer = as.mailer
	if cfg.Domains == nil && cfg.Emails == nil {
		cfg.Open = true
	}
	srv, err := NewServer(Config{DataDir: dir, Accounts: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, pc, nil) }()
	as.srv, as.addr = srv, pc.LocalAddr().String()
	stopped := false
	as.stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		<-done
		srv.Close()
	}
	t.Cleanup(as.stop)
	return as
}

func (as *acctServer) client() *Client {
	as.t.Helper()
	sock, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		as.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, sock, as.addr, Options{})
	if err != nil {
		as.t.Fatal(err)
	}
	as.t.Cleanup(func() { c.Close(); sock.Close() })
	return c
}

func tctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newID(t *testing.T) *auth.Identity {
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// device is one logged-in identity.
type device struct {
	id    *auth.Identity
	token []byte
	c     *Client
}

func (as *acctServer) login(email string, id *auth.Identity) (*device, *AccountReply) {
	as.t.Helper()
	c := as.client()
	ctx := tctx(as.t)
	if err := c.AccountLogin(ctx, email); err != nil {
		as.t.Fatal(err)
	}
	r, err := c.AccountVerify(ctx, id, email, as.mailer.code(as.t, email), "dev", "linux/amd64")
	if err != nil {
		as.t.Fatal(err)
	}
	return &device{id: id, token: r.Token, c: c}, r
}

func (d *device) call(t *testing.T, op string, args AccountArgs) (*AccountReply, error) {
	return d.c.AccountCall(tctx(t), d.id, d.token, op, args)
}

func TestAccountCodeFlowAndMembership(t *testing.T) {
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{}))
	ctx := tctx(t)
	email := "me@example.com"

	// A wrong code is refused; the right one logs in.
	c := as.client()
	if err := c.AccountLogin(ctx, email); err != nil {
		t.Fatal(err)
	}
	code := as.mailer.code(t, email)
	a := newID(t)
	wrong := "00000000"
	if code == wrong {
		wrong = "11111111"
	}
	if _, err := c.AccountVerify(ctx, a, email, wrong, "", ""); CodeOf(err) != CodeBadCode {
		t.Fatalf("wrong code: %v", err)
	}
	r, err := c.AccountVerify(ctx, a, email, FormatCode(code), "laptop", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != AccountNew || len(r.Token) != 32 {
		t.Fatalf("first login: %+v", r)
	}
	// Single use.
	if _, err := c.AccountVerify(ctx, a, email, code, "", ""); CodeOf(err) != CodeBadCode {
		t.Fatalf("code reused: %v", err)
	}
	da := &device{id: a, token: r.Token, c: c}
	create := ledger.Create(a, email, "laptop")
	r, err = da.call(t, "append", AccountArgs{Entry: &create})
	if err != nil || r.State != AccountMember || len(r.Log) != 1 {
		t.Fatalf("create: %+v %v", r, err)
	}

	// A second device waits for approval and sees nothing meanwhile.
	b := newID(t)
	db, r := as.login(email, b)
	if r.State != AccountPending || !bytes.Equal(r.AccountID, create.Hash()) || r.Log != nil {
		t.Fatalf("second device: %+v", r)
	}
	if _, err := db.call(t, "devices", AccountArgs{}); CodeOf(err) != CodeForbidden {
		t.Fatalf("pending device listed devices: %v", err)
	}
	// It cannot approve itself, not even with a well-formed entry.
	st, _ := ledger.Verify([]ledger.Entry{create})
	self := ledger.Entry{Seq: 1, Prev: st.Head, Op: ledger.OpAdd, Device: b.Public(), Signer: b.Public()}
	if _, err := db.call(t, "append", AccountArgs{Entry: &self}); CodeOf(err) != CodeForbidden {
		t.Fatalf("self-approval: %v", err)
	}
	// The member sees the request and approves it.
	r, err = da.call(t, "state", AccountArgs{})
	if err != nil || len(r.Pending) != 1 || !bytes.Equal(r.Pending[0].Key, b.Public()) {
		t.Fatalf("pending list: %+v %v", r, err)
	}
	add, err := st.Next(a, ledger.OpAdd, b.Public(), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := da.call(t, "append", AccountArgs{Entry: &add}); err != nil {
		t.Fatal(err)
	}
	// A stale append (same position) is a conflict.
	again, _ := st.Next(a, ledger.OpAdd, newID(t).Public(), "")
	if _, err := da.call(t, "append", AccountArgs{Entry: &again}); CodeOf(err) != CodeConflict {
		t.Fatalf("stale append: %v", err)
	}
	r, err = db.call(t, "devices", AccountArgs{})
	if err != nil || r.State != AccountMember || len(r.Devices) != 2 {
		t.Fatalf("devices: %+v %v", r, err)
	}
	if _, err := ledger.Verify(r.Log); err != nil {
		t.Fatal(err)
	}
}

func TestAccountCodeExpiryAndLockout(t *testing.T) {
	var mu sync.Mutex
	now := time.Now()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{Now: clock}))
	ctx := tctx(t)
	c := as.client()
	id := newID(t)

	// Expiry.
	c.AccountLogin(ctx, "a@example.com")
	code := as.mailer.code(t, "a@example.com")
	advance(11 * time.Minute)
	if _, err := c.AccountVerify(ctx, id, "a@example.com", code, "", ""); CodeOf(err) != CodeBadCode {
		t.Fatalf("expired code: %v", err)
	}

	// Five wrong codes burn the right one and lock the address.
	c.AccountLogin(ctx, "b@example.com")
	code = as.mailer.code(t, "b@example.com")
	for i := range 5 {
		wrong := strings.Repeat(string(rune('0'+i)), CodeDigits)
		if wrong == code {
			wrong = "98765432"
		}
		if _, err := c.AccountVerify(ctx, id, "b@example.com", wrong, "", ""); CodeOf(err) != CodeBadCode {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := c.AccountVerify(ctx, id, "b@example.com", code, "", ""); CodeOf(err) != CodeBadCode {
		t.Fatalf("right code after lockout: %v", err)
	}
	err := c.AccountLogin(ctx, "b@example.com")
	if e, ok := err.(*Error); !ok || e.Code != CodeRateLimited || e.RetryAfter < 10*time.Minute {
		t.Fatalf("new code during lockout: %v", err)
	}
	advance(16 * time.Minute)
	if err := c.AccountLogin(ctx, "b@example.com"); err != nil {
		t.Fatalf("after lockout: %v", err)
	}
}

func TestAccountRateLimitsAndPolicy(t *testing.T) {
	as := startAccounts(t, t.TempDir(), AccountsConfig{
		Domains: []string{"example.com"}, Emails: []string{"guest@other.org"},
		StartBurst: 3, StartEvery: time.Hour, EmailBurst: 2, EmailEvery: time.Hour,
		VerifyBurst: 2, VerifyEvery: time.Hour, OpBurst: 1000,
	})
	ctx := tctx(t)
	c := as.client()
	if err := c.AccountLogin(ctx, "x@elsewhere.net"); CodeOf(err) != CodeNotAllowed {
		t.Fatalf("address outside the allowlist: %v", err)
	}
	if err := c.AccountLogin(ctx, "guest@other.org"); err != nil {
		t.Fatalf("invited address: %v", err)
	}
	if err := c.AccountLogin(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.AccountLogin(ctx, "me@example.com"); err != nil {
		t.Fatal(err)
	}
	// Per source: the fourth request from this address is refused.
	if err := c.AccountLogin(ctx, "you@example.com"); CodeOf(err) != CodeRateLimited {
		t.Fatalf("per-source limit: %v", err)
	}
	// Per address, from another source (::1 is another source than 127.0.0.1).
	sock, err := nat.Listen("[::1]:0")
	if err == nil {
		c2, err := Dial(ctx, sock, as.addr, Options{})
		if err == nil {
			t.Cleanup(func() { c2.Close(); sock.Close() })
			if err := c2.AccountLogin(ctx, "me@example.com"); CodeOf(err) != CodeRateLimited {
				t.Fatalf("per-address limit: %v", err)
			}
		}
		sock.Close()
	}
	// Code checks per source.
	id := newID(t)
	for range 2 {
		c.AccountVerify(ctx, id, "me@example.com", "00000001", "", "")
	}
	if _, err := c.AccountVerify(ctx, id, "me@example.com", "00000001", "", ""); CodeOf(err) != CodeRateLimited {
		t.Fatalf("verify limit: %v", err)
	}
}

func TestAccountSessionsAndRevocation(t *testing.T) {
	as := startAccounts(t, t.TempDir(), generous(AccountsConfig{}))
	email := "me@example.com"
	a, b := newID(t), newID(t)
	da, _ := as.login(email, a)
	create := ledger.Create(a, email, "")
	if _, err := da.call(t, "append", AccountArgs{Entry: &create}); err != nil {
		t.Fatal(err)
	}
	db, _ := as.login(email, b)
	st, _ := ledger.Verify([]ledger.Entry{create})
	add, _ := st.Next(a, ledger.OpAdd, b.Public(), "")
	da.call(t, "append", AccountArgs{Entry: &add})

	// A token is bound to its key: someone else's key cannot use it.
	thief := &device{id: newID(t), token: db.token, c: db.c}
	if _, err := thief.call(t, "state", AccountArgs{}); CodeOf(err) != CodeNotLoggedIn {
		t.Fatalf("stolen token: %v", err)
	}
	// Removing a device ends its session.
	st.Apply(add)
	rm, _ := st.Next(a, ledger.OpRemove, b.Public(), "")
	if _, err := da.call(t, "append", AccountArgs{Entry: &rm}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.call(t, "state", AccountArgs{}); CodeOf(err) != CodeNotLoggedIn {
		t.Fatalf("removed device's session: %v", err)
	}
	// Logout ends one's own session.
	if _, err := da.call(t, "logout", AccountArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := da.call(t, "state", AccountArgs{}); CodeOf(err) != CodeNotLoggedIn {
		t.Fatalf("after logout: %v", err)
	}
}

func TestAccountVisibilityPresenceAndPersistence(t *testing.T) {
	dir := t.TempDir()
	as := startAccounts(t, dir, generous(AccountsConfig{}))
	a, other := newID(t), newID(t)
	da, _ := as.login("me@example.com", a)
	create := ledger.Create(a, "me@example.com", "")
	da.call(t, "append", AccountArgs{Entry: &create})
	do, _ := as.login("you@example.com", other)
	oc := ledger.Create(other, "you@example.com", "")
	do.call(t, "append", AccountArgs{Entry: &oc})

	// Each account sees only its own devices.
	r, err := do.call(t, "devices", AccountArgs{})
	if err != nil || len(r.Devices) != 1 || !bytes.Equal(r.Devices[0].Key, other.Public()) {
		t.Fatalf("other account: %+v %v", r, err)
	}
	// Presence: a's device registers as a host and shows online.
	r, _ = da.call(t, "devices", AccountArgs{})
	if r.Devices[0].Online || r.Devices[0].ID != "" {
		t.Fatalf("before registering: %+v", r.Devices[0])
	}
	host := as.client()
	reg, err := host.Register(tctx(t), a)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = da.call(t, "devices", AccountArgs{})
	if !r.Devices[0].Online || r.Devices[0].ID != reg.ID {
		t.Fatalf("registered: %+v", r.Devices[0])
	}
	host.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, _ = da.call(t, "devices", AccountArgs{})
		if !r.Devices[0].Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("still online after disconnecting")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.Devices[0].LastSeen.IsZero() {
		t.Fatal("no last-seen time")
	}

	// Restart: accounts and sessions survive; nothing secret is on disk.
	as.stop()
	b, err := os.ReadFile(filepath.Join(dir, accountsFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("example.com")) || bytes.Contains(b, []byte(encodeB64(da.token))) {
		t.Fatal("address or token in the clear in the journal")
	}
	as2 := startAccounts(t, dir, generous(AccountsConfig{}))
	da.c = as2.client()
	r, err = da.call(t, "devices", AccountArgs{})
	if err != nil || r.State != AccountMember {
		t.Fatalf("after restart: %+v %v", r, err)
	}
	// Delete.
	if _, err := da.call(t, "delete", AccountArgs{}); err != nil {
		t.Fatal(err)
	}
	if _, err := da.call(t, "state", AccountArgs{}); CodeOf(err) != CodeNotLoggedIn {
		t.Fatalf("after delete: %v", err)
	}
}

func encodeB64(b []byte) string {
	s := auth.EncodeKey(b) // raw standard base64, as JSON would not use; check both
	return s[:20]
}

func TestNoAccountsByDefault(t *testing.T) {
	tn := newTestNet(t, Limits{})
	h := tn.net.Host("203.0.113.5")
	c := tn.dial(tn.socket(h), Options{})
	if err := c.AccountLogin(tctx(t), "me@example.com"); CodeOf(err) != CodeNoAccounts {
		t.Fatalf("accounts off: %v", err)
	}
}

func TestAccountsConfigFlags(t *testing.T) {
	log := discardLogger()
	if _, err := accountsConfig(accountFlags{devCodes: true, listen: ":21116"}, log); err == nil {
		t.Fatal("dev codes on a public address accepted")
	}
	if _, err := accountsConfig(accountFlags{devCodes: true, listen: "0.0.0.0:21116"}, log); err == nil {
		t.Fatal("dev codes on 0.0.0.0 accepted")
	}
	for _, l := range []string{"127.0.0.1:21116", "[::1]:1", "localhost:5"} {
		if _, err := accountsConfig(accountFlags{devCodes: true, listen: l}, log); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
	}
	if _, err := accountsConfig(accountFlags{devCodes: true, devCodesPublic: true, listen: ":1"}, log); err != nil {
		t.Fatal(err)
	}
	if _, err := accountsConfig(accountFlags{smtpHost: "smtp.example.com", mailFrom: "a@example.com"}, log); err == nil {
		t.Fatal("open mail relay accepted")
	}
	cfg, err := accountsConfig(accountFlags{smtpHost: "smtp.example.com", smtpPort: 465, mailFrom: "a@example.com", domains: "example.com"}, log)
	if err != nil || cfg.Mailer.(*SMTPMailer).Security != "tls" {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := accountsConfig(accountFlags{smtpHost: "h", mailFrom: "a@example.com", domains: "x.org", smtpUser: "u"}, log); err == nil {
		t.Fatal("SMTP user without password accepted")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
