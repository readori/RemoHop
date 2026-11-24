// Package hostd is the controlled side of RemoHop: the daemon behind
// `remohop host`. It owns the machine's identity, keeps it registered with the
// rendezvous server, answers hole-punch introductions, authenticates
// incoming P2P connections and hands each authenticated one to the session
// layer with peer.HostServices.
//
// It also owns two small services every host offers, with their client
// APIs: "pair" (mint a pairing token so the client can come back without
// the password) and "ping" (round-trip probe over the session).
package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/tailnet"
	"github.com/readori/RemoHop/internal/wsdgram"
)

const (
	// helloTimeout is how long a new connection has to send its login
	// hello. A client sends it the moment the connection is up, so two
	// seconds is slack for a slow link, not time to think.
	helloTimeout = 2 * time.Second
	// loginTimeout bounds the rest of the login once the hello is in:
	// queueing behind other logins from the same source, the reply and
	// the client's confirmation.
	loginTimeout = 5 * time.Second
	// preAuthStreamWindow and preAuthConnWindow are the flow-control
	// windows a connection starts with. Until a client has logged in,
	// nothing reads its streams except the login handshake, so this is
	// the most a stranger can make the host buffer per connection (and
	// the gate bounds how many such connections exist). After login the
	// windows grow to the session layer's maximums as data flows.
	preAuthStreamWindow = 256 << 10
	preAuthConnWindow   = 512 << 10
	// handshakeBurst and handshakeEvery are the budget of QUIC handshakes
	// accepted from addresses that have not proven they can receive at
	// that address. Beyond it, clients must complete a Retry round trip
	// first, which makes floods of spoofed packets cheap to shed.
	handshakeBurst = 64
	handshakeEvery = 50 * time.Millisecond
	// revokeCheck is how often live sessions are checked against the
	// token store, so `remohop host revoke` also ends sessions in progress.
	revokeCheck = 5 * time.Second
)

// Application error codes on P2P connections closed by the host.
const (
	closeNormal quic.ApplicationErrorCode = 0
	// closeAuth: the login was refused; the reason text is closeReason's.
	closeAuth quic.ApplicationErrorCode = 0x100
	// closeBusy: closed to make room before the client sent its hello.
	closeBusy quic.ApplicationErrorCode = 0x101
)

// Config configures a Host.
type Config struct {
	// Dir holds the identity key, pairing tokens and the status file.
	// Empty means the remohop config directory.
	Dir string
	// Signal is the rendezvous server ("host[:port]"). Empty runs without
	// one: the host is then reachable only by direct address.
	Signal        string
	SignalOptions signal.Options
	// Listen is the UDP address to bind, default ":0".
	Listen string
	// Password fixes the login password for this run. Empty means the
	// fixed password from the config file, if any, and otherwise only the
	// generated temporary one.
	Password string
	// AskTimeout is how long a login waits for the owner's approval when
	// the "ask" setting is on. Default DefaultAskTimeout.
	AskTimeout time.Duration
	// OnApproval is called (from other goroutines) for each login that
	// starts waiting for approval; answer with Host.Decide.
	OnApproval func(Approval)
	// Control makes Run serve the local control socket in Dir, which
	// `remohop host approve`, `remohop host otp` and `remohop status` talk to.
	Control bool
	// NoPairing refuses requests to mint pairing tokens.
	NoPairing bool
	// Services is what authenticated clients may open; nil means
	// peer.HostServices.
	Services *peer.Registry
	// Logger receives connection and rendezvous events; nil discards them.
	Logger *slog.Logger
	// Limiter tunes brute-force protection; the zero value is the default
	// policy.
	Limiter auth.LimiterConfig
	// Overrides tighten the config file's access settings for this run;
	// nil leaves them as they are.
	Overrides *Overrides

	// PacketConn, if set, is used instead of opening a socket on Listen,
	// and Locals replaces interface enumeration. Tests on a virtual
	// network use both.
	PacketConn net.PacketConn
	Locals     func() []netip.Addr
}

