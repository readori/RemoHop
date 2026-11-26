package signal

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
)

const (
	keyFile = "signal.key"
	idsFile = "ids.journal"

	// requestTimeout bounds one request stream; offerTimeout bounds the
	// wait for a host to answer an offer inside a connect request.
	requestTimeout = 10 * time.Second
	offerTimeout   = 5 * time.Second
)

// Application error codes on the QUIC connection.
const (
	closeNormal   quic.ApplicationErrorCode = 0
	closeBusy     quic.ApplicationErrorCode = 1
	closeReplaced quic.ApplicationErrorCode = 2
	closeIdle     quic.ApplicationErrorCode = 3
	// closeMoved: a registered host's packets now come from another
	// address (its NAT rebound, its router got a new public address, the
	// machine changed networks). The connection followed it, but the NAT
	// type the host measured may no longer hold: it registers again and
	// measures afresh.
	closeMoved quic.ApplicationErrorCode = 4
)

// moveCheckEvery is how often registered hosts are checked for a changed
// address (see closeMoved). A variable for tests.
var moveCheckEvery = 5 * time.Second

// Config configures a Server.
type Config struct {
	// DataDir holds the server's TLS key and the ID journal.
	DataDir string
	// Logger receives operational events; nil discards them.
	Logger *slog.Logger
	Limits Limits
	// Relay, if not nil, makes the server offer to relay sessions whose
	// peers could not connect directly. Off by default: it is the one
	// thing here that costs an operator real bandwidth.
	Relay *RelayConfig
	// Accounts, if not nil, offers accounts (see accounts.go). Off by
	// default.
	Accounts *AccountsConfig
}

// RelayConfig bounds the relay service. Zero fields take the defaults
// documented on each.
type RelayConfig struct {
	// Listen opens the UDP socket for one relayed session. The default
	// binds an ephemeral port on all interfaces; the ports must be
	// reachable from the peers.
	Listen func() (net.PacketConn, error)
	// MaxSessions bounds concurrent relayed sessions. Default 64.
	MaxSessions int
	// Rate is each session's budget in bytes per second, both directions
	// together. Default 1,000,000 (8 Mbit/s).
	Rate int
	// TotalRate is the budget of all sessions together. Default
	// 5,000,000 (40 Mbit/s).
	TotalRate int
	// Idle ends a session that passes no traffic. Default 30 s.
	Idle time.Duration

	// Access control and budget; see relaypolicy.go.
	//
	// PerSource bounds concurrent relayed sessions per source address
	// (IPv4 address or IPv6 /64), counting both peers. 0: unlimited.
	PerSource int
	// AccountRate is the per-session rate when either peer is a verified
	// member of an account on this server. Default: Rate.
	AccountRate int
	// ForAccounts relays only sessions with such a peer.
	ForAccounts bool
	// DenyFile names a deny list (addresses, networks, identity keys),
	// re-read when it changes; empty for none.
	DenyFile string
	// MonthlyBytes is the relay budget per calendar month (UTC); 0 is
	// unlimited. From 80% anonymous sessions get half their rate, from
	// 100% they are refused, from 110% all are.
	MonthlyBytes int64
	// Now is the clock for the budget; default time.Now.
	Now func() time.Time
}

func (r *RelayConfig) defaults() {
	if r.Listen == nil {
		r.Listen = func() (net.PacketConn, error) { return net.ListenPacket("udp", ":0") }
	}
	if r.MaxSessions == 0 {
		r.MaxSessions = 64
	}
	if r.Rate == 0 {
		r.Rate = 1_000_000
	}
	if r.TotalRate == 0 {
		r.TotalRate = 5_000_000
	}
	if r.Idle == 0 {
		r.Idle = 30 * time.Second
	}
	if r.AccountRate == 0 {
		r.AccountRate = r.Rate
	}
	if r.PerSource == 0 {
		r.PerSource = int(^uint(0) >> 1)
	}
	if r.Now == nil {
		r.Now = time.Now
	}
}

// intro remembers a recent introduction, so that a relay is only ever set
// up between the two peers the server itself introduced. It names the
// peers rather than holding their connections: a closed connection must not
// stay reachable (some 30 KiB each) for as long as the entry lives.
type intro struct {
	client  uint64 // serverConn.seq of the client's connection
	host    string // the host's ID; its connection is looked up when needed
	at      time.Time
	relayed bool
}

const (
	introTTL  = 60 * time.Second
	maxIntros = 20000
)

