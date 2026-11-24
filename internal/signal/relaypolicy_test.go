package signal

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/nat"
)

// lockedBuffer collects log output for assertions.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// policyServer is a rendezvous server with accounts and a relay on
// loopback.
func policyServer(t *testing.T, relay RelayConfig) (*acctServer, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	as := &acctServer{t: t, dir: t.TempDir(), mailer: &fakeMailer{}}
	acfg := generous(AccountsConfig{Open: true, Mailer: as.mailer})
	if relay.Idle == 0 {
		relay.Idle = 400 * time.Millisecond
	}
	srv, err := NewServer(Config{DataDir: as.dir, Accounts: &acfg, Relay: &relay,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
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
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	return as, logs
}

// member makes id a verified member of a fresh account.
func (as *acctServer) member(email string, id *auth.Identity) {
	as.t.Helper()
	d, _ := as.login(email, id)
	create := ledger.Create(id, email, "dev")
	if _, err := d.call(as.t, "append", AccountArgs{Entry: &create}); err != nil {
		as.t.Fatal(err)
	}
}

// relayHost registers a host that accepts every relay without binding.
func (as *acctServer) relayHost(id *auth.Identity) string {
	as.t.Helper()
	c := as.client()
	reg, err := c.Register(tctx(as.t), id)
	if err != nil {
		as.t.Fatal(err)
	}
	c.OnRelay = func([]byte, netip.AddrPort) error { return nil }
	go c.ServeOffers(context.Background(), func(Offer) Answer { return Answer{} })
	return reg.ID
}

func (as *acctServer) introduce(c *Client, hostID string) *Introduction {
	as.t.Helper()
	in, err := c.Connect(tctx(as.t), hostID, nil, nat.TypeUnknown)
	if err != nil {
		as.t.Fatal(err)
	}
	return in
}

func TestRelayTiersNeedProof(t *testing.T) {
	as, logs := policyServer(t, RelayConfig{Rate: 250_000, AccountRate: 1_000_000})
	ctx := tctx(t)
	member := newID(t)
	as.member("member@example.com", member)
	hostID := as.relayHost(newID(t)) // an anonymous host

	// Anonymous: the lower rate.
	c := as.client()
	if _, err := c.Relay(ctx, as.introduce(c, hostID).Nonce); err != nil {
		t.Fatal(err)
	}
	waitLog(t, logs, "tier=anonymous kbps=2000")

	// A member proving its key: the account rate.
	c2 := as.client()
	if _, err := c2.RelayAs(ctx, as.introduce(c2, hostID).Nonce, member); err != nil {
		t.Fatal(err)
	}
	waitLog(t, logs, "tier=account kbps=8000")

	// Claiming the member's key without its signature gets nothing.
	c3 := as.client()
	in := as.introduce(c3, hostID)
	for _, proof := range [][]byte{nil, make([]byte, 64)} {
		err := c3.roundTrip(ctx, func(st *quic.Stream) error {
			if err := writeMsg(st, request{T: "relay", V: protoVersion, Nonce: in.Nonce, Key: member.Public(), Proof: proof}); err != nil {
				return err
			}
			var resp response
			if err := readMsg(st, &resp); err != nil {
				return err
			}
			return resp.err()
		})
		if CodeOf(err) != CodeBadRequest {
			t.Fatalf("claimed key without a valid proof: %v", err)
		}
	}
	// A proof made on another connection does not carry over.
	c4 := as.client()
	in4 := as.introduce(c4, hostID)
	cs := c3.q.ConnectionState().TLS
	exp, _ := cs.ExportKeyingMaterial(relayExporterLabel, nil, 32)
	err := c4.roundTrip(ctx, func(st *quic.Stream) error {
		writeMsg(st, request{T: "relay", V: protoVersion, Nonce: in4.Nonce, Key: member.Public(), Proof: relayProof(member, in4.Nonce, exp)})
		var resp response
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	if CodeOf(err) != CodeBadRequest {
		t.Fatalf("proof replayed from another connection: %v", err)
	}

	// A host that is a member lifts the session to the account tier
	// even for an anonymous client: its key was proven at registration.
	memberHost := newID(t)
	as.member("host@example.com", memberHost)
	hid := as.relayHost(memberHost)
	c5 := as.client()
	if _, err := c5.Relay(ctx, as.introduce(c5, hid).Nonce); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "tier=account"); n < 2 {
		waitLog(t, logs, "tier=account kbps=8000")
	}
}

func waitLog(t *testing.T, logs *lockedBuffer, want string) {
	t.Helper()
	for start := time.Now(); !strings.Contains(logs.String(), want); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestRelayModeAndPerSource(t *testing.T) {
	as, _ := policyServer(t, RelayConfig{ForAccounts: true, PerSource: 2})
	ctx := tctx(t)
	member := newID(t)
	as.member("m@example.com", member)
	hostID := as.relayHost(newID(t))

	c := as.client()
	_, err := c.Relay(ctx, as.introduce(c, hostID).Nonce)
	if CodeOf(err) != CodeNoRelay || !strings.Contains(err.Error(), "only for devices of an account") {
		t.Fatalf("anonymous relay on an accounts-only server: %v", err)
	}
	// Members are relayed, at most two sessions per address (all of
	// these come from 127.0.0.1).
	for i := range 2 {
		c := as.client()
		if _, err := c.RelayAs(ctx, as.introduce(c, hostID).Nonce, member); err != nil {
			t.Fatalf("member relay %d: %v", i+1, err)
		}
	}
	c3 := as.client()
	_, err = c3.RelayAs(ctx, as.introduce(c3, hostID).Nonce, member)
	if CodeOf(err) != CodeNoRelay || !strings.Contains(err.Error(), "already has 2 relayed sessions") {
		t.Fatalf("third session from one address: %v", err)
	}
	// Idle sessions end and free their slots.
	time.Sleep(time.Second)
	c4 := as.client()
	if _, err := c4.RelayAs(ctx, as.introduce(c4, hostID).Nonce, member); err != nil {
		t.Fatalf("after the sessions ended: %v", err)
	}
	sessions, refused := as.srv.relayCounters.summary()
	if sessions != 3 || refused[refusedMode] != 1 || refused[refusedPerSource] != 1 {
		t.Fatalf("summary: %d sessions, refused %v", sessions, refused)
	}
}

func TestRelayDenyList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deny")
	write := func(s string) {
		t.Helper()
		// A distinct size or mtime marks the change.
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		os.Chtimes(path, fi.ModTime(), fi.ModTime().Add(time.Second))
	}
	write("# nothing yet\n")
	as, _ := policyServer(t, RelayConfig{DenyFile: path})
	ctx := tctx(t)
	hostKey := newID(t)
	hostID := as.relayHost(hostKey)
	c := as.client()
	if _, err := c.Relay(ctx, as.introduce(c, hostID).Nonce); err != nil {
		t.Fatal(err)
	}

	// Deny loopback: reloaded on change (the poll is every 10 s; force it
	// here as SIGHUP would).
	write("127.0.0.0/8  # the lab\n")
	if err := as.srv.relayDeny.reload(false); err != nil {
		t.Fatal(err)
	}
	c2 := as.client()
	if _, err := c2.Relay(ctx, as.introduce(c2, hostID).Nonce); CodeOf(err) != CodeNoRelay || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("denied address: %v", err)
	}
	// A broken file leaves the previous list in force.
	write("not an entry\n")
	if err := as.srv.ReloadRelayDeny(); err == nil {
		t.Fatal("broken deny list accepted")
	}
	c3 := as.client()
	if _, err := c3.Relay(ctx, as.introduce(c3, hostID).Nonce); CodeOf(err) != CodeNoRelay {
		t.Fatalf("after a broken reload: %v", err)
	}
	// Keys: the host's key denies its relays, and it cannot register.
	write(auth.EncodeKey(hostKey.Public()) + "\n::1\n")
	if err := as.srv.ReloadRelayDeny(); err != nil {
		t.Fatal(err)
	}
	c4 := as.client()
	if _, err := c4.Relay(ctx, as.introduce(c4, hostID).Nonce); CodeOf(err) != CodeNoRelay {
		t.Fatalf("denied host key: %v", err)
	}
	c5 := as.client()
	if _, err := c5.Register(ctx, hostKey); err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("denied key registered: %v", err)
	}
	// Removing the entries lifts the ban.
	write("\n")
	as.srv.ReloadRelayDeny()
	other := as.relayHost(newID(t))
	c6 := as.client()
	if _, err := c6.Relay(ctx, as.introduce(c6, other).Nonce); err != nil {
		t.Fatalf("after lifting: %v", err)
	}
}

func TestParseDenyList(t *testing.T) {
	id := newID(t)
	d, err := parseDenyList([]byte("203.0.113.7\n198.51.100.0/24 # a network\n2001:db8::/48\n\n# comment\n" + auth.EncodeKey(id.Public()) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{"203.0.113.7": true, "203.0.113.8": false, "198.51.100.200": true,
		"2001:db8::1": true, "2001:db9::1": false, "::ffff:203.0.113.7": true} {
		if got := d.addr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("addr(%s) = %v", addr, got)
		}
	}
	if !d.key(id.Public()) || d.key(newID(t).Public()) {
		t.Error("key matching")
	}
	if _, err := parseDenyList([]byte("10.0.0.1\nexample.com\n")); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("bad entry: %v", err)
	}
}

func TestRelayBudget(t *testing.T) {
	dir := t.TempDir()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	now := time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	u, err := openUsage(dir, 1000, clock, log)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: Config{Relay: &RelayConfig{Rate: 100_000, AccountRate: 400_000, PerSource: 1 << 30}}, relayUsage: u, log: log}
	anon, acct := relayPeer{source: netip.MustParseAddr("192.0.2.1")}, relayPeer{source: netip.MustParseAddr("192.0.2.2")}
	member := func(rate int, p relayPeer, account bool) (int, string) {
		dec, reason, _ := s.decideRelay(p, p, account)
		if dec.release != nil {
			dec.release()
		}
		return dec.rate, reason
	}
	check := func(used int64, wantAnon int, anonReason string, wantAcct int, acctReason string) {
		t.Helper()
		u.bytes.Store(used)
		if r, why := member(0, anon, false); r != wantAnon || why != anonReason {
			t.Errorf("at %d bytes, anonymous: rate %d %q", used, r, why)
		}
		if r, why := member(0, acct, true); r != wantAcct || why != acctReason {
			t.Errorf("at %d bytes, account: rate %d %q", used, r, why)
		}
	}
	check(0, 100_000, "", 400_000, "")
	check(799, 100_000, "", 400_000, "")
	check(800, 50_000, "", 400_000, "")
	check(1000, 0, refusedBudget, 400_000, "")
	check(1100, 0, refusedBudget, 0, refusedBudget)

	// Thresholds are logged once each, and the count persists.
	u.bytes.Store(0)
	u.logged = 0
	for _, b := range []int64{500, 600, 850, 1000, 1001} {
		u.bytes.Store(b)
		if err := u.flush(); err != nil {
			t.Fatal(err)
		}
	}
	out := logs.String()
	for _, want := range []string{"half of this month", "80% used", "used up"} {
		if strings.Count(out, want) != 1 {
			t.Errorf("log %q %d times:\n%s", want, strings.Count(out, want), out)
		}
	}
	u2, err := openUsage(dir, 1000, clock, log)
	if err != nil || u2.current() != 1001 {
		t.Fatalf("after a restart: %d, %v", u2.current(), err)
	}
	// A crash between flushes loses at most the last minute: the file is
	// always a complete record.
	if r, err := readUsage(dir); err != nil || r.Month != "2026-11" || r.Bytes != 1001 {
		t.Fatalf("usage file: %+v %v", r, err)
	}

	// The month rolls over: a fresh budget.
	now = time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	if got := u2.current(); got != 0 {
		t.Fatalf("new month starts at %d", got)
	}
	s.relayUsage = u2
	if r, why := member(0, anon, false); r != 100_000 || why != "" {
		t.Fatalf("anonymous in the new month: %d %q", r, why)
	}
	u2.add(42)
	u2.flush()
	if r, _ := readUsage(dir); r.Month != "2026-12" || r.Bytes != 42 {
		t.Fatalf("usage file after the rollover: %+v", r)
	}
	// A restart in the new month does not resurrect the old count.
	u3, _ := openUsage(dir, 1000, clock, log)
	if u3.current() != 42 {
		t.Fatalf("restart after rollover: %d", u3.current())
	}

	// --status reads the same file.
	var b strings.Builder
	if err := printRelayStatus(&b, dir, 1000, now); err != nil || !strings.Contains(b.String(), "relay usage 2026-12") || !strings.Contains(b.String(), "4.2% used") {
		t.Fatalf("status: %v\n%s", err, b.String())
	}
}