// Host is a running (or ready to run) host daemon.
type Host struct {
	cfg      Config
	log      *slog.Logger
	dir      string
	identity *auth.Identity
	tokens   *auth.TokenStore
	sock     *nat.Socket
	ln       *quic.Listener
	// The websocket bridge, when the rendezvous server has one and UDP
	// to it may be blocked: a second socket with its own listener, on
	// which relayed clients arrive while the host is registered through
	// the bridge. Nil otherwise.
	wc       *wsdgram.Conn
	wsSock   *nat.Socket
	wsLn     *quic.Listener
	auth     *auth.Server
	fixed    string // fixed password given for this run, normalised
	policies policyCache
	audit    *auditLog

	registered chan struct{} // closed on first successful registration
	started    time.Time

	mu           sync.Mutex
	temp         string // generated temporary password
	tempGen      uint64 // its generation: bumped whenever temp is replaced
	approvals    []*Approval
	nextApproval int
	relays       map[netip.AddrPort]time.Time // relay addresses bound for clients
	offers       map[string]time.Time         // nonces of recent introductions
	id           string
	online       bool
	viaWS        bool // registered through the websocket bridge
	natType      nat.Type
	sessions     map[*Session]struct{}
	gate         *gate
	onChange     func()
	record       func() // set by StartExclusive: rewrites the status file
	nextSession  int
	// Grants (see grants.go).
	grants        grantCache
	nextRecording int
}

// Session is one authenticated client connection, as seen by host-side
// service handlers.
type Session struct {
	// Auth says how the client authenticated and who it is.
	Auth *auth.Result
	// Remote is the client's address on the P2P path.
	Remote  netip.AddrPort
	Started time.Time

	host *Host
	conn *peer.Conn
	id   int // see Session.ID
	// tokenID is the pairing token the session depends on ("" for a
	// password login); guarded by host.mu.
	tokenID string
	// used records the features the session has opened streams for;
	// guarded by host.mu.
	used map[perm.Feature]bool
	// grant is the grant the session stands on ("" for full access);
	// approved remembers its owner's approvals of actions (grants.go).
	grant    string
	approved actionApprovals
	// Path is how the client reaches the host: "lan", "direct", "punched"
	// or "relayed".
	Path string
}

// fields returns the audit fields naming the session, followed by more.
func (s *Session) fields(more ...string) []string {
	f := []string{"from", s.Remote.String(), "identity", auth.Fingerprint(s.Auth.ClientKey),
		"name", s.Auth.TokenName, "auth", string(s.Auth.Kind)}
	if s.grant != "" {
		f = append(f, "grant", s.grant)
	}
	return append(f, more...)
}

func (s *Session) setTokenID(id string) {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	if s.tokenID != "" {
		s.tokenID = id
	}
}

func (s *Session) currentTokenID() string {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	return s.tokenID
}

type sessionKey struct{}

// SessionOf returns the host-side session behind c, or nil if c is not a
// connection accepted by a Host (for example a client-side connection).
func SessionOf(c *peer.Conn) *Session {
	v, _ := c.Value(sessionKey{})
	s, _ := v.(*Session)
	return s
}

// New prepares a host: loads or creates the identity, picks the password,
// binds the socket and starts listening. Call Run to serve.
func New(cfg Config) (*Host, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Services == nil {
		cfg.Services = peer.HostServices
	}
	// A stream ends when its feature is withdrawn (revoke.go).
	cfg.Services = cfg.Services.Wrapped(guardStream)
	dir := cfg.Dir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return nil, err
		}
	}
	h := &Host{
		cfg:        cfg,
		log:        cfg.Logger,
		dir:        dir,
		tokens:     auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)),
		registered: make(chan struct{}),
		started:    time.Now(),
		relays:     map[netip.AddrPort]time.Time{},
		offers:     map[string]time.Time{},
		natType:    nat.TypeUnknown,
		sessions:   map[*Session]struct{}{},
		gate:       newGate(),
	}
	var err error
	if h.identity, err = auth.LoadIdentity(dir); err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	h.audit = &auditLog{path: filepath.Join(dir, AuditFile)}
	if cfg.Password != "" {
		h.fixed = auth.NormalizePassword(cfg.Password)
		if err := auth.CheckPassword(h.fixed); err != nil {
			return nil, err
		}
	}
	if h.temp, err = auth.GeneratePassword(); err != nil {
		return nil, err
	}
	h.tempGen = 1
	h.auth = &auth.Server{
		PasswordGen: h.primaryPassword,
		AltPassword: func() string { _, alt := h.passwords(); return alt },
		Tokens:      h.tokens,
		Limiter:     auth.NewLimiter(cfg.Limiter),
		HostID:      h.ID,
	}
	h.auth.ScopedKey = h.scopedKey

	if cfg.PacketConn != nil {
		h.sock, err = nat.NewSocket(cfg.PacketConn)
	} else {
		h.sock, err = nat.Listen(cfg.Listen)
	}
	if err != nil {
		return nil, err
	}
	if cfg.Locals != nil {
		h.sock.Locals = cfg.Locals
	}
	tlsConf, err := h.identity.ServerTLS(peer.ALPN, true)
	if err != nil {
		h.sock.Close()
		return nil, err
	}
	quicConf := peer.QUICConfig()
	quicConf.InitialStreamReceiveWindow = preAuthStreamWindow
	quicConf.InitialConnectionReceiveWindow = preAuthConnWindow
	budget := newBudget(handshakeBurst, handshakeEvery)
	h.sock.Transport().VerifySourceAddress = func(net.Addr) bool { return !budget.take() }
	if h.ln, err = h.sock.Transport().Listen(tlsConf, quicConf); err != nil {
		h.sock.Close()
		return nil, err
	}
	if cfg.Signal != "" && cfg.SignalOptions.UseWS() {
		// Nothing is sent until the bridge is needed.
		if err := h.openBridge(tlsConf, quicConf, budget); err != nil {
			h.ln.Close()
			h.sock.Close()
			return nil, err
		}
	}
	return h, nil
}

