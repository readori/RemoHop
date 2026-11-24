package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAccountsE2E runs the whole account life cycle against a rendezvous
// server that mails its login codes through a real SMTP server over
// STARTTLS with authentication:
//
//	REMOHOP_ACCOUNTS_E2E=1 go test -count=1 -v -run TestAccountsE2E ./test/e2e
//
// With REMOHOP_E2E_MAILPIT (the HTTP API base of a Mailpit, e.g.
// http://127.0.0.1:8025) the codes go through that server, configured by
// REMOHOP_E2E_SMTP_HOST, _PORT, _USER, _PASSWORD and _CA (the PEM file of the
// CA that signed its certificate; the server trusts it through
// SSL_CERT_FILE, as an operator of a relay with a private CA would).
// Without it a small in-process SMTP server with the same requirements
// stands in.
//
// Device A creates the account and hosts with account-trust on; device B
// logs in, is approved by A after the fingerprint comparison, connects to A
// without a password, is removed and loses access. Device C, never in the
// account, is refused throughout.
func TestAccountsE2E(t *testing.T) {
	gate(t, "REMOHOP_ACCOUNTS_E2E")
	const email = "test@example.com"

	src, smtpArgs, smtpEnv := mailSetup(t)

	// The rendezvous server with accounts on.
	sigDev := newDevice(t, "signal")
	port := freePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	sigDev.env = smtpEnv
	sig := sigDev.start(append([]string{"signal", "--listen", addr, "--data", sigDev.dir, "--accounts",
		"--mail-from", "remohop@example.com", "--account-domains", "example.com"}, smtpArgs...)...)
	key := sig.waitFor(t, regexp.MustCompile(`server key: (\S+)`), 15*time.Second)[1]
	sig.waitFor(t, regexp.MustCompile(`accounts: on`), 5*time.Second)
	t.Logf("rendezvous server on %s with accounts and SMTP (%s)", addr, src.describe())

	newDev := func(name string) *device {
		d := newDevice(t, name)
		d.must("config", "set", "signal", addr)
		d.must("config", "set", "signal-pin", key)
		d.must("config", "set", "name", name)
		return d
	}
	a, b, c := newDev("device-a"), newDev("device-b"), newDev("device-c")

	// An address outside --account-domains gets no mail.
	if r := a.run("login", "someone@not-allowed.example", "--send-only"); r.code == 0 || !strings.Contains(r.stderr, "does not take accounts") {
		t.Errorf("login for a domain the server does not serve: %s", r)
	}

	// --- A creates the account.
	seen := src.count(t, email)
	a.must("login", email, "--send-only")
	codeA := src.waitCode(t, email, seen)
	t.Log("A: the login code arrived by mail")
	var resA loginResult
	jsonOut(t, a.must("login", email, "--code", codeA, "--json"), &resA)
	if resA.Status != "created" || resA.Account == "" || resA.Device == "" {
		t.Fatalf("A login: %+v", resA)
	}
	t.Logf("A: created the account, fingerprint %s, device %s", resA.Account, resA.Device)
	if r := a.run("login", email, "--code", codeA, "--json"); r.code == 0 {
		t.Errorf("a login code worked twice: %s", r)
	}
	sumA := account(t, a)
	if sumA.Status != "member" || sumA.Account != resA.Account || len(sumA.Devices) != 1 || !sumA.Devices[0].This {
		t.Fatalf("A account: %+v", sumA)
	}

	// --- A hosts, accepting its account's devices.
	a.must("config", "account-trust", "on")
	hostPW := randHex(12)
	host := a.start("host", "--password", hostPW)
	hostID := digits(host.waitFor(t, hostIDRE, 20*time.Second)[1])
	t.Logf("A: hosting as %s with account-trust on", hostID)

	// --- B joins: first with a wrong account fingerprint (refused), then right.
	seen = src.count(t, email)
	b.must("login", email, "--send-only")
	codeB := src.waitCode(t, email, seen)
	r := b.run("login", email, "--code", codeB, "--account-fingerprint", "0000:0000:0000:0000:0000", "--json")
	if r.code == 0 || !strings.Contains(r.stderr, "not joining") {
		t.Fatalf("B joined with a wrong account fingerprint: %s", r)
	}
	t.Log("B: refused to join an account whose fingerprint does not match")
	seen = src.count(t, email)
	b.must("login", email, "--send-only")
	codeB = src.waitCode(t, email, seen)
	if r := b.run("login", email, "--code", "00000000", "--account-fingerprint", resA.Account, "--json"); r.code == 0 {
		t.Fatalf("a wrong code was accepted: %s", r)
	}
	var resB loginResult
	jsonOut(t, b.must("login", email, "--code", codeB, "--account-fingerprint", resA.Account, "--no-wait", "--json"), &resB)
	if resB.Status != "pending" || resB.Account != resA.Account || resB.Device == "" || resB.Device == resA.Device {
		t.Fatalf("B login: %+v", resB)
	}
	t.Logf("B: waiting for approval, identity %s", resB.Device)
	if s := account(t, b); s.Status != "pending" {
		t.Errorf("B before approval: %+v", s)
	}
	// Not in the account yet: no access to A.
	if r := b.run(hostID, "shell", "-c", "echo should-not-run"); r.code == 0 {
		t.Fatalf("B reached A before being approved: %s", r)
	}

	// --- A compares and approves.
	sumA = account(t, a)
	if len(sumA.Pending) != 1 || sumA.Pending[0].Identity != resB.Device {
		t.Fatalf("A does not see exactly B's identity waiting: %+v", sumA.Pending)
	}
	if r := a.run("account", "approve", resB.Device[:9]); r.code == 0 {
		t.Errorf("approve accepted a fingerprint prefix: %s", r)
	}
	a.must("account", "approve", resB.Device)
	t.Log("A: approved B after comparing its identity fingerprint")
	if s := account(t, b); s.Status != "member" || s.Account != resA.Account {
		t.Fatalf("B after approval: %+v", s)
	}

	// --- Both list each other.
	sumA = account(t, a)
	if !hasDevice(sumA.Devices, resB.Device) || len(sumA.Devices) != 2 {
		t.Errorf("A's devices: %+v", sumA.Devices)
	}
	devB := devices(t, b)
	ia := slices.IndexFunc(devB, func(d deviceRow) bool { return d.Account && d.Target == hostID })
	if ia < 0 || devB[ia].Presence != "online" || devB[ia].Name != "device-a" || devB[ia].Login != "account" {
		t.Fatalf("B's remohop devices does not show A as an online account host: %+v", devB)
	}
	devA := devices(t, a)
	if !slices.ContainsFunc(devA, func(d deviceRow) bool { return d.Account && d.Name == "device-b" }) {
		t.Errorf("A's remohop devices lacks B: %+v", devA)
	}
	t.Log("remohop account / remohop devices: A and B list each other, A online as a host")

	// --- account-trust: B connects to A with no password at all.
	sync := a.must("account", "sync")
	t.Logf("A: %s", strings.TrimSpace(sync.stdout))
	if p := a.must("host", "paired", "--json"); !strings.Contains(p.stdout, "account:") {
		t.Errorf("A's authorised keys lack the account entry: %s", p.stdout)
	}
	var out result
	eventually(t, "B to log in to A through account-trust", 30*time.Second, func() bool {
		out = b.run(hostID, "shell", "-c", "echo trusted-by-account")
		return out.code == 0
	})
	if strings.TrimSpace(out.stdout) != "trusted-by-account" {
		t.Fatalf("B's shell on A: %s", out)
	}
	t.Log("B: ran a command on A without a password (account-trust)")
	if r := c.run(hostID, "shell", "-c", "echo should-not-run"); r.code == 0 {
		t.Fatalf("device C (not in the account, no password) got in: %s", r)
	}

	// --- A removes B; B loses access.
	a.must("account", "remove", resB.Device)
	t.Logf("A: %s", strings.TrimSpace(a.must("account", "sync").stdout))
	sumA = account(t, a)
	if hasDevice(sumA.Devices, resB.Device) {
		t.Errorf("B still in A's device list: %+v", sumA.Devices)
	}
	if p := a.must("host", "paired", "--json"); strings.Contains(p.stdout, "account:") {
		t.Errorf("A still authorises an account key: %s", p.stdout)
	}
	eventually(t, "B to be refused by A", 30*time.Second, func() bool {
		return b.run(hostID, "shell", "-c", "echo should-not-run").code != 0
	})
	t.Log("B: refused by A after the removal")
	if s := account(t, b); s.Status == "member" {
		t.Errorf("B still a member after removal: %+v", s)
	} else {
		t.Logf("B: account status now %q", s.Status)
	}

	// A's own session is unaffected; the host keeps running.
	select {
	case <-host.done:
		t.Fatalf("A's host exited:\n%s", clip(host.output()))
	default:
	}
	if strings.Contains(sig.output(), "could not send a login code") {
		t.Errorf("the server failed to send a mail:\n%s", clip(sig.output()))
	}
	src.check(t)
}