// Two machines with one identity key (a cloned VM image, a copied
// configuration directory) replace each other's registration once a second,
// forever, and clients reach either at random. The server notices when a
// registration comes from an address whose registration of the same ID it
// replaced moments ago, while the other one is still connected: after
// cloneFlaps such returns within cloneWindow it keeps the one online and
// refuses the other address for cloneBlock (CodeIdentityInUse). A host that
// merely restarted, moved or reconnected never comes back from an address
// it was replaced at while its replacement lives.
const (
	cloneWindow = 2 * time.Minute
	cloneFlaps  = 3
	cloneBlock  = MaxRetryAfter
	maxGone     = 8
)

// replacements is what the server remembers about an ID's registrations
// replacing one another; see cloneWindow.
type replacements struct {
	gone  map[netip.AddrPort]time.Time // addresses replaced, and when
	flaps []time.Time
	block time.Time // other addresses than the online one are refused until then
}

// Server is a rendezvous server.
type Server struct {
	cfg      Config
	log      *slog.Logger
	id       *auth.Identity
	store    *idStore
	accounts *accounts // nil unless accounts are on

	registers   *rateTable[netip.Addr]
	newIDs      *rateTable[netip.Addr] // per source
	newIDsNet   *rateTable[netip.Addr] // per network
	newIDsAll   *rateTable[struct{}]   // global
	connects    *rateTable[netip.Addr] // per source
	connectsNet *rateTable[netip.Addr] // per network
	misses      *rateTable[struct{}]   // global, connects to IDs not online
	offers      *rateTable[string]
	// handshakes is the budget of new connections accepted without first
	// making the client prove it owns its source address.
	handshakes *rateTable[struct{}]

	altPort  atomic.Int32
	mainPort atomic.Int32 // set by Serve; the websocket bridge needs it
	ws       wsRegistry

	relayBudget *nat.ByteBudget
	relayCtx    context.Context // set by Serve
	relays      int             // guarded by mu
	intros      map[string]*intro
	connSeq     atomic.Uint64

	relayDeny     *denyFile   // nil without a deny list
	relayUsage    *relayUsage // nil without a relay
	relayCounters relayCounters
	relaySources  map[netip.Addr]int // relayed sessions per source; guarded by mu

	mu        sync.Mutex
	online    map[string]*serverConn
	repl      map[string]*replacements // see cloneWindow
	perSource map[netip.Addr]int
	conns     int
}

type serverConn struct {
	q   *quic.Conn
	seq uint64 // unique per server, for intro
	// addr is where the connection came from when it was accepted. A QUIC
	// connection follows its peer to a new address; current says where it
	// is now.
	addr   netip.AddrPort
	source netip.Addr             // IPv4 address or IPv6 /64
	net    netip.Addr             // IPv6 /48; the address itself for IPv4
	id     atomic.Pointer[string] // set once registered
	regMu  sync.Mutex             // serialises registrations on this connection
	// bridged: the peer comes through the websocket bridge (addr is its
	// bridge socket; source and net are the WebSocket client's).
	bridged bool
	// real is the peer's own address: addr's, or the WebSocket client's.
	real  netip.Addr
	moved atomic.Bool // asked to register again (closeMoved)
	// outcomes counts the outcome reports made on this connection.
	outcomes atomic.Int64
}

// current is the address the peer's packets come from now.
func (c *serverConn) current() netip.AddrPort {
	if c.bridged {
		return c.addr
	}
	if ap, ok := remoteAddrPort(c.q); ok {
		return ap
	}
	return c.addr
}

// peerAddr is the address others are told about for this peer: where its
// packets come from now, not where they came from when it connected.
func (c *serverConn) peerAddr() netip.AddrPort {
	if c.bridged {
		return bridgedPeerAddr
	}
	return c.current()
}

// via is the peerInfo marker for this peer's transport.
func (c *serverConn) via() string {
	if c.bridged {
		return viaWS
	}
	return ""
}