func (h *Host) openBridge(tlsConf *tls.Config, quicConf *quic.Config, budget *budget) error {
	var err error
	if h.wc, err = h.cfg.SignalOptions.NewBridge(h.log); err != nil {
		return err
	}
	if h.wsSock, err = nat.NewSocket(h.wc); err != nil {
		h.wc.Close()
		return err
	}
	h.wsSock.Transport().VerifySourceAddress = func(net.Addr) bool { return !budget.take() }
	if h.wsLn, err = h.wsSock.Transport().Listen(tlsConf, quicConf); err != nil {
		h.wsSock.Close()
		return err
	}
	return nil
}

// ID is the host's 9-digit ID, or "" before the first registration.
func (h *Host) ID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.id
}

// Registered is closed once the host has registered with the rendezvous
// server for the first time.
func (h *Host) Registered() <-chan struct{} { return h.registered }

// fixedPassword is the fixed password in force: the one given for this run,
// else the one in the config file.
func (h *Host) fixedPassword() string {
	if h.fixed != "" {
		return h.fixed
	}
	return h.policy().fixed
}

// passwords returns the password(s) accepted right now. The temporary
// password is accepted when there is no fixed one, or when one-time
// passwords are on; a fixed password is accepted whenever it is set.
func (h *Host) passwords() (primary, alt string) {
	fixed := h.fixedPassword()
	if fixed != "" && !h.policy().otp {
		return fixed, ""
	}
	h.mu.Lock()
	temp := h.temp
	h.mu.Unlock()
	return temp, fixed
}

// primaryPassword is the auth.Server's PasswordGen hook: passwords' primary
// password, with the temporary password's generation if that is what it
// is, or 0 for the fixed password.
func (h *Host) primaryPassword() (string, uint64) {
	fixed := h.fixedPassword()
	if fixed != "" && !h.policy().otp {
		return fixed, 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.temp, h.tempGen
}

// Password is a password clients can log in with now, in canonical form:
// the temporary one if it is accepted, else the fixed one.
func (h *Host) Password() string {
	pw, _ := h.passwords()
	return pw
}

// PasswordMode names what Password is: "temporary" (one per run of the
// host), "one-time" (replaced after every login), "fixed", or
// "fixed+one-time".
func (h *Host) PasswordMode() string {
	fixed, otp := h.fixedPassword() != "", h.policy().otp
	switch {
	case fixed && otp:
		return "fixed+one-time"
	case fixed:
		return "fixed"
	case otp:
		return "one-time"
	}
	return "temporary"
}

// TempPassword returns the temporary password if one is accepted (formatted
// for display), and the password mode.
func (h *Host) TempPassword() (password, mode string) {
	mode = h.PasswordMode()
	if mode == "fixed" {
		return "", mode
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return auth.FormatPassword(h.temp), mode
}

// RotatePassword replaces the temporary password.
func (h *Host) RotatePassword() error {
	pw, err := auth.GeneratePassword()
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.temp = pw
	h.tempGen++
	h.mu.Unlock()
	h.notify()
	return nil
}

// PublicKey is the host's identity key.
func (h *Host) PublicKey() ed25519.PublicKey { return h.identity.Public() }

// Addr is the UDP address the host listens on.
func (h *Host) Addr() net.Addr { return h.sock.LocalAddr() }

// DirectAddrs lists the addresses at which the host can be dialled
// directly (`remohop ADDR:PORT ...`) by machines that can route to them.
func (h *Host) DirectAddrs() []netip.AddrPort { return h.sock.Candidates() }

// Tokens is the host's pairing-token store.
func (h *Host) Tokens() *auth.TokenStore { return h.tokens }

// Sessions returns the sessions currently open.
func (h *Host) Sessions() []*Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Session, 0, len(h.sessions))
	for s := range h.sessions {
		out = append(out, s)
	}
	return out
}