// TestAccountsSMTPVerifiesTLS: a server whose SMTP certificate the
// rendezvous server does not trust gets no mail; the code is not sent in
// the clear either.
func TestAccountsSMTPVerifiesTLS(t *testing.T) {
	gate(t, "REMOHOP_ACCOUNTS_E2E")
	src, smtpArgs, _ := mailSetup(t)
	sigDev := newDevice(t, "signal-untrusting")
	port := freePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	// No SSL_CERT_FILE: only the system roots, which do not know the test CA.
	sig := sigDev.start(append([]string{"signal", "--listen", addr, "--data", sigDev.dir, "--accounts",
		"--mail-from", "remohop@example.com", "--account-domains", "example.com"}, smtpArgs...)...)
	key := sig.waitFor(t, regexp.MustCompile(`server key: (\S+)`), 15*time.Second)[1]
	d := newDevice(t, "device-x")
	d.must("config", "set", "signal", addr)
	d.must("config", "set", "signal-pin", key)
	const email = "tls-check@example.com"
	seen := src.count(t, email)
	d.must("login", email, "--send-only")
	sig.waitFor(t, regexp.MustCompile(`could not send a login code.*(certificate|x509)`), 30*time.Second)
	time.Sleep(2 * time.Second)
	if n := src.count(t, email); n != seen {
		t.Fatalf("a mail was delivered although the SMTP certificate is not trusted")
	}
	t.Log("an untrusted SMTP certificate stops the mail (no silent downgrade)")
}

