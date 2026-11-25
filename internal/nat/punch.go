package nat

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const (
	// NonceSize is the length of the session nonce the rendezvous server
	// hands to both peers.
	NonceSize = 32

	// maxLearned bounds how many peer-reflexive addresses one session
	// adopts, so forged probes cannot turn us into a packet source aimed
	// at arbitrary addresses.
	maxLearned = 4

	probeFirstInterval = 40 * time.Millisecond
	probeMaxInterval   = 400 * time.Millisecond

	// DefaultPunchTime is how long the peers keep probing before the
	// attempt is declared failed. NATs that are going to open do so within
	// the first few probes; the rest is margin for packet loss.
	DefaultPunchTime = 8 * time.Second

	// maxDials bounds the QUIC handshakes raced over working paths.
	maxDials = 3

	// maxHedges bounds the extra handshakes started over a path whose
	// first one has not finished (see hedgeAfter).
	maxHedges = 6

	// maxRoutable bounds how many globally routable addresses one session
	// probes on somebody's say-so. The candidates come from the other peer
	// through the rendezvous server, neither of which is trusted not to
	// name a third party; an honest peer has one public address, perhaps
	// two.
	maxRoutable = 2

	// hostHold is how long a host waits to hear from the client before it
	// sends normal-TTL probes to the client's public candidates, and
	// primeTTLs are the TTLs it probes with meanwhile; see "Who sends
	// first" in the package comment. The hold normally ends early, when
	// the client's first probe arrives; its length only has to outlast
	// the time from the host's answer to that probe: the answer's trip
	// through the rendezvous server to the client, and the client getting
	// round to probing, which on a loaded or slow machine can take a good
	// part of a second. A hold that ends too soon loses the punch between
	// NATs that remember unsolicited packets. A long one costs time only
	// when the client's probes cannot reach the host at all, and those
	// punches fail either way: a client that can reach a primed host
	// always does so within a round trip. TTL 2 opens a NAT one hop away,
	// TTL 3 also a second NAT right behind it (a home router behind the
	// provider's box); higher values would more and more often reach the
	// client's NAT, which is exactly what the hold is there to prevent.
	hostHold = 2 * time.Second

	// hostLinger is how long a host keeps a punch session going after the
	// path has been proven. By then the client is already dialling QUIC
	// through the open mappings; the short tail only covers a client that
	// lost our first acks. Ending early keeps a busy host's session table
	// (maxSessions) free for new clients.
	hostLinger = 2 * time.Second
)

var primeTTLs = [...]int{2, 3}

// Session is one punch in progress towards one peer.
type Session struct {
	sock *Socket
	role Role
	key  []byte
	tag  [8]byte

	paths chan netip.AddrPort
	// proven is closed when the first path has been found, heard when the
	// first valid packet from the peer has arrived.
	proven     chan struct{}
	provenOnce sync.Once
	heard      chan struct{}
	heardOnce  sync.Once

	closeOnce sync.Once
	closed    chan struct{}

	mu      sync.Mutex
	remotes []netip.AddrPort
	known   map[netip.AddrPort]bool
	working map[netip.AddrPort]bool
	learned int
}

var sha256New = sha256.New

func derive(nonce []byte, label string) []byte {
	m := hmac.New(sha256.New, nonce)
	m.Write([]byte(label))
	return m.Sum(nil)
}

// Punch starts probing remote, the peer's candidates, and answering the
// peer's probes, for at most lifetime. Both peers must call it with the
// same nonce and opposite roles. The caller should Close the session once
// it has what it wanted.
func (s *Socket) Punch(nonce []byte, role Role, remote []netip.AddrPort, lifetime time.Duration) (*Session, error) {
	if len(nonce) != NonceSize {
		return nil, errors.New("nat: bad session nonce")
	}
	sess := &Session{
		sock:    s,
		role:    role,
		key:     derive(nonce, "remohop punch key"),
		tag:     [8]byte(derive(nonce, "remohop punch tag")),
		paths:   make(chan netip.AddrPort, 16),
		proven:  make(chan struct{}),
		heard:   make(chan struct{}),
		closed:  make(chan struct{}),
		known:   map[netip.AddrPort]bool{},
		working: map[netip.AddrPort]bool{},
	}
	routable := 0
	for _, r := range remote {
		r = normalize(r)
		if !r.IsValid() || r.Port() == 0 || sess.known[r] || len(sess.remotes) >= 2*MaxCandidates {
			continue
		}
		if Routable(r.Addr()) {
			if routable == maxRoutable {
				continue
			}
			routable++
		}
		sess.known[r] = true
		sess.remotes = append(sess.remotes, r)
	}

	s.mu.Lock()
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		return nil, errors.New("nat: too many connection attempts in progress")
	}
	if _, dup := s.sessions[sess.tag]; dup {
		s.mu.Unlock()
		return nil, errors.New("nat: session nonce reused")
	}
	s.sessions[sess.tag] = sess
	s.mu.Unlock()

	// A host that can prime its NAT holds back normal probes to public
	// candidates until the client has spoken or the hold runs out. Not
	// without a NAT (nothing to prime), and not behind a symmetric NAT:
	// its mapping towards the client is new whoever sends first, so the
	// client can only learn it from the host's real probes, which then
	// should not wait.
	natType := s.NATType()
	holding := role == RoleHost && s.ttl != nil && natType != TypeSymmetric && natType != TypeNone
	// The first round goes out before Punch returns. A host calls Punch
	// before it answers the introduction, so its NAT is primed before the
	// client can know where to send: the client's first probe can never
	// arrive ahead of the priming, however the two machines schedule.
	sess.round(holding)
	go sess.run(lifetime, holding)
	return sess, nil
}