func TestRelaySummaryHasNoAddresses(t *testing.T) {
	logs := &lockedBuffer{}
	u, _ := openUsage(t.TempDir(), 0, time.Now, nil)
	s := &Server{log: slog.New(slog.NewTextHandler(logs, nil)), relayUsage: u}
	s.relayCounters.sessions.Add(3)
	s.relayCounters.refuse(refusedPerSource)
	s.relayCounters.refuse(refusedBudget)
	u.add(5000)
	s.logRelaySummary()
	out := logs.String()
	for _, want := range []string{"sessions=3", "bytes=5000", "refused_per_source=1", "refused_budget=1", "refused_deny=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %s: %s", want, out)
		}
	}
	if strings.Contains(out, "127.0.0.1") || strings.Contains(out, "192.0.2") {
		t.Error("summary names an address")
	}
}

func TestRelayPolicyFlags(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	ctx := context.Background()
	for _, args := range [][]string{
		{"--relay-monthly-gb", "10"},
		{"--relay", "--relay-for", "accounts"},
		{"--relay", "--relay-account-kbps", "8000"},
		{"--relay", "--relay-for", "some"},
		{"--relay", "--relay-per-source", "-1"},
	} {
		if code, _, _ := runSignalCmd(t, ctx, args...); code != cli.ExitUsage {
			t.Errorf("%q accepted", args)
		}
	}
	data := t.TempDir()
	deny := filepath.Join(t.TempDir(), "deny")
	os.WriteFile(deny, []byte("192.0.2.0/24\n"), 0o600)
	sctx, cancel := context.WithCancel(ctx)
	res := make(chan string, 1)
	go func() {
		_, out, errOut := runSignalCmd(t, sctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", data, "--relay",
			"--relay-kbps", "2000", "--relay-monthly-gb", "8000", "--relay-deny", deny)
		res <- out + errOut
	}()
	time.Sleep(500 * time.Millisecond)
	cancel()
	got := <-res
	for _, want := range []string{"2000 kbit/s each", "2 per address", "deny list " + deny, "0.00 GB relayed this month of 8000.00 GB", "relay deny list loaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("start output lacks %q:\n%s", want, got)
		}
	}
	if code, out, _ := runSignalCmd(t, ctx, "--status", "--data", data, "--relay-monthly-gb", "8000"); code != cli.ExitOK || !strings.Contains(out, "relay usage") || !strings.Contains(out, "0.0% used, normal") {
		t.Fatalf("--status: %d\n%s", code, out)
	}
	// A missing deny file is an error at start.
	if code, _, errOut := runSignalCmd(t, ctx, "--listen", "127.0.0.1:0", "--alt", "off", "--data", data, "--relay", "--relay-deny", deny+".missing"); code == cli.ExitOK || !strings.Contains(errOut, "relay-deny") {
		t.Fatalf("missing deny file: %d %s", code, errOut)
	}
}