type loginResult struct {
	Status  string `json:"status"`
	Email   string `json:"email"`
	Account string `json:"account"`
	Device  string `json:"device"`
}

type accountDevice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Online   bool   `json:"online"`
	This     bool   `json:"this_device"`
}

type accountSummary struct {
	Email   string          `json:"email"`
	Account string          `json:"account"`
	Status  string          `json:"status"`
	This    string          `json:"this_device"`
	Devices []accountDevice `json:"devices"`
	Pending []struct {
		Identity string `json:"identity"`
		Name     string `json:"name"`
	} `json:"pending"`
	Error string `json:"error"`
}

type deviceRow struct {
	Target   string `json:"target"`
	Account  bool   `json:"account"`
	Name     string `json:"name"`
	Login    string `json:"login"`
	Presence string `json:"presence"`
}

func jsonOut(t *testing.T, r result, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.stdout), v); err != nil {
		t.Fatalf("bad JSON (%v): %s", err, r)
	}
}

func account(t *testing.T, d *device) accountSummary {
	t.Helper()
	var s accountSummary
	jsonOut(t, d.run("account", "--json"), &s)
	return s
}

func devices(t *testing.T, d *device) []deviceRow {
	t.Helper()
	var rows []deviceRow
	jsonOut(t, d.must("devices", "--json"), &rows)
	return rows
}