// round sends one probe to every candidate, low-TTL priming probes only to
// public IPv4 candidates while holding.
func (p *Session) round(holding bool) {
	p.mu.Lock()
	targets := append([]netip.AddrPort(nil), p.remotes...)
	p.mu.Unlock()
	for _, r := range targets {
		// Errors are per destination (no route to an IPv6 candidate from
		// an IPv4-only machine, say) and simply mean that candidate will
		// not answer.
		if holding && r.Addr().Is4() && !lanLike(r.Addr()) {
			for _, ttl := range primeTTLs {
				p.sock.ttl(p.packet(pktProbe, nil), r, ttl)
			}
			continue
		}
		p.sock.send(p.packet(pktProbe, nil), r)
	}
}

// Paths delivers each remote address a probe-ack arrived from, i.e. each
// address proven reachable in both directions, in the order they answered.
func (p *Session) Paths() <-chan netip.AddrPort { return p.paths }

// Close stops the session.
func (p *Session) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.sock.mu.Lock()
		delete(p.sock.sessions, p.tag)
		p.sock.mu.Unlock()
	})
}

func (p *Session) run(lifetime time.Duration, holding bool) {
	defer p.Close()
	end := time.NewTimer(lifetime)
	defer end.Stop()
	proven := p.proven
	if p.role != RoleHost {
		proven = nil // the dialling side closes the session itself
	}
	var holdEnd <-chan time.Time
	heard := p.heard
	if holding {
		t := time.NewTimer(min(hostHold, lifetime))
		defer t.Stop()
		holdEnd = t.C
	} else {
		heard = nil
	}

	interval := probeFirstInterval
	for {
		// Punch sent the first round; each later one follows a pause, or
		// the end of the hold.
		t := time.NewTimer(interval)
		select {
		case <-t.C:
			interval = min(interval*3/2, probeMaxInterval)
		case <-heard:
			t.Stop()
			heard, holdEnd, holding = nil, nil, false
		case <-holdEnd:
			t.Stop()
			heard, holdEnd, holding = nil, nil, false
		case <-proven:
			t.Stop()
			proven = nil
			end.Reset(min(hostLinger, lifetime))
			continue
		case <-end.C:
			t.Stop()
			return
		case <-p.closed:
			t.Stop()
			return
		case <-p.sock.done:
			t.Stop()
			return
		}
		p.round(holding)
	}
}

// packet builds a probe or an ack. An ack echoes the probe's txid.
func (p *Session) packet(typ byte, txid []byte) []byte {
	pkt := make([]byte, probeLen)
	putHeader(pkt, typ)
	copy(pkt[4:12], p.tag[:])
	if txid == nil {
		rand.Read(pkt[12:20])
	} else {
		copy(pkt[12:20], txid)
	}
	copy(pkt[20:], p.mac(pkt[:20], p.role))
	return pkt
}

func (p *Session) mac(body []byte, sender Role) []byte {
	m := hmac.New(sha256.New, p.key)
	m.Write([]byte{byte(sender)})
	m.Write(body)
	return m.Sum(nil)[:16]
}