// NewServer loads (or creates) the server's key and ID table from
// cfg.DataDir.
func NewServer(cfg Config) (*Server, error) {
	cfg.Limits.defaults()
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	id, err := auth.LoadIdentityFile(filepath.Join(cfg.DataDir, keyFile))
	if err != nil {
		return nil, err
	}
	store, err := openStore(filepath.Join(cfg.DataDir, idsFile), cfg.Limits.MaxIDs, cfg.Limits.IDExpiry)
	if err != nil {
		return nil, err
	}
	var accts *accounts
	if cfg.Accounts != nil {
		ac := *cfg.Accounts
		if accts, err = newAccounts(ac, filepath.Join(cfg.DataDir, accountsFile), cfg.Logger); err != nil {
			store.close()
			return nil, err
		}
	}
	l := cfg.Limits
	var relayBudget *nat.ByteBudget
	var deny *denyFile
	var usage *relayUsage
	if cfg.Relay != nil {
		rc := *cfg.Relay
		rc.defaults()
		cfg.Relay = &rc
		relayBudget = nat.NewByteBudget(cfg.Relay.TotalRate)
		if rc.DenyFile != "" {
			deny = &denyFile{path: rc.DenyFile, log: cfg.Logger}
			if err := deny.reload(true); err != nil {
				store.close()
				return nil, err
			}
		}
		if usage, err = openUsage(cfg.DataDir, rc.MonthlyBytes, rc.Now, cfg.Logger); err != nil {
			store.close()
			return nil, err
		}
	}
	return &Server{
		relayDeny:   deny,
		relayUsage:  usage,
		accounts:    accts,
		relayBudget: relayBudget,
		relayCtx:    context.Background(),
		intros:      map[string]*intro{},
		cfg:         cfg,
		log:         cfg.Logger,
		id:          id,
		store:       store,
		registers:   newRateTable[netip.Addr](l.RegisterBurst, l.RegisterEvery),
		newIDs:      newRateTable[netip.Addr](l.NewIDBurst, l.NewIDEvery),
		newIDsNet:   newRateTable[netip.Addr](l.NewIDNetBurst, l.NewIDNetEvery),
		newIDsAll:   newRateTable[struct{}](l.NewIDGlobalBurst, l.NewIDGlobalEvery),
		connects:    newRateTable[netip.Addr](l.ConnectBurst, l.ConnectEvery),
		connectsNet: newRateTable[netip.Addr](l.ConnectNetBurst, l.ConnectNetEvery),
		misses:      newRateTable[struct{}](l.MissBurst, l.MissEvery),
		offers:      newRateTable[string](l.OfferBurst, l.OfferEvery),
		handshakes:  newRateTable[struct{}](500, 2*time.Millisecond),
		online:      map[string]*serverConn{},
		repl:        map[string]*replacements{},
		perSource:   map[netip.Addr]int{},
	}, nil
}

// PublicKey is the server's TLS identity, which clients may pin.
func (s *Server) PublicKey() ed25519.PublicKey { return s.id.Public() }

// Close releases the ID journal. Call it after Serve has returned.
func (s *Server) Close() error {
	err := s.store.close()
	if s.relayUsage != nil {
		err = errors.Join(err, s.relayUsage.flush())
	}
	if s.accounts != nil {
		err = errors.Join(err, s.accounts.store.close())
	}
	return err
}

// Serve runs the rendezvous service on main and, if alt is not nil, the
// NAT-probe service on alt, until ctx is cancelled. It closes both
// connections before returning.
func (s *Server) Serve(ctx context.Context, main, alt net.PacketConn) error {
	defer main.Close()
	if alt != nil {
		defer alt.Close()
		if ua, ok := alt.LocalAddr().(*net.UDPAddr); ok {
			s.altPort.Store(int32(ua.Port))
		}
		go func() {
			if err := nat.ServeReflect(alt); err != nil {
				s.log.Error("NAT-probe service stopped", "err", err)
			}
		}()
	}

	tlsConf, err := s.id.ServerTLS(ALPN, false)
	if err != nil {
		return err
	}
	tr := &quic.Transport{
		Conn: main,
		// A peer whose connection this server no longer knows (it restarted,
		// or dropped a host that slept) learns so from its next packet
		// instead of after its idle timeout. Derived from the server's key,
		// so the resets stay valid across restarts.
		StatelessResetKey: s.resetKey(),
		// QUIC already limits what it sends to an unverified address to
		// three times what it received. Under a flood of new connections
		// we additionally demand a Retry round trip, so spoofed Initials
		// cost us no handshake state at all.
		VerifySourceAddress: func(net.Addr) bool {
			ok, _ := s.handshakes.allow(struct{}{})
			return !ok
		},
	}
	defer tr.Close()
	// Connection handlers and the helpers below end when ctx does.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Peers measure their NAT against the main port too, before they ask
	// for anything; see nat.ServeReflectQUIC.
	go func() {
		if err := nat.ServeReflectQUIC(ctx, tr, uint16(s.altPort.Load())); err != nil {
			s.log.Error("NAT-probe service on the main port stopped", "err", err)
		}
	}()
	s.mu.Lock()
	s.relayCtx = ctx
	s.mu.Unlock()
	// The websocket bridge starts serving once it knows the port, and
	// from then on stops with ctx.
	if ua, ok := main.LocalAddr().(*net.UDPAddr); ok {
		s.mainPort.Store(int32(ua.Port))
	}
	go s.housekeeping(ctx)
	go s.shortHousekeeping(ctx)
	if s.cfg.Relay != nil {
		go s.relayHousekeeping(ctx)
	}
	ln, err := tr.Listen(tlsConf, &quic.Config{
		HandshakeIdleTimeout: 5 * time.Second,
		// Hosts send keep-alives well inside this window; a host that
		// vanished is noticed, and shown offline, after at most this long.
		MaxIdleTimeout: 40 * time.Second,
		// A peer has no reason to run more than a couple of requests at
		// once, or to send more than a few kilobytes.
		MaxIncomingStreams:             8,
		MaxIncomingUniStreams:          -1,
		InitialStreamReceiveWindow:     16 << 10,
		MaxStreamReceiveWindow:         16 << 10,
		InitialConnectionReceiveWindow: 32 << 10,
		MaxConnectionReceiveWindow:     32 << 10,
		// The server answers at the size the peer started with; see
		// nat.InitialPacketSize.
		InitialPacketSize: nat.InitialPacketSize,
	})
	if err != nil {
		return err
	}
	defer ln.Close()

	// Wait for the connection handlers so that every peer has been sent a
	// close before the socket goes away.
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for {
		q, err := ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		wg.Go(func() { s.handleConn(ctx, q) })
	}
}