// Run serves until ctx is cancelled, then closes every session and the
// socket. A Host cannot be restarted.
func (h *Host) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	if h.cfg.Signal != "" {
		wg.Go(func() { h.signalLoop(ctx) })
	}
	wg.Go(func() { h.revocationLoop(ctx) })
	// The host's account (remohop login): report in, keep account-trusted keys current.
	wg.Go(func() { account.RunHost(ctx, h.dir, h.log) })
	h.startExtensions(ctx, &wg)
	if h.cfg.Control {
		wg.Go(func() { h.serveControl(ctx) })
	}

	var conns sync.WaitGroup
	if h.wsLn != nil {
		wg.Go(func() {
			for {
				q, err := h.wsLn.Accept(ctx)
				if err != nil {
					return
				}
				conns.Go(func() { h.serveConn(ctx, q) })
			}
		})
	}
	for {
		q, err := h.ln.Accept(ctx)
		if err != nil {
			break
		}
		conns.Go(func() { h.serveConn(ctx, q) })
	}

	// Shutdown. Sessions are told why; handlers see their contexts end.
	h.ln.Close()
	if h.wsLn != nil {
		h.wsLn.Close()
	}
	for _, s := range h.Sessions() {
		s.conn.CloseWithReason("host shutting down")
	}
	conns.Wait()
	wg.Wait()
	if h.wsSock != nil {
		h.wsSock.Close()
	}
	err := h.sock.Close()
	if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (h *Host) notify() {
	h.mu.Lock()
	fn, record := h.onChange, h.record
	h.mu.Unlock()
	if record != nil {
		record()
	}
	if fn != nil {
		fn()
	}
}