// handle processes a probe or ack carrying this session's tag.
func (p *Session) handle(pkt []byte, src netip.AddrPort) {
	if !hmac.Equal(pkt[20:36], p.mac(pkt[:20], p.role.peer())) {
		return
	}
	select {
	case <-p.closed:
		return
	default:
	}
	p.heardOnce.Do(func() { close(p.heard) })

	p.mu.Lock()
	adopt := false
	if !p.known[src] && p.learned < maxLearned {
		// Peer-reflexive: the peer reaches us from an address nobody
		// advertised. Probe it like any other candidate.
		p.known[src] = true
		p.learned++
		p.remotes = append(p.remotes, src)
		adopt = true
	}
	newPath := false
	if pkt[0] == pktProbeAck && p.known[src] && !p.working[src] {
		p.working[src] = true
		newPath = true
	}
	p.mu.Unlock()

	if pkt[0] == pktProbe {
		p.sock.send(p.packet(pktProbeAck, pkt[12:20]), src)
	}
	if adopt {
		p.sock.send(p.packet(pktProbe, nil), src)
	}
	if newPath {
		p.provenOnce.Do(func() { close(p.proven) })
		select {
		case p.paths <- src:
		default:
		}
	}
}

// ConnectConfig describes a P2P connection attempt.
type ConnectConfig struct {
	Nonce  []byte           // session nonce from the rendezvous server
	Remote []netip.AddrPort // the host's candidates
	TLS    *tls.Config
	QUIC   *quic.Config
	// PunchTime overrides DefaultPunchTime.
	PunchTime time.Duration
	// Prefer, if set, marks candidates worth waiting for: the host's
	// address on an overlay network both peers are on, such as a tailnet.
	// A proven path that is neither preferred nor on a LAN is held back
	// for up to PreferWait (default DefaultPreferWait) in case a
	// preferred one answers, and while a handshake over a preferred path
	// is in progress. Preferring a path never changes what runs over it:
	// the same QUIC session and login, whatever the path.
	Prefer     func(netip.AddrPort) bool
	PreferWait time.Duration
	// Logger, if set, gets the timing of each step at debug level: when
	// each path answered, when each handshake started and how it ended.
	Logger *slog.Logger
	// OnPath, if set, is called (from Connect's goroutine) when the first
	// path has answered: a direct connection is under way. A caller racing
	// a relay against the punch uses it to keep waiting for the direct one.
	OnPath func()
}

// hedgeAfter are the delays, from a path's first QUIC handshake, after
// which another handshake is started over the same path if none has
// finished. The punch has proven the path both ways, so a handshake that
// has not completed within a few round trips lost its first packets (a
// Wi-Fi station waking up, a neighbour or firewall entry being set up,
// an access point in power save), and QUIC retransmits a lost Initial
// with exponential backoff: after 0.3, 0.9, 2.1, 4.5 and 9.3 s, so
// packets lost for the first few seconds cost up to ten. A fresh
// handshake sends its Initial at once. Whichever finishes first wins and
// the others are closed, as when several paths race.
var hedgeAfter = []time.Duration{400 * time.Millisecond, 1200 * time.Millisecond, 2400 * time.Millisecond, 4 * time.Second}

// DefaultPreferWait is how long a proven but not preferred path waits for
// a preferred one. An overlay network answers within its round trip, which
// through its own relays can be a few hundred milliseconds.
const DefaultPreferWait = time.Second

// preferHold bounds how long a handshake over a preferred path holds back
// proven other paths: one that has not finished by then lost its packets
// or found a path that does not carry QUIC, and would otherwise hold them
// until it timed out (10 s, more with hedges). A variable for tests.
var preferHold = 2500 * time.Millisecond