func (s *Server) isOnline(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.online[id] != nil
}

// relayHousekeeping keeps the deny list current, persists the relay usage
// and writes the hourly relay summary.
func (s *Server) relayHousekeeping(ctx context.Context) {
	deny := time.NewTicker(denyPollEvery)
	flush := time.NewTicker(usageFlushEvery)
	summary := time.NewTicker(time.Hour)
	defer deny.Stop()
	defer flush.Stop()
	defer summary.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deny.C:
			if err := s.relayDeny.reload(false); err != nil {
				s.log.Error("relay deny list not reloaded; the previous one stays in force", "err", err)
			}
		case <-flush.C:
			if err := s.relayUsage.flush(); err != nil {
				s.log.Error("cannot save the relay usage", "err", err)
			}
		case <-summary.C:
			s.logRelaySummary()
		}
	}
}

// ReloadRelayDeny re-reads the relay deny list now (on SIGHUP).
func (s *Server) ReloadRelayDeny() error { return s.relayDeny.reload(true) }

// housekeeping expires idle IDs and keeps the journal compact.
func (s *Server) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		dropped, err := s.store.maintain(s.isOnline)
		if err != nil {
			s.log.Error("ID journal maintenance failed", "err", err)
		}
		if dropped > 0 {
			s.log.Info("expired idle IDs", "count", dropped, "remaining", s.store.count())
		}
	}
}

// shortHousekeeping runs the checks that cannot wait for the hourly round.
func (s *Server) shortHousekeeping(ctx context.Context) {
	t := time.NewTicker(moveCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.checkMoves()
			s.expireIntros(now)
		}
	}
}

// resetKey derives the QUIC stateless reset key from the server's identity:
// an Ed25519 signature is deterministic and only the key's holder can make it.
func (s *Server) resetKey() *quic.StatelessResetKey {
	k := quic.StatelessResetKey(sha256.Sum256(s.id.Sign([]byte("remohop rendezvous stateless reset key v1"))))
	return &k
}

// expireIntros forgets introductions too old to be relayed, and
// replacements too old to matter.
func (s *Server) expireIntros(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, in := range s.intros {
		if now.Sub(in.at) > introTTL {
			delete(s.intros, k)
		}
	}
	for id, r := range s.repl {
		for a, t := range r.gone {
			if now.Sub(t) > cloneWindow {
				delete(r.gone, a)
			}
		}
		if len(r.gone) == 0 && now.After(r.block) {
			delete(s.repl, id)
		}
	}
}

// cloneCheck decides whether c may take id over from old, the connection
// registered under it now (see cloneWindow). It returns how long to refuse
// c, or 0. s.mu must be held.
func (s *Server) cloneCheck(id string, c, old *serverConn, now time.Time) time.Duration {
	if old == nil || old == c || c.bridged || old.bridged {
		return 0
	}
	from, was := c.current(), old.current()
	if from == was {
		return 0 // the same socket: the same machine
	}
	r := s.repl[id]
	if r == nil {
		r = &replacements{gone: map[netip.AddrPort]time.Time{}}
		s.repl[id] = r
	}
	if now.Before(r.block) {
		r.block = now.Add(cloneBlock)
		return cloneBlock
	}
	if at, ok := r.gone[from]; ok && now.Sub(at) < cloneWindow {
		recent := r.flaps[:0]
		for _, t := range r.flaps {
			if now.Sub(t) < cloneWindow {
				recent = append(recent, t)
			}
		}
		r.flaps = append(recent, now)
		if len(r.flaps) >= cloneFlaps {
			r.block = now.Add(cloneBlock)
			s.log.Warn("two machines are registering with the same identity key (a cloned disk image or copied configuration?); "+
				"keeping one online and refusing the other", "id", id, "online", was, "refused", from)
			return cloneBlock
		}
	}
	if len(r.gone) >= maxGone {
		for a := range r.gone {
			delete(r.gone, a)
			break
		}
	}
	r.gone[was] = now
	return 0
}

