package signal

// Accounts: an optional service of the rendezvous server, off unless its
// operator enables it (Config.Accounts, `remohop signal --accounts`).
//
// An account groups a person's devices under an email address so that each
// device can list the others with their presence. It grants no access to
// any device: logging in to a host still takes a credential that host
// accepts. A host whose owner turns on `account-trust` accepts the account's
// devices like authorised keys, but it decides who they are from the
// account's signed membership log (internal/account/ledger), which this
// server stores and relays without being able to write: every entry is
// signed by a member device, and the first one fixes the account ID that
// each device's owner confirmed when it joined.
//
// Operations, all on the "account" request (Key is the device's identity
// key; Proof is its signature over accountAuthLabel and the connection's
// TLS exporter, so a token is useless away from the key it was issued to):
//
//	login   {email}                          mail a login code
//	verify  {email, code, key, proof, name, platform}
//	        ← {state, account_id, token, log}
//	          state "new": the account has no devices; the client appends
//	          a create entry. "pending": the account has devices; one of
//	          them must approve this one (add entry). "member": it is in.
//
// and with {key, token, proof} besides:
//
//	state    {name?, platform?}  ← {state, account_id, log?, pending?}
//	devices  {name?, platform?}  ← {state, account_id, log, devices, pending}
//	append   {entry}             ← {state, account_id, log}
//	reject   {device}            drop a join request
//	logout                       end this session
//	delete                       delete the account
//
// Everything except state and logout needs the device to be a member;
// append from a non-member is accepted only as the create entry of an
// account that has no members.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"net/smtp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
)

const (
	accountsFile = "accounts.journal"

	accountAuthLabel     = "remohop-account-auth/1"
	accountExporterLabel = "EXPORTER-remohop-account"

	// maxAccountMsg bounds an answer to an account request, which may
	// carry a whole log.
	maxAccountMsg = 256 << 10

	// CodeDigits is the length of a login code.
	CodeDigits = 8

	maxPlatform = 32
)

// Account states.
const (
	AccountNew     = "new"
	AccountPending = "pending"
	AccountMember  = "member"
)

// Mailer delivers login codes.
type Mailer interface {
	SendCode(ctx context.Context, to, code string) error
}

// AccountsConfig enables and bounds accounts. Zero fields take the defaults
// documented on each.
type AccountsConfig struct {
	// Mailer sends login codes. Required.
	Mailer Mailer
	// Domains and Emails are the addresses accounts may be made for: any
	// address in Domains, and the addresses in Emails. Open allows every
	// address. With none of the three no address is allowed.
	Domains []string
	Emails  []string
	Open    bool

	// MaxAccounts bounds the store. Default 100,000.
	MaxAccounts int
	// CodeTTL is how long a login code is valid. Default 10 min.
	CodeTTL time.Duration
	// MaxAttempts wrong codes burn the code and lock the address for
	// Lockout. Defaults 5 and 15 min.
	MaxAttempts int
	Lockout     time.Duration

	// Rate limits. Login-code requests: per source burst 5, one more a
	// minute; per address burst 3, one more every 5 min; in total (mails
	// sent) burst 100, one more every 3 s. Code checks per source: burst
	// 10, one more every 10 s. Other account requests per source: burst
	// 60, one more every 500 ms.
	StartBurst  int
	StartEvery  time.Duration
	EmailBurst  int
	EmailEvery  time.Duration
	MailBurst   int
	MailEvery   time.Duration
	VerifyBurst int
	VerifyEvery time.Duration
	OpBurst     int
	OpEvery     time.Duration

	// Now replaces the clock for code expiry and lockouts (tests).
	Now func() time.Time
}

func (a *AccountsConfig) defaults() {
	def := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	defDur := func(v *time.Duration, d time.Duration) {
		if *v == 0 {
			*v = d
		}
	}
	def(&a.MaxAccounts, 100_000)
	defDur(&a.CodeTTL, 10*time.Minute)
	def(&a.MaxAttempts, 5)
	defDur(&a.Lockout, 15*time.Minute)
	def(&a.StartBurst, 5)
	defDur(&a.StartEvery, time.Minute)
	def(&a.EmailBurst, 3)
	defDur(&a.EmailEvery, 5*time.Minute)
	def(&a.MailBurst, 100)
	defDur(&a.MailEvery, 3*time.Second)
	def(&a.VerifyBurst, 10)
	defDur(&a.VerifyEvery, 10*time.Second)
	def(&a.OpBurst, 60)
	defDur(&a.OpEvery, 500*time.Millisecond)
	if a.Now == nil {
		a.Now = time.Now
	}
}