func hasDevice(ds []accountDevice, identity string) bool {
	return slices.ContainsFunc(ds, func(d accountDevice) bool { return d.Identity == identity })
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- where login codes come from ----

var codeRE = regexp.MustCompile(`RemoHop login code (\d{4}) ?(\d{4})`)

type mailSource interface {
	describe() string
	// codes are the login codes mailed to addr so far, oldest first.
	codes(t *testing.T, addr string) []string
	// check asserts transport properties once the test is done.
	check(t *testing.T)
}

// mailSourceT adds the waiting helpers to a mailSource.
type mailSourceT struct{ mailSource }

func (m mailSourceT) count(t *testing.T, addr string) int { return len(m.codes(t, addr)) }

// waitCode waits for a code mailed to addr after the first `seen` ones.
func (m mailSourceT) waitCode(t *testing.T, addr string, seen int) string {
	t.Helper()
	var code string
	eventually(t, "a login mail to "+addr, 60*time.Second, func() bool {
		cs := m.codes(t, addr)
		if len(cs) > seen {
			code = cs[len(cs)-1]
			return true
		}
		return false
	})
	return code
}

// mailSetup picks the SMTP server: Mailpit when configured, else the
// in-process stand-in. It returns the remohop signal flags and the environment
// that makes the server trust the SMTP certificate.
func mailSetup(t *testing.T) (mailSourceT, []string, []string) {
	t.Helper()
	passFile := filepath.Join(t.TempDir(), "smtp-pass")
	if base := os.Getenv("REMOHOP_E2E_MAILPIT"); base != "" {
		host, port := os.Getenv("REMOHOP_E2E_SMTP_HOST"), os.Getenv("REMOHOP_E2E_SMTP_PORT")
		user, pass, ca := os.Getenv("REMOHOP_E2E_SMTP_USER"), os.Getenv("REMOHOP_E2E_SMTP_PASSWORD"), os.Getenv("REMOHOP_E2E_SMTP_CA")
		if host == "" || port == "" || ca == "" {
			t.Fatal("REMOHOP_E2E_MAILPIT needs REMOHOP_E2E_SMTP_HOST, REMOHOP_E2E_SMTP_PORT and REMOHOP_E2E_SMTP_CA")
		}
		args := []string{"--smtp-host", host, "--smtp-port", port, "--smtp-tls", "starttls"}
		if user != "" {
			if err := os.WriteFile(passFile, []byte(pass+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--smtp-user", user, "--smtp-password-file", passFile)
		}
		return mailSourceT{&mailpit{base: strings.TrimRight(base, "/")}}, args, []string{"SSL_CERT_FILE=" + ca}
	}
	ca := newTestCA(t)
	user, pass := "remohop", randHex(12)
	srv := startSMTP(t, ca.issue(t, "127.0.0.1", "localhost"), user, pass)
	if err := os.WriteFile(passFile, []byte(pass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--smtp-host", "127.0.0.1", "--smtp-port", strconv.Itoa(srv.port()), "--smtp-tls", "starttls",
		"--smtp-user", user, "--smtp-password-file", passFile}
	return mailSourceT{&localMail{srv}}, args, []string{"SSL_CERT_FILE=" + ca.writePEM(t)}
}

type localMail struct{ s *smtpServer }

func (l *localMail) describe() string { return "in-process STARTTLS stand-in" }

func (l *localMail) codes(t *testing.T, addr string) []string {
	var out []string
	for _, m := range l.s.all() {
		if m.To == addr {
			if c := codeRE.FindStringSubmatch(m.Data); c != nil {
				out = append(out, c[1]+c[2])
			}
		}
	}
	return out
}

func (l *localMail) check(t *testing.T) {
	ms := l.s.all()
	if len(ms) == 0 {
		t.Error("no mail at all")
	}
	for _, m := range ms {
		if !m.TLS || !m.Authed {
			t.Errorf("mail to %s: tls=%v authenticated=%v", m.To, m.TLS, m.Authed)
		}
		if !strings.Contains(m.Data, "From: remohop@example.com") {
			t.Errorf("mail without the configured sender")
		}
	}
}

// mailpit reads messages through Mailpit's HTTP API.
type mailpit struct{ base string }

func (m *mailpit) describe() string { return "Mailpit" }

func (m *mailpit) codes(t *testing.T, addr string) []string {
	t.Helper()
	resp, err := http.Get(m.base + "/api/v1/messages?limit=500")
	if err != nil {
		t.Fatalf("mailpit API: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != 200 {
		t.Fatalf("mailpit API: %s", resp.Status)
	}
	var list struct {
		Messages []struct {
			From    struct{ Address string }
			To      []struct{ Address string }
			Subject string
			Created time.Time
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("mailpit API: %v", err)
	}
	var out []string
	for i := len(list.Messages) - 1; i >= 0; i-- { // the API lists newest first
		msg := list.Messages[i]
		if !slices.ContainsFunc(msg.To, func(a struct{ Address string }) bool { return strings.EqualFold(a.Address, addr) }) {
			continue
		}
		if c := codeRE.FindStringSubmatch(msg.Subject); c != nil {
			out = append(out, c[1]+c[2])
		}
		if msg.From.Address != "remohop@example.com" {
			t.Errorf("mail from %q", msg.From.Address)
		}
	}
	return out
}

func (m *mailpit) check(t *testing.T) {}