// checkMoves asks registered hosts whose address changed to register again
// (closeMoved). Until they do, introductions already carry the new address
// (peerAddr).
func (s *Server) checkMoves() {
	s.mu.Lock()
	hosts := make([]*serverConn, 0, len(s.online))
	for _, c := range s.online {
		hosts = append(hosts, c)
	}
	s.mu.Unlock()
	for _, c := range hosts {
		if c.bridged {
			continue
		}
		if now := c.current(); now != c.addr && c.moved.CompareAndSwap(false, true) {
			id := ""
			if p := c.id.Load(); p != nil {
				id = *p
			}
			s.log.Info("host moved; asking it to register again", "id", id, "from", c.addr, "to", now)
			c.q.CloseWithError(closeMoved, "your address changed; register again")
		}
	}
}

func (s *Server) handleConn(ctx context.Context, q *quic.Conn) {
	addr, ok := remoteAddrPort(q)
	if !ok {
		q.CloseWithError(closeNormal, "unsupported address")
		return
	}
	c := &serverConn{q: q, seq: s.connSeq.Add(1), addr: addr, real: addr.Addr()}
	c.source, c.net = sourceOf(addr.Addr())
	if client, ok := s.ws.bridgedClient(addr); ok {
		// Limits count against where the WebSocket came from.
		c.bridged, c.real = true, client
		c.source, c.net = sourceOf(client)
	}

	s.mu.Lock()
	if s.conns >= s.cfg.Limits.MaxConns || s.perSource[c.source] >= s.cfg.Limits.MaxConnsPerSource {
		s.mu.Unlock()
		q.CloseWithError(closeBusy, "too many connections")
		return
	}
	s.conns++
	s.perSource[c.source]++
	s.mu.Unlock()

	// Dialling peers are done within seconds. Only registered hosts may
	// hold a connection open.
	idle := time.AfterFunc(s.cfg.Limits.ClientLifetime, func() {
		if c.id.Load() == nil {
			q.CloseWithError(closeIdle, "idle")
		}
	})

	defer func() {
		idle.Stop()
		q.CloseWithError(closeNormal, "rendezvous server shutting down")
		s.mu.Lock()
		s.conns--
		if s.perSource[c.source]--; s.perSource[c.source] <= 0 {
			delete(s.perSource, c.source)
		}
		var offline string
		if id := c.id.Load(); id != nil && s.online[*id] == c {
			delete(s.online, *id)
			offline = *id
			s.log.Info("host offline", "id", *id, "addr", c.addr)
		}
		s.mu.Unlock()
		if offline != "" && s.accounts != nil {
			if key, ok := s.store.keyOf(offline); ok {
				s.accounts.deviceSeen(key)
			}
		}
	}()

	for {
		st, err := q.AcceptStream(ctx)
		if err != nil {
			return
		}
		go s.handleStream(c, st)
	}
}

func remoteAddrPort(q *quic.Conn) (netip.AddrPort, bool) {
	ua, ok := q.RemoteAddr().(*net.UDPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	ap := ua.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port()), ap.IsValid()
}

func (s *Server) handleStream(c *serverConn, st *quic.Stream) {
	defer st.Close()
	defer st.CancelRead(0)
	st.SetDeadline(time.Now().Add(requestTimeout))

	var req request
	if err := readMsg(st, &req); err != nil {
		return
	}
	fail := func(code, msg string, retry time.Duration) {
		writeMsg(st, response{Code: code, Error: msg, RetryAfterMS: retry.Milliseconds()})
	}
	if req.V != protoVersion {
		fail(CodeBadRequest, "unsupported protocol version", 0)
		return
	}
	switch req.T {
	case "register":
		s.handleRegister(c, st, &req, fail)
	case "connect":
		s.handleConnect(c, st, &req, fail)
	case "presence":
		s.handlePresence(c, st, &req, fail)
	case "relay":
		s.handleRelay(c, st, &req, fail)
	case "account":
		s.handleAccount(c, st, &req, fail)
	case "outcome":
		s.handleOutcome(c, st, &req, fail)
	default:
		fail(CodeBadRequest, "unknown request", 0)
	}
}