// Allowed reports whether accounts may be made for a normalised address.
func (a *AccountsConfig) Allowed(email string) bool {
	if a.Open || slices.Contains(a.Emails, email) {
		return true
	}
	return slices.Contains(a.Domains, ledger.Domain(email))
}

// codeRec is a login code waiting to be used, kept as a MAC.
type codeRec struct {
	mac      []byte
	expires  time.Time
	attempts int
}

const maxCodes = 10_000

type accounts struct {
	cfg   AccountsConfig
	log   *slog.Logger
	store *accountStore
	key   [32]byte // MAC key for codes; never stored

	starts      *rateTable[netip.Addr]
	startsEmail *rateTable[string]
	mails       *rateTable[struct{}]
	verifies    *rateTable[netip.Addr]
	ops         *rateTable[netip.Addr]
	mailSlots   chan struct{}

	mu     sync.Mutex
	codes  map[string]*codeRec  // by hex email hash
	locked map[string]time.Time // address → end of lockout
}

func newAccounts(cfg AccountsConfig, dir string, log *slog.Logger) (*accounts, error) {
	cfg.defaults()
	if cfg.Mailer == nil {
		return nil, errors.New("accounts need a mailer")
	}
	cfg.Domains, cfg.Emails = slices.Clone(cfg.Domains), slices.Clone(cfg.Emails)
	for i, d := range cfg.Domains {
		cfg.Domains[i] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))
	}
	for i, e := range cfg.Emails {
		n, err := ledger.NormalizeEmail(e)
		if err != nil {
			return nil, fmt.Errorf("allowed address %q: %w", e, err)
		}
		cfg.Emails[i] = n
	}
	store, err := openAccountStore(dir, cfg.MaxAccounts)
	if err != nil {
		return nil, err
	}
	a := &accounts{
		cfg:         cfg,
		log:         log,
		store:       store,
		starts:      newRateTable[netip.Addr](cfg.StartBurst, cfg.StartEvery),
		startsEmail: newRateTable[string](cfg.EmailBurst, cfg.EmailEvery),
		mails:       newRateTable[struct{}](cfg.MailBurst, cfg.MailEvery),
		verifies:    newRateTable[netip.Addr](cfg.VerifyBurst, cfg.VerifyEvery),
		ops:         newRateTable[netip.Addr](cfg.OpBurst, cfg.OpEvery),
		mailSlots:   make(chan struct{}, 4),
		codes:       map[string]*codeRec{},
		locked:      map[string]time.Time{},
	}
	if _, err := rand.Read(a.key[:]); err != nil {
		store.close()
		return nil, err
	}
	return a, nil
}

func (a *accounts) codeMAC(emailHash []byte, code string) []byte {
	m := hmac.New(sha256.New, a.key[:])
	m.Write(emailHash)
	m.Write([]byte(code))
	return m.Sum(nil)
}

// NormalizeCode strips the spaces and dashes people type into a code.
func NormalizeCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' {
			return -1
		}
		return r
	}, s)
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}

// FormatCode shows a code the way it is mailed: "1234 5678".
func FormatCode(code string) string {
	if len(code) == CodeDigits {
		return code[:4] + " " + code[4:]
	}
	return code
}

// handleAccount serves one account request.
func (s *Server) handleAccount(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	a := s.accounts
	if a == nil {
		fail(CodeNoAccounts, "this rendezvous server does not offer accounts", 0)
		return
	}
	if ok, wait := a.ops.allow(c.source); !ok {
		fail(CodeRateLimited, "too many account requests", wait)
		return
	}
	reply := func(r *accountReply) { writeMsgN(st, response{OK: true, Account: r}, maxAccountMsg) }
	switch req.Op {
	case "login":
		if a.startLogin(c, req, fail) {
			writeMsg(st, response{OK: true})
		}
	case "verify":
		a.verify(s, c, req, fail, reply)
	case "state", "devices", "append", "reject", "logout", "delete":
		a.authenticated(s, c, req, fail, reply)
	default:
		fail(CodeBadRequest, "unknown account operation", 0)
	}
}