// signalLoop keeps the host registered for as long as it runs. Losing the
// rendezvous server never affects sessions already established: they are
// separate QUIC connections that merely share the socket.
func (h *Host) signalLoop(ctx context.Context) {
	const minBackoff, maxBackoff = time.Second, time.Minute
	backoff := minBackoff
	for ctx.Err() == nil {
		wasOnline, err := h.signalOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if wasOnline {
			backoff = minBackoff
		}
		wait := retryWait(backoff, err)
		h.log.Warn("not connected to the rendezvous server; retrying", "server", h.cfg.Signal, "in", wait.Round(100*time.Millisecond), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// retryWait is how long to wait before the next attempt to reach the
// rendezvous server: the current backoff, or longer if the server asked for
// that, plus jitter so hosts that lost the server together do not all come
// back in the same instant. What the server asks for is honoured only up
// to signal.MaxRetryAfter, whatever it says and whoever is speaking in its
// name: staying reachable must not depend on a rendezvous server's word.
func retryWait(backoff time.Duration, err error) time.Duration {
	wait := backoff
	if e, ok := errors.AsType[*signal.Error](err); ok && e.RetryAfter > wait {
		wait = min(e.RetryAfter, signal.MaxRetryAfter)
	}
	return wait + time.Duration(rand.Int64N(int64(wait)/4+1))
}

// dialSignal connects to the rendezvous server: over UDP from the host's
// socket and, if that is blocked (or the transport says so), through the
// websocket bridge. It returns the client and the socket it runs on.
func (h *Host) dialSignal(ctx context.Context) (*signal.Client, *nat.Socket, error) {
	const full = 15 * time.Second
	opts := h.cfg.SignalOptions
	var udpErr error
	if opts.UseUDP() {
		dctx, cancel := context.WithTimeout(ctx, opts.UDPWait(full))
		c, err := signal.Dial(dctx, h.sock, h.cfg.Signal, opts)
		cancel()
		if err == nil || h.wc == nil || ctx.Err() != nil {
			return c, h.sock, err
		}
		udpErr = err
		h.log.Debug("rendezvous server not reachable over UDP; trying its websocket bridge", "err", err)
	}
	if h.wc == nil {
		return nil, nil, errors.New("no websocket bridge configured")
	}
	dctx, cancel := context.WithTimeout(ctx, full)
	defer cancel()
	c, err := signal.DialBridge(dctx, h.wsSock, h.wc, opts)
	if err != nil && udpErr != nil {
		err = fmt.Errorf("over UDP: %v; %w", udpErr, err)
	}
	return c, h.wsSock, err
}

// signalOnce runs one rendezvous connection from dial to disconnect.
func (h *Host) signalOnce(ctx context.Context) (wasOnline bool, err error) {
	c, sock, err := h.dialSignal(ctx)
	if err != nil {
		return false, err
	}
	defer c.Close()
	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// The NAT is measured while connecting, and the result is in hand
	// before registering makes us reachable: every answer to an offer
	// carries it, and it must not be taken while a punch is in progress.
	// A measurement that got no answer does not erase an earlier one.
	natType := c.NATType()
	bridged := c.Bridged()
	c.OnRelay = func(nonce []byte, relay netip.AddrPort) error { return h.onRelay(sock, nonce, relay) }
	reg, err := c.Register(dctx, h.identity)
	if err != nil {
		return false, err
	}

	h.mu.Lock()
	first := h.id == ""
	changed := h.id != reg.ID
	h.id, h.online, h.viaWS = reg.ID, true, bridged
	if natType != nat.TypeUnknown {
		h.natType = natType
	}
	h.mu.Unlock()
	h.log.Debug("NAT type", "type", natType)
	if first {
		close(h.registered)
	}
	if bridged {
		h.log.Info("registered with the rendezvous server through its websocket bridge (UDP to it is blocked; clients will come through the relay)",
			"id", signal.FormatID(reg.ID), "bridge", h.wc.URL())
	} else {
		h.log.Info("registered with the rendezvous server", "id", signal.FormatID(reg.ID), "server", h.cfg.Signal, "public_addr", reg.Reflexive)
	}
	if changed && !first {
		h.log.Warn("the rendezvous server assigned a different ID", "id", signal.FormatID(reg.ID))
	}
	h.notify()
	defer func() {
		h.mu.Lock()
		h.online, h.viaWS = false, false
		h.mu.Unlock()
		h.notify()
	}()

	err = c.ServeOffers(ctx, func(o signal.Offer) signal.Answer { return h.onOffer(o, bridged) })
	if err == nil || ctx.Err() == nil {
		if cause := c.Err(); cause != nil {
			err = cause
		}
	}
	return true, err
}

// onRelay attaches the host to a relay the rendezvous server opened for a
// client whose punch failed. Only for an introduction this host answered
// moments ago, and only if its owner has not turned relaying off. Being
// attached grants the relay nothing: whoever arrives through it must log in
// like anybody else, over a TLS connection the relay cannot be party to.
func (h *Host) onRelay(sock *nat.Socket, nonce []byte, relay netip.AddrPort) error {
	h.mu.Lock()
	at, known := h.offers[string(nonce)]
	delete(h.offers, string(nonce))
	h.mu.Unlock()
	if !known || time.Since(at) > time.Minute {
		return errors.New("no such introduction")
	}
	if !signal.RelayAllowed(h.dir) {
		h.log.Info("refused a relayed connection (relay is off)", "relay", relay)
		return errors.New("relay is off")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sock.BindRelay(ctx, relay, nonce, nat.RoleHost); err != nil {
		return err
	}
	h.mu.Lock()
	for addr, t := range h.relays {
		if time.Since(t) > 24*time.Hour {
			delete(h.relays, addr)
		}
	}
	h.relays[relay] = time.Now()
	h.mu.Unlock()
	h.log.Info("no direct path to a client; accepting it through the rendezvous server's relay", "relay", relay)
	return nil
}

// onOffer reacts to an introduction: start punching towards the client and
// tell it where we are. bridged says we are registered through the
// websocket bridge; then, as when the client is, nothing can be punched
// and the client comes through the relay.
func (h *Host) onOffer(o signal.Offer, bridged bool) signal.Answer {
	// The session expires on its own; the client's QUIC connection, if
	// the punch works, simply shows up at the listener.
	punch := !bridged && !o.Peer.Bridged
	if punch {
		if _, err := h.sock.Punch(o.Nonce, nat.RoleHost, o.Peer.Candidates(), nat.DefaultPunchTime+5*time.Second); err != nil {
			h.log.Warn("declined a connection request", "from", o.Peer.Reflexive, "err", err)
			return signal.Answer{Decline: "host is busy"}
		}
	}
	h.mu.Lock()
	natType := h.natType
	for k, t := range h.offers {
		if time.Since(t) > time.Minute {
			delete(h.offers, k)
		}
	}
	h.offers[string(o.Nonce)] = time.Now()
	h.mu.Unlock()
	h.log.Debug("connection request", "from", o.Peer.Reflexive, "client_nat", o.Peer.NAT, "client_bridged", o.Peer.Bridged, "bridged", bridged)
	if !punch {
		return signal.Answer{NAT: natType}
	}
	return signal.Answer{Local: h.sock.Candidates(), NAT: natType}
}

func (h *Host) serveConn(ctx context.Context, q *quic.Conn) {
	remote := netip.AddrPort{}
	if ua, ok := q.RemoteAddr().(*net.UDPAddr); ok {
		ap := ua.AddrPort()
		remote = netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port())
	}
	src := remote.Addr()

	slot := h.gate.enter(src, func() { q.CloseWithError(closeBusy, "too many pending logins") })
	sess, err := h.authenticate(ctx, q, remote, slot)
	if err != nil {
		// Close first, then free the slot: whatever the connection holds
		// stays accounted for until it is really gone. The refusal is in
		// the close reason as well as in the reply on the stream, so the
		// client learns why even though we do not wait for the stream to
		// drain.
		if e, ok := errors.AsType[*auth.Error](err); ok {
			h.log.Warn("login rejected", "from", remote, "reason", e.Code)
			h.audit.add("login", "result", "refused", "reason", string(e.Code), "from", remote.String(), "identity", peerFingerprint(q))
			q.CloseWithError(closeAuth, closeReason(e))
		} else {
			// A connection that goes away before saying anything is
			// normal: the client raced several paths and kept another.
			h.log.Debug("connection ended before login", "from", remote, "err", err)
			q.CloseWithError(closeNormal, "")
		}
		h.gate.leave(slot)
		return
	}
	h.gate.leave(slot)

	// The session was set up and admitted (admit) before the client was
	// welcomed; only now does it serve the streams the client opens.
	res, conn, label := sess.Auth, sess.conn, sess.conn.RemoteID()
	h.log.Info("session opened", "client", label, "identity", auth.Fingerprint(res.ClientKey), "from", remote, "auth", res.Kind, "path", sess.Path)
	h.audit.add("login", sess.fields("result", "accepted", "path", sess.Path)...)
	if testHookSessionBuilt != nil {
		testHookSessionBuilt()
	}
	conn.Start()

	<-conn.Done()

	h.dropSession(sess)
	h.log.Info("session closed", "client", label, "from", remote, "duration", time.Since(sess.Started).Round(time.Second))
	h.audit.add("session-end", sess.fields("duration", time.Since(sess.Started).Round(time.Second).String())...)
	// Handlers undo what they set up once the session is gone (a USB
	// device goes back to its driver). Run waits for this goroutine, so
	// a host that is stopping does not exit under them.
	if !conn.WaitHandlers(handlerCleanup) {
		h.log.Warn("service handlers still running after the session closed", "client", label)
	}
}

// admit is the final step of a login, after the credential has checked out
// and the owner has approved, and before the client is welcomed. It sets the
// session up completely (identity, grant guard or the mark of full access)
// on a connection that serves nothing yet, and then, in one step under
// h.mu, makes the last checks and registers the session:
//
//   - a granted session's grant is still active and still the one its
//     credential was issued for, and has a session to spare;
//   - a pairing token still exists;
//   - the password a password login proved is still the one in force, and
//     a one-time password is spent here, so that of several logins racing
//     with it exactly one gets in.
//
// A login refused here never serves a stream and is not recorded as
// accepted. On success the caller starts the connection once the client has
// been welcomed, or drops the session if that fails.
func (h *Host) admit(q *quic.Conn, remote netip.AddrPort, res *auth.Result) (*Session, error) {
	sess := &Session{Auth: res, Remote: remote, Started: time.Now(), host: h, tokenID: res.TokenID, grant: res.Grant,
		used: map[perm.Feature]bool{}, Path: h.pathOf(remote, wsdgram.IsBridge(q.LocalAddr()))}
	label := "key " + auth.Fingerprint(res.ClientKey)
	if res.TokenName != "" {
		label = res.TokenName
	}
	conn := peer.New(peer.QUICMux(q), peer.Options{Role: peer.RoleHost, RemoteID: label, Services: h.cfg.Services, Authorize: sess.authorize})
	sess.conn = conn
	conn.SetValue(sessionKey{}, sess)
	if sess.grant != "" {
		grant.Attach(conn, sessionGuard{sess})
	} else {
		grant.AttachFull(conn)
	}

	denied := func(err error) (*Session, error) {
		h.log.Info("login refused at admission", "from", remote, "reason", err)
		if sess.grant != "" {
			h.audit.add("grant-refused", "from", remote.String(), "identity", auth.Fingerprint(res.ClientKey), "grant", sess.grant, "reason", err.Error())
		}
		return nil, &auth.Error{Code: auth.CodeDenied}
	}
	maxSessions := 0
	if sess.grant != "" {
		e, err := h.grantFor(sess.grant, res.TokenID)
		if err != nil {
			return denied(err)
		}
		maxSessions = e.g.MaxSessions
	}
	if res.Kind == auth.KindToken {
		if ok, err := h.tokens.Exists(res.TokenID); err != nil || !ok {
			return nil, &auth.Error{Code: auth.CodeUnknownToken}
		}
	}
	otp := h.policy().otp
	var next string
	if res.Kind == auth.KindPassword && res.PasswordGen != 0 && otp {
		var err error
		if next, err = auth.GeneratePassword(); err != nil {
			h.log.Error("could not replace the one-time password", "err", err)
			return nil, &auth.Error{Code: auth.CodeBusy}
		}
	}

	h.mu.Lock()
	if sess.grant != "" {
		if err := h.grantFullLocked(sess.grant, maxSessions); err != nil {
			h.mu.Unlock()
			return denied(err)
		}
	}
	if res.Kind == auth.KindPassword && res.PasswordGen != 0 {
		if res.PasswordGen != h.tempGen {
			// Replaced since the client proved it: spent by a login
			// that got here first, or changed by the owner.
			h.mu.Unlock()
			return nil, &auth.Error{Code: auth.CodeAuthFailed}
		}
		if next != "" {
			// One-time: spent by this login.
			h.temp = next
			h.tempGen++
		}
	}
	h.nextSession++
	sess.id = h.nextSession
	h.sessions[sess] = struct{}{}
	h.mu.Unlock()
	if next != "" {
		h.notify()
	}
	return sess, nil
}

// dropSession removes a session from the live ones.
func (h *Host) dropSession(s *Session) {
	h.mu.Lock()
	delete(h.sessions, s)
	h.mu.Unlock()
}

// testHookSessionBuilt, set only by tests, runs in serveConn once a
// session's connection exists and before the session is ready to serve.
var testHookSessionBuilt func()

// handlerCleanup bounds how long a closed session's service handlers may
// take to clean up before the host stops waiting for them.
const handlerCleanup = 15 * time.Second

func peerFingerprint(q *quic.Conn) string {
	if key, err := auth.PeerKey(q.ConnectionState().TLS); err == nil {
		return auth.Fingerprint(key)
	}
	return ""
}

// pathOf classifies how a client at remote reaches this host.
func (h *Host) pathOf(remote netip.AddrPort, bridged bool) string {
	if bridged {
		// Arrived on the bridge socket: only relays are reachable there.
		return "relayed (websocket)"
	}
	ip := remote.Addr()
	h.mu.Lock()
	natType := h.natType
	_, relayed := h.relays[remote]
	h.mu.Unlock()
	switch {
	case relayed:
		return "relayed"
	case tailnet.InRange(ip) && len(tailnet.LocalAddrs()) > 0:
		return "tailnet" // both on a tailnet; see internal/tailnet
	case ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast():
		return "lan"
	case natType == nat.TypeNone:
		return "direct"
	}
	return "punched"
}

// closeReason encodes a login refusal for the QUIC close frame:
// "code" or "code:retry-after-milliseconds".
func closeReason(e *auth.Error) string {
	if e.RetryAfter > 0 {
		return string(e.Code) + ":" + strconv.FormatInt(e.RetryAfter.Milliseconds(), 10)
	}
	return string(e.Code)
}

// LoginRefusal recovers the reason a host closed a connection during login
// from the error a client got on it, or returns nil if the error is
// something else. A host refuses on the login stream and in the close
// frame; whichever the client sees first tells it the same thing. Like
// everything a host says before it has authenticated, the reason is taken
// from a fixed vocabulary and its retry time is clamped.
func LoginRefusal(err error) *auth.Error {
	ae, ok := errors.AsType[*quic.ApplicationError](err)
	if !ok || !ae.Remote {
		return nil
	}
	switch ae.ErrorCode {
	case closeBusy:
		return &auth.Error{Code: auth.CodeBusy}
	case closeAuth:
	default:
		return nil
	}
	code, retry, _ := strings.Cut(ae.ErrorMessage, ":")
	e := &auth.Error{}
	switch c := auth.Code(code); c {
	case auth.CodeAuthFailed, auth.CodeUnknownToken, auth.CodeUnknownKey, auth.CodeDenied, auth.CodeNoPassword, auth.CodeWrongHost, auth.CodeBusy, auth.CodeBadVersion:
		e.Code = c
	case auth.CodeLockedOut:
		e.Code = c
		ms, _ := strconv.ParseInt(retry, 10, 64)
		e.RetryAfter = time.Duration(min(max(ms, 0), auth.MaxRetryAfter.Milliseconds())) * time.Millisecond
	default:
		e.Code = auth.CodeProtocol
	}
	return e
}

// authenticate runs the login handshake on the connection's first stream
// and returns the session it admitted (see admit), set up but not started.
func (h *Host) authenticate(ctx context.Context, q *quic.Conn, remote netip.AddrPort, slot *pending) (*Session, error) {
	src := remote.Addr()
	hctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	st, err := q.AcceptStream(hctx)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(helloTimeout))
	hello, err := h.auth.ReadHello(st)
	if err != nil {
		return nil, err
	}
	if !h.gate.promote(slot, hello.Kind()) {
		busy := &auth.Error{Code: auth.CodeBusy}
		auth.Reject(st, busy)
		return nil, busy
	}

	binding, err := auth.HostBinding(q.ConnectionState().TLS, h.identity.Public())
	if err != nil {
		return nil, err
	}
	lctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()
	st.SetDeadline(time.Now().Add(loginTimeout))
	var sess *Session
	approve := func(res *auth.Result, announce func() error) error {
		if res.Grant != "" {
			if err := h.grantLogin(res); err != nil {
				h.log.Info("grant login refused", "from", remote, "grant", res.Grant, "reason", err)
				h.audit.add("grant-refused", "from", remote.String(), "identity", auth.Fingerprint(res.ClientKey), "grant", res.Grant, "reason", err.Error())
				return err
			}
		}
		if h.policy().needsApproval(res) {
			// The client has proven its credential: it is no stranger any
			// more, and its wait is bounded by the approval timeout and
			// maxApprovals rather than by the login gate.
			h.gate.leave(slot)
			st.SetDeadline(time.Now().Add(h.cfg.AskTimeout + DefaultAskTimeout + loginTimeout))
			if err := announce(); err != nil {
				return err
			}
			if err := h.ask(ctx, q.Context().Done(), res, remote); err != nil {
				return err
			}
		}
		var err error
		sess, err = h.admit(q, remote, res)
		return err
	}
	res, err := h.auth.Complete(lctx, st, hello, binding, src, approve)
	if err != nil {
		if sess != nil {
			// Admitted, but the client never heard: it is not in.
			h.dropSession(sess)
		}
		return nil, err
	}
	if sess == nil || sess.Auth != res {
		return nil, errors.New("login completed without admission")
	}
	return sess, nil
}

// revocationLoop ends sessions whose pairing token has been revoked.
func (h *Host) revocationLoop(ctx context.Context) {
	t := time.NewTicker(revokeCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, s := range h.Sessions() {
			id := s.currentTokenID()
			if id == "" {
				continue
			}
			if ok, err := h.tokens.Exists(id); err == nil && !ok {
				h.log.Info("closing session of a revoked pairing", "client", s.Auth.TokenName, "from", s.Remote)
				s.conn.CloseWithReason("pairing revoked")
			}
		}
		// Grants expire, and are revoked or suspended from other
		// processes; their sessions end within one tick.
		h.enforceGrants()
	}
}

// budget is a token bucket.
type budget struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	every  time.Duration
	at     time.Time
}

func newBudget(burst int, every time.Duration) *budget {
	return &budget{tokens: float64(burst), burst: float64(burst), every: every, at: time.Now()}
}

func (b *budget) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens = min(b.burst, b.tokens+float64(now.Sub(b.at))/float64(b.every))
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