type failFunc func(code, msg string, retry time.Duration)

func (s *Server) handleRegister(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	if len(req.Key) != ed25519.PublicKeySize {
		fail(CodeBadRequest, "malformed key", 0)
		return
	}
	if ok, wait := s.registers.allow(c.source); !ok {
		fail(CodeRateLimited, "too many registrations", wait)
		return
	}
	key := ed25519.PublicKey(req.Key)
	if s.relayDeny.list().key(key) {
		fail(CodeBadRequest, "this identity is not accepted by this server", 0)
		return
	}

	challenge := make([]byte, challengeSize)
	if _, err := rand.Read(challenge); err != nil {
		fail(CodeBadRequest, "internal error", 0)
		return
	}
	if err := writeMsg(st, response{OK: true, Challenge: challenge}); err != nil {
		return
	}
	var p proof
	if err := readMsg(st, &p); err != nil {
		return
	}
	cs := c.q.ConnectionState().TLS
	exporter, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
	if err != nil || !verifyRegistration(key, challenge, exporter, p.Sig) {
		fail(CodeBadRequest, "proof of key possession failed", 0)
		return
	}

	// One connection is one host: presence is tracked per connection, so a
	// second identity on it would leave the first one looking online.
	c.regMu.Lock()
	defer c.regMu.Unlock()
	if cur := c.id.Load(); cur != nil {
		if id, known := s.store.lookup(key); !known || id != *cur {
			fail(CodeBadRequest, "this connection is already registered under another key", 0)
			return
		}
	}

	// Only now, with the key proven, may the request touch the ID table.
	// New allocations draw on a much smaller budget than re-registrations.
	if _, known := s.store.lookup(key); !known {
		if ok, wait := s.newIDs.allow(c.source); !ok {
			fail(CodeRateLimited, "too many new IDs from this address", wait)
			return
		}
		if ok, wait := s.newIDsNet.allow(c.net); !ok {
			fail(CodeRateLimited, "too many new IDs from this network", wait)
			return
		}
		if ok, wait := s.newIDsAll.allow(struct{}{}); !ok {
			fail(CodeRateLimited, "the server is allocating IDs as fast as it allows itself to", wait)
			return
		}
	}
	id, isNew, err := s.store.assign(key, s.isOnline)
	if errors.Is(err, errStoreFull) {
		fail(CodeFull, "this server is not accepting new hosts", 0)
		return
	}
	if err != nil {
		s.log.Error("ID allocation failed", "err", err)
		fail(CodeBadRequest, "internal error", 0)
		return
	}

	s.mu.Lock()
	old := s.online[id]
	if wait := s.cloneCheck(id, c, old, time.Now()); wait > 0 {
		s.mu.Unlock()
		fail(CodeIdentityInUse, "another machine is registered under this host's identity key (a cloned disk image or a copied "+
			"configuration directory?); each machine needs its own: delete identity.key in the copy's configuration directory", wait)
		return
	}
	s.online[id] = c
	s.mu.Unlock()
	c.id.Store(&id)
	if old != nil && old != c {
		// The same key registered again (the host restarted or changed
		// network before we noticed the old connection die).
		old.q.CloseWithError(closeReplaced, "registered from another connection")
	}
	s.log.Info("host online", "id", id, "addr", c.addr, "new", isNew, "websocket", c.bridged)
	if s.accounts != nil {
		s.accounts.deviceSeen(key)
	}
	writeMsg(st, response{OK: true, ID: id, You: c.peerAddr().String(), AltPort: int(s.altPort.Load())})
}

// handlePresence tells a client which of the IDs it names are online. It
// is a batch of what connect requests would find out, without contacting
// any host, and is charged as such (see the protocol description).
func (s *Server) handlePresence(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	if len(req.IDs) == 0 || len(req.IDs) > MaxPresenceIDs {
		fail(CodeBadRequest, fmt.Sprintf("a presence request names 1 to %d IDs", MaxPresenceIDs), 0)
		return
	}
	for _, id := range req.IDs {
		if !validID(id) {
			fail(CodeBadRequest, "malformed ID", 0)
			return
		}
	}
	out := make([]string, len(req.IDs))
	limited := false
	for i, id := range req.IDs {
		if limited {
			out[i] = PresenceLimited
			continue
		}
		if ok, _ := s.connects.allow(c.source); !ok {
			limited = true
		} else if ok, _ := s.connectsNet.allow(c.net); !ok {
			limited = true
		}
		if limited {
			out[i] = PresenceLimited
			continue
		}
		s.mu.Lock()
		online := s.online[id] != nil
		s.mu.Unlock()
		switch {
		case online:
			out[i] = PresenceOnline
		case !func() bool { ok, _ := s.misses.allow(struct{}{}); return ok }():
			out[i] = PresenceLimited
		case s.store.known(id):
			out[i] = PresenceOffline
		default:
			out[i] = PresenceUnknown
		}
	}
	writeMsg(st, response{OK: true, Presence: out})
}