// startLogin mails a login code. The answer does not say whether an
// account exists for the address.
func (a *accounts) startLogin(c *serverConn, req *request, fail failFunc) bool {
	email, err := ledger.NormalizeEmail(req.Email)
	if err != nil {
		fail(CodeBadRequest, "malformed email address", 0)
		return false
	}
	if !a.cfg.Allowed(email) {
		fail(CodeNotAllowed, "this rendezvous server does not take accounts for that address", 0)
		return false
	}
	if ok, wait := a.starts.allow(c.source); !ok {
		fail(CodeRateLimited, "too many login requests from this address", wait)
		return false
	}
	eh := ledger.EmailHash(email)
	k := hex.EncodeToString(eh)
	now := a.cfg.Now()
	a.mu.Lock()
	if until, ok := a.locked[k]; ok && now.Before(until) {
		a.mu.Unlock()
		fail(CodeRateLimited, "too many wrong codes for this address", until.Sub(now))
		return false
	}
	a.mu.Unlock()
	if ok, wait := a.startsEmail.allow(k); !ok {
		fail(CodeRateLimited, "a code was sent to this address moments ago; use it, or wait", wait)
		return false
	}
	if ok, wait := a.mails.allow(struct{}{}); !ok {
		fail(CodeRateLimited, "the server is sending as many login mails as it allows itself to", wait)
		return false
	}
	code, err := newCode()
	if err != nil {
		fail(CodeBadRequest, "internal error", 0)
		return false
	}
	a.mu.Lock()
	if len(a.codes) >= maxCodes {
		for k, cr := range a.codes {
			if now.After(cr.expires) {
				delete(a.codes, k)
			}
		}
		for k, until := range a.locked {
			if now.After(until) {
				delete(a.locked, k)
			}
		}
	}
	if len(a.codes) >= maxCodes {
		a.mu.Unlock()
		fail(CodeRateLimited, "too many logins in progress", time.Minute)
		return false
	}
	// A new code replaces the previous one for the address.
	a.codes[k] = &codeRec{mac: a.codeMAC(eh, code), expires: now.Add(a.cfg.CodeTTL)}
	a.mu.Unlock()

	select {
	case a.mailSlots <- struct{}{}:
	default:
		fail(CodeRateLimited, "the server is busy sending mail", 5*time.Second)
		return false
	}
	go func() {
		defer func() { <-a.mailSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.cfg.Mailer.SendCode(ctx, email, code); err != nil {
			a.log.Error("could not send a login code", "err", err)
		}
	}()
	a.log.Info("login code requested", "from", c.addr)
	return true
}

// checkCode spends one attempt on the code for an address.
func (a *accounts) checkCode(emailHash []byte, code string) bool {
	k := hex.EncodeToString(emailHash)
	now := a.cfg.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	cr := a.codes[k]
	if cr == nil {
		return false
	}
	if now.After(cr.expires) {
		delete(a.codes, k)
		return false
	}
	// Compared as MACs, in constant time.
	if len(code) == CodeDigits && hmac.Equal(cr.mac, a.codeMAC(emailHash, code)) {
		delete(a.codes, k) // single use
		return true
	}
	if cr.attempts++; cr.attempts >= a.cfg.MaxAttempts {
		delete(a.codes, k)
		a.locked[k] = now.Add(a.cfg.Lockout)
	}
	return false
}

func (c *serverConn) exporter() ([]byte, error) {
	cs := c.q.ConnectionState().TLS
	return cs.ExportKeyingMaterial(accountExporterLabel, nil, 32)
}

func accountProofMessage(exporter []byte) []byte {
	return append([]byte(accountAuthLabel), exporter...)
}

// proven reports whether req carries a valid proof of possession of its
// key for this connection.
func (c *serverConn) proven(req *request) bool {
	if len(req.Key) != ed25519.PublicKeySize {
		return false
	}
	exp, err := c.exporter()
	return err == nil && ed25519.Verify(ed25519.PublicKey(req.Key), accountProofMessage(exp), req.Proof)
}

func cleanPlatform(s string) string { return auth.CleanText(s, maxPlatform) }

func (a *accounts) verify(s *Server, c *serverConn, req *request, fail failFunc, reply func(*accountReply)) {
	email, err := ledger.NormalizeEmail(req.Email)
	if err != nil {
		fail(CodeBadRequest, "malformed email address", 0)
		return
	}
	if !c.proven(req) {
		fail(CodeBadRequest, "proof of key possession failed", 0)
		return
	}
	if ok, wait := a.verifies.allow(c.source); !ok {
		fail(CodeRateLimited, "too many login attempts from this address", wait)
		return
	}
	eh := ledger.EmailHash(email)
	if !a.checkCode(eh, NormalizeCode(req.Code)) {
		fail(CodeBadCode, "wrong or expired login code", 0)
		return
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		fail(CodeBadRequest, "internal error", 0)
		return
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	r, err := a.store.login(eh, req.Key, token, auth.CleanText(req.Name, ledger.MaxName), cleanPlatform(req.Platform), c.addr.Addr().String())
	if errors.Is(err, errAccountsFull) {
		fail(CodeFull, "this server is not accepting new accounts", 0)
		return
	}
	if err != nil {
		a.log.Error("account store", "err", err)
		fail(CodeBadRequest, "internal error", 0)
		return
	}
	a.log.Info("account login", "from", c.addr, "identity", auth.Fingerprint(req.Key))
	out := a.describe(s, r, req.Key, false)
	out.Token = token
	reply(out)
}

// describe is the answer about r for the device key: its state, and the
// log and lists when it is a member. Callers hold a.store.mu.
func (a *accounts) describe(s *Server, r *acctRec, key []byte, devices bool) *accountReply {
	out := &accountReply{}
	switch {
	case r.state == nil:
		out.State = AccountNew
		return out
	case r.isMember(key):
		out.State = AccountMember
	default:
		out.State = AccountPending
	}
	out.AccountID = r.state.ID
	if out.State != AccountMember {
		return out
	}
	out.Log = r.Log
	now := a.store.now()
	r.prune(now)
	for _, p := range r.Pending {
		out.Pending = append(out.Pending, accountPending{Key: p.Key, Name: p.Name, Platform: p.Platform, From: p.From, At: p.At})
	}
	if !devices {
		return out
	}
	for _, m := range r.state.Members {
		d := accountDevice{Key: m.Key, Name: m.Name}
		if dr := r.device(m.Key); dr != nil {
			if dr.Name != "" {
				d.Name = dr.Name
			}
			d.Platform, d.LastSeen = dr.Platform, dr.Seen
		}
		if id, ok := s.store.lookup(m.Key); ok {
			d.ID = id
			if s.isOnline(id) {
				d.Online = true
				d.LastSeen = now.Unix()
			}
		}
		out.Devices = append(out.Devices, d)
	}
	return out
}

func (a *accounts) authenticated(s *Server, c *serverConn, req *request, fail failFunc, reply func(*accountReply)) {
	if !c.proven(req) {
		fail(CodeBadRequest, "proof of key possession failed", 0)
		return
	}
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	r, sess := a.store.session(req.Token)
	if r == nil || !bytes.Equal(sess.Key, req.Key) {
		fail(CodeNotLoggedIn, "", 0)
		return
	}
	now := a.store.now()
	if req.Name != "" || req.Platform != "" {
		if r.setDevice(req.Key, auth.CleanText(req.Name, ledger.MaxName), cleanPlatform(req.Platform), now) {
			if err := a.store.put(r); err != nil {
				a.log.Error("account store", "err", err)
			}
		}
	}
	a.store.seen(r, req.Key)
	member := r.isMember(req.Key)
	switch req.Op {
	case "state":
		reply(a.describe(s, r, req.Key, false))
	case "devices":
		if !member {
			fail(CodeForbidden, "", 0)
			return
		}
		reply(a.describe(s, r, req.Key, true))
	case "logout":
		h := tokenHash(req.Token)
		a.store.unindex(r)
		r.Sessions = slices.DeleteFunc(r.Sessions, func(ss sessRec) bool { return bytes.Equal(ss.Hash, h[:]) })
		r.Pending = slices.DeleteFunc(r.Pending, func(p pendRec) bool { return bytes.Equal(p.Key, req.Key) })
		a.store.index(r)
		if err := a.store.put(r); err != nil {
			fail(CodeBadRequest, "internal error", 0)
			return
		}
		reply(&accountReply{})
	case "append":
		e := req.Entry
		if e == nil {
			fail(CodeBadRequest, "no entry", 0)
			return
		}
		// A non-member may only found an account that has no members.
		if !member && !(e.Seq == 0 && r.state == nil && bytes.Equal(e.Signer, req.Key)) {
			fail(CodeForbidden, "", 0)
			return
		}
		err := a.store.appendEntry(r, *e)
		if errors.Is(err, errConflict) {
			fail(CodeConflict, "the account has changed; fetch it and try again", 0)
			return
		}
		if err != nil {
			fail(CodeBadRequest, auth.CleanText(err.Error(), maxErrorText), 0)
			return
		}
		a.log.Info("account changed", "op", e.Op, "device", auth.Fingerprint(e.Device), "by", auth.Fingerprint(e.Signer))
		reply(a.describe(s, r, req.Key, false))
	case "reject":
		if !member {
			fail(CodeForbidden, "", 0)
			return
		}
		if !r.isPending(req.Device) {
			fail(CodeBadRequest, "no such join request", 0)
			return
		}
		r.Pending = slices.DeleteFunc(r.Pending, func(p pendRec) bool { return bytes.Equal(p.Key, req.Device) })
		a.store.revokeKey(r, req.Device)
		if err := a.store.put(r); err != nil {
			fail(CodeBadRequest, "internal error", 0)
			return
		}
		reply(a.describe(s, r, req.Key, false))
	case "delete":
		if !member {
			fail(CodeForbidden, "", 0)
			return
		}
		if err := a.store.del(r); err != nil {
			fail(CodeBadRequest, "internal error", 0)
			return
		}
		a.log.Info("account deleted", "by", auth.Fingerprint(req.Key))
		reply(&accountReply{})
	}
}

// deviceSeen records that a host with key registered or left, for the
// last-seen time of the accounts it belongs to.
func (a *accounts) deviceSeen(key []byte) {
	a.store.mu.Lock()
	defer a.store.mu.Unlock()
	for _, r := range a.store.accountsOf(key) {
		a.store.seen(r, key)
	}
}

type accountReply struct {
	State     string           `json:"state,omitempty"`
	AccountID []byte           `json:"account_id,omitempty"`
	Token     []byte           `json:"token,omitempty"`
	Log       []ledger.Entry   `json:"log,omitempty"`
	Devices   []accountDevice  `json:"devices,omitempty"`
	Pending   []accountPending `json:"pending,omitempty"`
}

type accountDevice struct {
	Key      []byte `json:"key"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	Online   bool   `json:"online,omitempty"`
	LastSeen int64  `json:"last_seen,omitempty"`
}

type accountPending struct {
	Key      []byte `json:"key"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform,omitempty"`
	From     string `json:"from,omitempty"`
	At       int64  `json:"at"`
}

// LogMailer "sends" codes to a log. It is for development only: anyone who
// can read the log can log in to any account.
type LogMailer struct{ Log *slog.Logger }

// SendCode implements Mailer.
func (m LogMailer) SendCode(_ context.Context, to, code string) error {
	m.Log.Warn("DEVELOPMENT login code (not mailed)", "email", to, "code", FormatCode(code))
	return nil
}

// SMTPMailer sends codes through an SMTP server.
type SMTPMailer struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	// Security is "starttls" (the default: plain connection upgraded with
	// STARTTLS, which must succeed), "tls" (TLS from the start, usually
	// port 465) or "none" (only for a relay on the same machine).
	Security string
}

// SendCode implements Mailer.
func (m *SMTPMailer) SendCode(ctx context.Context, to, code string) error {
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	d := net.Dialer{Timeout: 15 * time.Second}
	tlsConf := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if m.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tlsConf}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if m.Security == "" || m.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsConf); err != nil {
			return err
		}
	}
	if m.User != "" {
		// PlainAuth refuses to send the password without TLS, except to
		// localhost.
		if err := c.Auth(smtp.PlainAuth("", m.User, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: " + m.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: RemoHop login code " + FormatCode(code) + "\r\n" +
		"Date: " + time.Now().Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		"Your RemoHop login code is\r\n\r\n    " + FormatCode(code) + "\r\n\r\n" +
		"It expires in a few minutes. If you did not ask for it, ignore this mail:\r\n" +
		"nobody can log in to your account without the code.\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