// Connect punches towards the host and establishes QUIC over the first
// path that works, returning the connection and the host address it runs
// to. When several candidates answer, their handshakes race and the losers
// are cancelled. It returns an error wrapping ErrNoPath when no candidate
// answers within the punch time.
func (s *Socket) Connect(ctx context.Context, cfg ConnectConfig) (*quic.Conn, netip.AddrPort, error) {
	punchTime := cfg.PunchTime
	if punchTime == 0 {
		punchTime = DefaultPunchTime
	}
	// Keep answering and probing a little past the deadline so a handshake
	// started at the last moment still finds its hole open.
	sess, err := s.Punch(cfg.Nonce, RoleClient, cfg.Remote, punchTime+5*time.Second)
	if err != nil {
		return nil, netip.AddrPort{}, err
	}
	defer sess.Close()

	type result struct {
		q    *quic.Conn
		addr netip.AddrPort
		err  error
		pref bool
	}
	logf := func(msg string, args ...any) {
		if cfg.Logger != nil {
			cfg.Logger.Debug(msg, args...)
		}
	}
	begin := time.Now()
	since := func() string { return time.Since(begin).Round(time.Millisecond).String() }
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan result, maxDials+maxHedges)
	started, active, hedges := 0, 0, 0
	var lastErr error
	// pending counts the unfinished handshakes per path; hedge receives a
	// path whose hedge delay has passed.
	pending := map[netip.AddrPort]int{}
	type hedgeReq struct {
		addr netip.AddrPort
		pref bool
	}
	hedge := make(chan hedgeReq, maxDials*len(hedgeAfter))
	var timers []*time.Timer
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()

	// Preference (see ConnectConfig.Prefer): held are proven paths waiting
	// for a preferred one, holdC fires when they have waited long enough.
	var held []netip.AddrPort
	var holdC, prefStaleC <-chan time.Time
	holdOver := cfg.Prefer == nil
	prefActive := 0
	prefStale := false // the preferred handshakes have had their time
	prefDone := func() bool { return prefActive == 0 || prefStale }
	dialOne := func(addr netip.AddrPort, pref bool) {
		active++
		pending[addr]++
		if pref {
			prefActive++
			if prefStaleC == nil && !prefStale {
				t := time.NewTimer(preferHold)
				timers = append(timers, t)
				prefStaleC = t.C
			}
		}
		go func() {
			q, err := s.tr.Dial(dctx, net.UDPAddrFromAddrPort(addr), cfg.TLS, cfg.QUIC)
			results <- result{q, addr, err, pref}
		}()
	}
	startDial := func(addr netip.AddrPort, pref bool) {
		if started == maxDials {
			return
		}
		started++
		logf("punch: QUIC handshake started", "remote", addr, "after", since())
		dialOne(addr, pref)
		for _, d := range hedgeAfter {
			timers = append(timers, time.AfterFunc(d, func() {
				select {
				case hedge <- hedgeReq{addr, pref}:
				default:
				}
			}))
		}
	}
	release := func() {
		for _, a := range held {
			startDial(a, false)
		}
		held = nil
	}

	deadline := time.NewTimer(punchTime)
	defer deadline.Stop()
	expired := false
	for {
		if expired && active == 0 {
			if lastErr != nil {
				return nil, netip.AddrPort{}, fmt.Errorf("%w: %v", ErrNoPath, lastErr)
			}
			return nil, netip.AddrPort{}, ErrNoPath
		}
		select {
		case h := <-hedge:
			if pending[h.addr] > 0 && hedges < maxHedges {
				hedges++
				logf("punch: QUIC handshake not finished; starting another", "remote", h.addr, "after", since())
				dialOne(h.addr, h.pref)
			}
		case addr := <-sess.Paths():
			logf("punch: path answered", "remote", addr, "after", since())
			if cfg.OnPath != nil {
				cfg.OnPath()
				cfg.OnPath = nil
			}
			pref := cfg.Prefer != nil && cfg.Prefer(addr)
			if pref || lanLike(addr.Addr()) || (holdOver && prefDone()) {
				startDial(addr, pref)
				continue
			}
			held = append(held, addr)
			if holdC == nil && !holdOver {
				wait := cfg.PreferWait
				if wait <= 0 {
					wait = DefaultPreferWait
				}
				t := time.NewTimer(wait)
				defer t.Stop()
				holdC = t.C
			}
		case <-holdC:
			holdC, holdOver = nil, true
			if prefDone() {
				release()
			}
		case <-prefStaleC:
			prefStaleC, prefStale = nil, true
			if holdOver {
				logf("punch: preferred path still has no QUIC connection; releasing the others", "after", since())
				release()
			}
		case r := <-results:
			active--
			pending[r.addr]--
			if r.pref {
				prefActive--
			}
			if r.err != nil {
				logf("punch: QUIC handshake failed", "remote", r.addr, "after", since(), "err", r.err)
				if pending[r.addr] > 0 {
					continue // another handshake over this path is still running
				}
				lastErr = fmt.Errorf("QUIC handshake with %s: %w", r.addr, r.err)
				if holdOver && prefDone() {
					release()
				}
				continue
			}
			// Winner. Stop the other handshakes, and close any that
			// completed in the same instant.
			logf("punch: QUIC handshake done", "remote", r.addr, "after", since())
			cancel()
			go func(n int) {
				for ; n > 0; n-- {
					if l := <-results; l.err == nil {
						l.q.CloseWithError(0, "another path won")
					}
				}
			}(active)
			return r.q, r.addr, nil
		case <-deadline.C:
			expired, holdOver = true, true
			release()
		case <-ctx.Done():
			cancel()
			go func(n int) {
				for ; n > 0; n-- {
					if l := <-results; l.err == nil {
						l.q.CloseWithError(0, "cancelled")
					}
				}
			}(active)
			return nil, netip.AddrPort{}, ctx.Err()
		}
	}
}