func (s *Server) handleConnect(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	if !validID(req.ID) {
		fail(CodeBadRequest, "malformed ID", 0)
		return
	}
	// Charged before the lookup, so probing for which IDs exist is as
	// rate-limited as connecting to them.
	if ok, wait := s.connects.allow(c.source); !ok {
		fail(CodeRateLimited, "too many connection requests", wait)
		return
	}
	if ok, wait := s.connectsNet.allow(c.net); !ok {
		fail(CodeRateLimited, "too many connection requests from this network", wait)
		return
	}
	local, err := parseCandidates(req.Candidates, c.current().Addr())
	if err != nil {
		fail(CodeBadRequest, err.Error(), 0)
		return
	}
	if c.bridged {
		// Nothing a bridged peer says about its addresses is reachable,
		// and its observed address is this machine's loopback.
		local = nil
	}

	s.mu.Lock()
	host := s.online[req.ID]
	s.mu.Unlock()
	if host == nil {
		// Whether an ID exists is only told while the global budget for
		// such answers lasts.
		if ok, wait := s.misses.allow(struct{}{}); !ok {
			fail(CodeRateLimited, "too many requests for hosts that are not online", wait)
			return
		}
		if s.store.known(req.ID) {
			fail(CodeOffline, "", 0)
		} else {
			fail(CodeUnknownID, "", 0)
		}
		return
	}
	if (c.bridged || host.bridged) && s.cfg.Relay == nil {
		fail(CodeNoRelay, "one side reaches this server only through its websocket bridge, which needs the relay, and this server does not relay", 0)
		return
	}
	if ok, wait := s.offers.allow(req.ID); !ok {
		fail(CodeRateLimited, "the host is receiving too many requests", wait)
		return
	}

	nonce := make([]byte, nat.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		fail(CodeBadRequest, "internal error", 0)
		return
	}
	st.SetDeadline(time.Now().Add(requestTimeout + offerTimeout))
	answer, err := s.offer(host, request{
		T:     "offer",
		V:     protoVersion,
		Nonce: nonce,
		Peer:  &peerInfo{Reflexive: c.peerAddr().String(), Local: formatCandidates(local), NAT: cleanNAT(req.NAT), Via: c.via()},
	})
	if err != nil {
		s.log.Debug("offer failed", "id", req.ID, "err", err)
		fail(CodeNoAnswer, "", 0)
		return
	}
	if !answer.OK {
		// The reason is host-supplied text on its way to a stranger.
		fail(CodeDeclined, auth.CleanText(answer.Error, maxErrorText), 0)
		return
	}
	hostLocal, err := parseCandidates(answer.Candidates, host.current().Addr())
	if err != nil {
		fail(CodeNoAnswer, "", 0)
		return
	}
	if host.bridged {
		hostLocal = nil
	}
	s.mu.Lock()
	if len(s.intros) < maxIntros {
		s.intros[string(nonce)] = &intro{client: c.seq, host: req.ID, at: time.Now()}
	}
	s.mu.Unlock()
	s.log.Debug("introduced", "id", req.ID, "client", c.current(), "host", host.current())
	writeMsg(st, response{
		OK:      true,
		Nonce:   nonce,
		Peer:    &peerInfo{Reflexive: host.peerAddr().String(), Local: formatCandidates(hostLocal), NAT: cleanNAT(answer.NAT), Via: host.via()},
		You:     c.peerAddr().String(),
		AltPort: int(s.altPort.Load()),
		Relay:   s.cfg.Relay != nil,
	})
}

