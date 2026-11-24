package account_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
)

func noLocals() []netip.Addr { return nil }

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// mailer is the test's mailbox: it captures login codes. In the attack
// test it is also the malicious server's knowledge of every code.
type mailer struct {
	mu    sync.Mutex
	codes map[string]string
}

func (m *mailer) SendCode(_ context.Context, to, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[to] = code
	return nil
}

func (m *mailer) code(t *testing.T, email string) string {
	t.Helper()
	for range 500 {
		m.mu.Lock()
		c, ok := m.codes[email]
		delete(m.codes, email)
		m.mu.Unlock()
		if ok {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no code for %s", email)
	return ""
}

// server is a rendezvous server with accounts on a fixed loopback port, so
// that it can be stopped, tampered with and started again at the address
// clients have recorded.
type server struct {
	t      *testing.T
	dir    string
	addr   string
	mail   *mailer
	cancel func()
	done   chan error
	srv    *signal.Server
}

func startServer(t *testing.T) *server {
	t.Helper()
	s := &server{t: t, dir: t.TempDir(), mail: &mailer{codes: map[string]string{}}}
	s.start("127.0.0.1:0")
	t.Cleanup(s.stop)
	return s
}

func (s *server) start(listen string) {
	s.t.Helper()
	srv, err := signal.NewServer(signal.Config{DataDir: s.dir, Accounts: &signal.AccountsConfig{
		Mailer: s.mail, Open: true, StartBurst: 1000, EmailBurst: 1000, VerifyBurst: 1000, OpBurst: 10000,
	}})
	if err != nil {
		s.t.Fatal(err)
	}
	var pc net.PacketConn
	for range 50 {
		if pc, err = net.ListenPacket("udp", listen); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		s.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done, s.srv, s.addr = cancel, make(chan error, 1), srv, pc.LocalAddr().String()
	go func() { s.done <- srv.Serve(ctx, pc, nil) }()
}

func (s *server) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.srv.Close()
	s.cancel = nil
}

func (s *server) restart() {
	s.stop()
	s.start(s.addr)
}

func newDir(t *testing.T, name string) string {
	dir := t.TempDir()
	if name != "" {
		setConfig(t, dir, "name", name)
	}
	return dir
}

func setConfig(t *testing.T, dir, key, value string) {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	m := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	m[key] = value
	b, _ := json.Marshal(m)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (s *server) opts(dir string) account.Options { return account.Options{Dir: dir, Signal: s.addr} }

func (s *server) login(t *testing.T, dir, email, accountFP string) (*account.LoginResult, error) {
	t.Helper()
	ctx := testCtx(t)
	if err := account.RequestCode(ctx, s.opts(dir), email); err != nil {
		t.Fatal(err)
	}
	return account.Login(ctx, s.opts(dir), account.LoginRequest{Email: email, Code: s.mail.code(t, email), AccountFingerprint: accountFP})
}

// join logs dir in to the account of existing and has existing approve it.
func (s *server) join(t *testing.T, dir, existing, email string) {
	t.Helper()
	st, err := account.Load(existing)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.login(t, dir, email, st.Fingerprint())
	if err != nil || res.Status != "pending" {
		t.Fatalf("join: %+v %v", res, err)
	}
	if _, err := account.Approve(testCtx(t), s.opts(existing), res.Device); err != nil {
		t.Fatal(err)
	}
	if st, err := account.Refresh(testCtx(t), s.opts(dir)); err != nil || st.Status != account.StatusMember {
		t.Fatalf("after approval: %+v %v", st, err)
	}
}

func identity(t *testing.T, dir string) *auth.Identity {
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func startHost(t *testing.T, dir, sig string) *hostd.Host {
	t.Helper()
	h, err := hostd.New(hostd.Config{Dir: dir, Signal: sig, Listen: "127.0.0.1:0", Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-h.Registered():
	case <-time.After(10 * time.Second):
		t.Fatal("host did not register")
	}
	return h
}

func (s *server) dialHost(t *testing.T, dir, id string) (*peer.Conn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return dial.Dial(ctx, id, dial.Options{Signal: s.addr, Dir: dir, Locals: noLocals})
}

// waitOnline waits until the host with id is registered (after a restart
// of the server, hosts come back by themselves within seconds).
func (s *server) waitOnline(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sock, err := nat.Listen("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		sc, err := signal.Dial(ctx, sock, s.addr, signal.Options{})
		if err == nil {
			p, _ := sc.Presence(ctx, []string{id})
			sc.Close()
			if p[id] == signal.PresenceOnline {
				cancel()
				sock.Close()
				return
			}
		}
		cancel()
		sock.Close()
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("host %s did not come back online", id)
}

func trustedKeys(t *testing.T, dir string) map[string]bool {
	t.Helper()
	toks, err := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)).List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tok := range toks {
		if len(tok.Secret) == 0 && account.IsTrustEntry(tok.Name) {
			out[auth.EncodeKey(tok.ClientKey)] = true
		}
	}
	return out
}

func TestLoginApproveAndDevices(t *testing.T) {
	s := startServer(t)
	email := "me@example.com"
	a, b := newDir(t, "laptop"), newDir(t, "desktop")

	res, err := s.login(t, a, email, "")
	if err != nil || res.Status != "created" {
		t.Fatalf("first login: %+v %v", res, err)
	}
	// Joining needs the account fingerprint: none, or a wrong one, fails.
	if _, err := s.login(t, b, email, ""); !errors.Is(err, account.ErrConfirmAccount) {
		t.Fatalf("join without confirmation: %v", err)
	}
	if _, err := s.login(t, b, email, "0000:0000:0000:0000:0000"); err == nil || !strings.Contains(err.Error(), "not joining") {
		t.Fatalf("join with the wrong fingerprint: %v", err)
	}
	if _, err := account.Load(b); !errors.Is(err, account.ErrNotLoggedIn) {
		t.Fatal("a refused join left a session behind")
	}
	jb, err := s.login(t, b, email, strings.ToUpper(strings.ReplaceAll(res.Account, ":", "")))
	if err != nil || jb.Status != "pending" || jb.Account != res.Account {
		t.Fatalf("join: %+v %v", jb, err)
	}
	// A pending device sees no devices.
	if _, _, err := account.Devices(testCtx(t), s.opts(b)); err == nil {
		t.Fatal("pending device listed the account")
	}
	pending, err := account.Pending(testCtx(t), s.opts(a))
	if err != nil || len(pending) != 1 || pending[0].Identity != jb.Device || pending[0].Name != "desktop" {
		t.Fatalf("pending: %+v %v", pending, err)
	}
	// Only the complete fingerprint approves.
	if _, err := account.Approve(testCtx(t), s.opts(a), jb.Device[:9]); err == nil {
		t.Fatal("approved by a fingerprint prefix")
	}
	if _, err := account.Approve(testCtx(t), s.opts(a), jb.Device); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if st, err := account.WaitForApproval(ctx, s.opts(b), 50*time.Millisecond); err != nil || st.Status != account.StatusMember {
		t.Fatalf("wait: %+v %v", st, err)
	}

	// b sees both devices; a is a host and online.
	h := startHost(t, a, s.addr)
	devices, _, err := account.Devices(testCtx(t), s.opts(b))
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices: %+v %v", devices, err)
	}
	for _, d := range devices {
		switch d.Name {
		case "laptop":
			if !d.Online || d.ID != h.ID() || d.This {
				t.Errorf("laptop: %+v", d)
			}
		case "desktop":
			if !d.This || d.Online || d.ID != "" {
				t.Errorf("desktop: %+v", d)
			}
		default:
			t.Errorf("unexpected device %+v", d)
		}
	}

	// `remohop devices` lists the account first, then saved peers not in it.
	store, _ := dial.OpenStore(b)
	store.SetAlias("123456789", "elsewhere")
	rows, err := dial.Devices(testCtx(t), dial.DevicesOptions{Options: dial.Options{Dir: b, Signal: s.addr}})
	if err != nil || len(rows) != 3 {
		t.Fatalf("remohop devices: %+v %v", rows, err)
	}
	if !rows[0].Account || !rows[1].Account || rows[2].Account || rows[2].Target != "123456789" {
		t.Fatalf("order: %+v", rows)
	}
	for _, r := range rows[:2] {
		if r.Target == h.ID() && r.Presence != "online" {
			t.Errorf("host row: %+v", r)
		}
		if r.ThisDevice && r.Presence != "not_a_host" {
			t.Errorf("this device row: %+v", r)
		}
	}
	// Not logged in: exactly the old behaviour.
	plain := newDir(t, "")
	ps, _ := dial.OpenStore(plain)
	ps.SetAlias("123456789", "elsewhere")
	rows, err = dial.Devices(testCtx(t), dial.DevicesOptions{Options: dial.Options{Dir: plain}, Offline: true})
	if err != nil || len(rows) != 1 || rows[0].Account || rows[0].Presence != "not_checked" {
		t.Fatalf("without account: %+v %v", rows, err)
	}

	// Logout leaves the account.
	if err := account.Logout(testCtx(t), s.opts(b)); err != nil {
		t.Fatal(err)
	}
	devices, _, err = account.Devices(testCtx(t), s.opts(a))
	if err != nil || len(devices) != 1 {
		t.Fatalf("after logout: %+v %v", devices, err)
	}
}

// TestAccountTrust: a client in the same account is accepted without a
// password only while the host's owner has account-trust on, and only while
// it is in the account's signed device list.
func TestAccountTrust(t *testing.T) {
	s := startServer(t)
	email := "me@example.com"
	hdir, cdir := newDir(t, "server-box"), newDir(t, "laptop")
	if _, err := s.login(t, hdir, email, ""); err != nil {
		t.Fatal(err)
	}
	s.join(t, cdir, hdir, email)
	h := startHost(t, hdir, s.addr)
	// The client learns the host's ID and key from the account.
	if _, _, err := account.Devices(testCtx(t), s.opts(cdir)); err != nil {
		t.Fatal(err)
	}

	// account-trust off (the default): no password, no login.
	if _, _, err := account.SyncTrust(hdir); err != nil {
		t.Fatal(err)
	}
	_, err := s.dialHost(t, cdir, h.ID())
	if !errors.Is(err, dial.ErrNoCredential) || !strings.Contains(err.Error(), "account-trust") {
		t.Fatalf("trust off: %v", err)
	}
	// A password still works, and the host key it meets is the one the
	// account vouches for.
	conn, err := dial.Dial(testCtx(t), h.ID(), dial.Options{Signal: s.addr, Dir: cdir, Locals: noLocals, Password: h.Password()})
	if err != nil {
		t.Fatalf("password login: %v", err)
	}
	conn.Close()

	// account-trust on: the client's key is accepted.
	setConfig(t, hdir, account.SettingTrust, "on")
	if added, _, err := account.SyncTrust(hdir); err != nil || added != 1 {
		t.Fatalf("sync: %d %v", added, err)
	}
	conn, err = s.dialHost(t, cdir, h.ID())
	if err != nil {
		t.Fatalf("trust on: %v", err)
	}
	if info := dial.InfoOf(conn); info.Auth != auth.KindKey || !bytes.Equal(info.HostKey, h.PublicKey()) {
		t.Fatalf("info: %+v", info)
	}

	// Removing the client from the account ends its session and its
	// access.
	if _, err := account.Remove(testCtx(t), s.opts(hdir), "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, removed, err := account.SyncTrust(hdir); err != nil || removed != 1 {
		t.Fatalf("sync after removal: %d %v", removed, err)
	}
	select {
	case <-conn.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("session of a removed device still open")
	}
	if _, err := dial.Dial(testCtx(t), h.ID(), dial.Options{Signal: s.addr, Dir: cdir, Locals: noLocals}); err == nil {
		t.Fatal("removed device logged in")
	}

	// Turning account-trust off removes every key it added.
	cdir2 := newDir(t, "phone")
	s.join(t, cdir2, hdir, email)
	account.Refresh(testCtx(t), s.opts(hdir))
	account.SyncTrust(hdir)
	if len(trustedKeys(t, hdir)) != 1 {
		t.Fatalf("trusted: %v", trustedKeys(t, hdir))
	}
	setConfig(t, hdir, account.SettingTrust, "off")
	account.SyncTrust(hdir)
	if len(trustedKeys(t, hdir)) != 0 {
		t.Fatalf("still trusted with account-trust off: %v", trustedKeys(t, hdir))
	}
}

// TestMaliciousServerCannotInject is the attack account-trust is designed
// against: the rendezvous server (which issues the login codes and stores
// everything) tries to get its own device accepted by a host that has
// account-trust on.
func TestMaliciousServerCannotInject(t *testing.T) {
	s := startServer(t)
	email := "me@example.com"
	hdir, cdir := newDir(t, "host"), newDir(t, "laptop")
	if _, err := s.login(t, hdir, email, ""); err != nil {
		t.Fatal(err)
	}
	s.join(t, cdir, hdir, email)
	setConfig(t, hdir, account.SettingTrust, "on")
	h := startHost(t, hdir, s.addr)
	refreshAndSync := func() error {
		_, err := account.Refresh(testCtx(t), s.opts(hdir))
		if _, _, serr := account.SyncTrust(hdir); serr != nil {
			t.Fatal(serr)
		}
		return err
	}
	if err := refreshAndSync(); err != nil {
		t.Fatal(err)
	}
	// The client has listed the account once, so it knows the host's key.
	if _, _, err := account.Devices(testCtx(t), s.opts(cdir)); err != nil {
		t.Fatal(err)
	}
	clientKey := auth.EncodeKey(identity(t, cdir).Public())
	if k := trustedKeys(t, hdir); len(k) != 1 || !k[clientKey] {
		t.Fatalf("trusted before the attack: %v", k)
	}

	// The attacker's device: it attempts key logins to the host, with the
	// host's key (public, from the server) pinned.
	mdir := newDir(t, "laptop") // it may call itself anything
	mallory := identity(t, mdir)
	mstore, _ := dial.OpenStore(mdir)
	mstore.Update(h.ID(), func(p *dial.Peer) error {
		p.KeyLogin, p.HostKey = true, auth.EncodeKey(h.PublicKey())
		return nil
	})
	attempt := func(stage string) {
		t.Helper()
		if k := trustedKeys(t, hdir); k[auth.EncodeKey(mallory.Public())] {
			t.Fatalf("%s: the host trusts the attacker's key", stage)
		}
		conn, err := s.dialHost(t, mdir, h.ID())
		if err == nil {
			conn.Close()
			t.Fatalf("%s: the attacker logged in to the host", stage)
		}
		if !strings.Contains(err.Error(), "does not accept this machine's key") {
			t.Fatalf("%s: attacker's key login failed for another reason: %v", stage, err)
		}
	}

	// 1. The server knows every login code, so it can log its device in
	// to the account. That makes it a pending device, nothing more.
	res, err := s.login(t, mdir, email, mustFingerprint(t, hdir))
	if err != nil || res.Status != "pending" {
		t.Fatalf("attacker login: %+v %v", res, err)
	}
	if err := refreshAndSync(); err != nil {
		t.Fatal(err)
	}
	attempt("pending attacker")

	// 2. The server replaces the account's log with one of its own making
	// (its device as the founder, adding the host and itself). Every
	// signature in it is valid, but it is another account.
	journal := filepath.Join(s.dir, "accounts.journal")
	before, _ := os.ReadFile(journal)
	forged := ledger.Create(mallory, email, "host")
	fst, _ := ledger.Verify([]ledger.Entry{forged})
	addHost, _ := fst.Next(mallory, ledger.OpAdd, h.PublicKey(), "host")
	fst.Apply(addHost)
	addClient, _ := fst.Next(mallory, ledger.OpAdd, identity(t, cdir).Public(), "laptop")
	s.stop()
	rewriteAccount(t, journal, email, []ledger.Entry{forged, addHost, addClient})
	s.start(s.addr)
	s.waitOnline(t, h.ID())
	if err := refreshAndSync(); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("forged account accepted: %v", err)
	}
	attempt("forged account")
	// The client refuses the forged log as well.
	if _, err := account.Refresh(testCtx(t), s.opts(cdir)); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("client accepted the forged account: %v", err)
	}
	// The real member is still accepted from the log verified earlier.
	if conn, err := s.dialHost(t, cdir, h.ID()); err != nil {
		t.Fatalf("real member after the forged log: %v", err)
	} else {
		conn.Close()
	}

	// 3. The server appends an entry adding its device, signed by its own
	// key, to the real log. The real server refuses to even load that, so
	// it is shown to the host directly, as the answer it would get.
	s.stop()
	os.WriteFile(journal, before, 0o600)
	s.start(s.addr)
	s.waitOnline(t, h.ID())
	st, _ := account.Load(hdir)
	real, _ := st.Ledger()
	inject := ledger.Entry{Seq: real.Len, Prev: real.Head, Op: ledger.OpAdd, Device: mallory.Public(), Time: time.Now().Unix()}
	signAs(&inject, mallory)
	if err := account.CheckLog(hdir, append(st.Log, inject)); err == nil {
		t.Fatal("log extended by a non-member accepted")
	}
	if err := refreshAndSync(); err != nil {
		t.Fatalf("honest server again: %v", err)
	}
	attempt("injected entry")

	// 4. Rollback: after the client is removed, the server serves the old
	// log again, in which it was still a member.
	old, _ := os.ReadFile(journal)
	if _, err := account.Remove(testCtx(t), s.opts(hdir), "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := refreshAndSync(); err != nil {
		t.Fatal(err)
	}
	if trustedKeys(t, hdir)[clientKey] {
		t.Fatal("removed client still trusted")
	}
	s.stop()
	os.WriteFile(journal, old, 0o600)
	s.start(s.addr)
	s.waitOnline(t, h.ID())
	if err := refreshAndSync(); err == nil || !strings.Contains(err.Error(), "does not extend") {
		t.Fatalf("rolled-back log accepted: %v", err)
	}
	if trustedKeys(t, hdir)[clientKey] {
		t.Fatal("rollback restored a removed device")
	}
}

func mustFingerprint(t *testing.T, dir string) string {
	st, err := account.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Fingerprint()
}

// signAs signs an entry with a key outside the ledger package's helpers,
// the way an attacker would.
func signAs(e *ledger.Entry, id *auth.Identity) {
	e.Signer = id.Public()
	e.Sig = id.Sign(ledger.SignedBytes(e))
}

// rewriteAccount replaces the log of email's account in a server journal,
// keeping its sessions, as a malicious operator could.
func rewriteAccount(t *testing.T, path, email string, log []ledger.Entry) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	recs := map[string]map[string]any{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var line map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if put, ok := line["put"]; ok {
			var rec map[string]any
			json.Unmarshal(put, &rec)
			k := rec["email"].(string)
			if _, seen := recs[k]; !seen {
				order = append(order, k)
			}
			recs[k] = rec
		}
	}
	f.Close()
	target := base64.StdEncoding.EncodeToString(ledger.EmailHash(email))
	if recs[target] == nil {
		t.Fatal("account not in the journal")
	}
	recs[target]["log"] = log
	var out bytes.Buffer
	for _, k := range order {
		b, _ := json.Marshal(map[string]any{"put": recs[k]})
		out.Write(append(b, '\n'))
	}
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