// handleRelay sets up a relayed session for an introduction this server
// made a moment ago, at the request of that introduction's client.
func (s *Server) handleRelay(c *serverConn, st *quic.Stream, req *request, fail failFunc) {
	rc := s.cfg.Relay
	if rc == nil {
		fail(CodeNoRelay, "this rendezvous server does not relay", 0)
		return
	}
	if ok, wait := s.connects.allow(c.source); !ok {
		fail(CodeRateLimited, "too many requests", wait)
		return
	}
	s.mu.Lock()
	in := s.intros[string(req.Nonce)]
	valid := in != nil && in.client == c.seq && !in.relayed && time.Since(in.at) < introTTL
	var host *serverConn
	if valid {
		in.relayed = true // one relay per introduction
		host = s.online[in.host]
	}
	s.mu.Unlock()
	if !valid {
		fail(CodeBadRequest, "no such introduction", 0)
		return
	}
	if host == nil {
		fail(CodeNoRelay, "the host went offline", 0)
		return
	}
	// Who the peers are, as far as this server can prove: a client key
	// only with a signature for this introduction on this connection, a
	// host key as proven at registration.
	var clientKey []byte
	if len(req.Key) > 0 {
		cs := c.q.ConnectionState().TLS
		exp, err := cs.ExportKeyingMaterial(relayExporterLabel, nil, 32)
		if err != nil || len(req.Key) != ed25519.PublicKeySize ||
			!ed25519.Verify(ed25519.PublicKey(req.Key), relayProofMessage(req.Nonce, exp), req.Proof) {
			fail(CodeBadRequest, "proof of key possession failed", 0)
			return
		}
		clientKey = req.Key
	}
	var hostKey []byte
	if id := host.id.Load(); id != nil {
		hostKey, _ = s.store.keyOf(*id)
	}
	dec, reason, msg := s.admitRelay(relayPeer{source: c.source, addr: c.real, key: clientKey},
		relayPeer{source: host.source, addr: host.real, key: hostKey})
	if reason != "" {
		s.relayCounters.refuse(reason)
		s.log.Debug("relay refused", "reason", reason, "client", c.addr)
		fail(CodeNoRelay, msg, 0)
		return
	}
	s.mu.Lock()
	if s.relays >= rc.MaxSessions {
		s.mu.Unlock()
		dec.release()
		s.relayCounters.refuse(refusedFull)
		fail(CodeNoRelay, "all relay slots are in use; try again later", 0)
		return
	}
	s.relays++
	relayCtx := s.relayCtx
	s.mu.Unlock()
	release := func() {
		dec.release()
		s.mu.Lock()
		s.relays--
		s.mu.Unlock()
	}
	pc, err := rc.Listen()
	if errors.Is(err, ErrRelayPortsFull) {
		release()
		s.relayCounters.refuse(refusedFull)
		s.log.Warn("relay: refused a session, every port of --relay-ports is in use", "client", c.addr)
		fail(CodeFull, "all relay ports are in use; try again later", 0)
		return
	}
	if err != nil {
		release()
		s.log.Error("relay: cannot open a socket", "err", err)
		fail(CodeNoRelay, "the relay is unavailable", 0)
		return
	}
	port := 0
	if ua, ok := pc.LocalAddr().(*net.UDPAddr); ok {
		port = ua.Port
	}
	// The session exists from here on; it ends by itself when nobody
	// binds or the traffic stops. While it lasts, bridged peers may
	// address its port.
	if port > 0 && port <= 65535 {
		s.ws.relayPorts.Store(uint16(port), struct{}{})
	}
	go func() {
		defer release()
		defer s.ws.relayPorts.Delete(uint16(port))
		start := time.Now()
		stats := nat.ServeRelay(relayCtx, pc, req.Nonce, nat.RelayLimits{Rate: dec.rate, Global: s.relayBudget, Idle: rc.Idle, Meter: s.relayUsage.add})
		s.log.Info("relay session ended", "client", c.addr, "host", host.addr, "duration", time.Since(start).Round(time.Second),
			"bytes", stats.Bytes, "delayed_packets", stats.Delayed, "dropped_packets", stats.Dropped)
	}()
	st.SetDeadline(time.Now().Add(requestTimeout + offerTimeout))
	answer, err := s.offer(host, request{T: "relay", V: protoVersion, Nonce: req.Nonce, RelayPort: port})
	if err != nil || !answer.OK {
		reason := "the host did not accept the relay"
		if err == nil && answer.Code == CodeNoRelay {
			reason = "the host does not accept relayed connections"
		}
		fail(CodeNoRelay, reason, 0)
		return
	}
	s.relayCounters.sessions.Add(1)
	tier := "anonymous"
	if dec.account {
		tier = "account"
	}
	s.log.Info("relay session started", "client", c.addr, "host", host.addr, "port", port, "tier", tier, "kbps", dec.rate/125)
	writeMsg(st, response{OK: true, RelayPort: port})
}

// offer delivers an introduction to a host and waits for its answer.
func (s *Server) offer(host *serverConn, req request) (*response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), offerTimeout)
	defer cancel()
	st, err := host.q.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	defer st.CancelRead(0)
	st.SetDeadline(time.Now().Add(offerTimeout))
	if err := writeMsg(st, req); err != nil {
		return nil, err
	}
	var resp response
	if err := readMsg(st, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
