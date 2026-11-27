package nat

import (
	"bytes"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Relaying is the fallback for peers that cannot be punched together, when
// the rendezvous operator offers it. The relay is deliberately dumb: one
// UDP socket per session that passes datagrams between two addresses.
//
// The two addresses are the ones that sent it a valid bind packet, one per
// role. A bind is authenticated with a key derived from the session nonce
// the rendezvous server gave to exactly these two peers, so nobody else can
// attach to the session, and since nothing is forwarded to an address that
// did not bind (with a return packet no larger than its own), the relay
// cannot be pointed at third parties. The relay acknowledges a bind with a
// MAC under the same key.
//
// What is relayed is the peers' ordinary QUIC connection. The relay sees
// ciphertext and can do to it what any router on the path could: drop,
// delay, or try to sit in the middle, which the login handshake defeats
// exactly as it does for a direct path (see internal/auth).
//
//	bind (40 bytes)      type(1)=0x05  magic(3)  role(1)  zero(19)  mac(16)
//	bind-ack (20 bytes)  type(1)=0x06  magic(3)  mac(16)
//	release (40 bytes)   type(1)=0x07  magic(3)  role(1)  zero(19)  mac(16)
//
// A peer whose session is over sends release, so the relay frees the
// session at once instead of after its idle timeout (which matters where
// the operator caps concurrent sessions per address).
//
// Only bound peers keep a session alive: packets from anyone else neither
// pass nor count as activity, so whoever finds the port cannot hold the
// session (and its slot) open. A bind is a pure function of the nonce and
// the role, so an observer on the path could replay one from another
// address; a bind therefore moves a role to a new address only once its
// current address has been silent for relayMoveQuiet, which a peer that is
// still talking never is.
const (
	pktRelayBind    = 0x05
	pktRelayAck     = 0x06
	pktRelayRelease = 0x07

	relayBindLen = 40
	relayAckLen  = 20

	// relayMoveQuiet: see above.
	relayMoveQuiet = 2 * time.Second
)

func relayKey(nonce []byte) []byte { return derive(nonce, "remohop relay key") }

func relayMAC(key []byte, label byte, body []byte) []byte {
	m := hmac.New(sha256New, key)
	m.Write([]byte{label})
	m.Write(body)
	return m.Sum(nil)[:16]
}

func relayBind(key []byte, role Role) []byte {
	p := make([]byte, relayBindLen)
	putHeader(p, pktRelayBind)
	p[4] = byte(role)
	copy(p[24:], relayMAC(key, 'B', p[:24]))
	return p
}

func relayRelease(key []byte, role Role) []byte {
	p := make([]byte, relayBindLen)
	putHeader(p, pktRelayRelease)
	p[4] = byte(role)
	copy(p[24:], relayMAC(key, 'R', p[:24]))
	return p
}

func relayAck(key []byte) []byte {
	p := make([]byte, relayAckLen)
	putHeader(p, pktRelayAck)
	copy(p[4:], relayMAC(key, 'A', p[:4]))
	return p
}

// RelayLimits bounds what one relayed session may cost the operator.
type RelayLimits struct {
	// Rate is the session's budget in bytes per second (both directions
	// together). Traffic above it is paced: held back for up to MaxDelay
	// and then dropped, like at any bottleneck router with a short queue.
	// The queueing delay and the occasional tail drop are signals the
	// peers' QUIC congestion control handles well; dropping every packet
	// over the rate at once (a policer) makes it collapse to a fraction
	// of the rate.
	Rate int
	// Global, if not nil, is a budget shared with other sessions; it is
	// paced the same way.
	Global *ByteBudget
	// Idle ends a session that has passed nothing for this long.
	Idle time.Duration
	// Meter, if set, is told the size of every relayed packet.
	Meter func(n int)
	// MaxDelay is the longest a packet is held back. Default 100 ms;
	// negative drops instead of delaying (a policer with a one-second
	// bucket, which is how the relay used to work).
	MaxDelay time.Duration
}

// ByteBudget is a token bucket of bytes, safe for concurrent use. It can
// police (Take) or pace (reserve: go into debt for up to a given delay).
type ByteBudget struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	at     time.Time
}

// NewByteBudget allows rate bytes per second, with bursts of one second's
// worth.
func NewByteBudget(rate int) *ByteBudget { return newByteBudget(rate, rate) }

func newByteBudget(rate, burst int) *ByteBudget {
	return &ByteBudget{rate: float64(rate), burst: float64(burst), tokens: float64(burst), at: time.Now()}
}

// Take spends n bytes if the budget has them and reports whether it did.
// A nil budget always has them.
func (b *ByteBudget) Take(n int) bool { return b.take(n) }

func (b *ByteBudget) take(n int) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(time.Now())
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

func (b *ByteBudget) refill(now time.Time) {
	b.tokens = min(b.burst, b.tokens+now.Sub(b.at).Seconds()*b.rate)
	b.at = now
}

// reserve spends n bytes, going into debt if need be, and returns how long
// the packet must wait for the debt to be paid off. If that is longer than
// maxDelay nothing is spent and ok is false. A nil budget never waits.
func (b *ByteBudget) reserve(now time.Time, n int, maxDelay time.Duration) (wait time.Duration, ok bool) {
	if b == nil {
		return 0, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now)
	after := b.tokens - float64(n)
	if after >= 0 {
		b.tokens = after
		return 0, true
	}
	wait = time.Duration(-after / b.rate * float64(time.Second))
	if wait > maxDelay {
		return 0, false
	}
	b.tokens = after
	return wait, true
}

// refund gives back n bytes taken by reserve.
func (b *ByteBudget) refund(n int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.tokens = min(b.burst, b.tokens+float64(n))
	b.mu.Unlock()
}

// RelayStats is what a finished relay session passed on.
type RelayStats struct {
	// Packets and Bytes were relayed; Delayed of the packets were paced;
	// Dropped were not relayed for want of budget.
	Packets, Bytes, Delayed, Dropped int64
}

// relayQueueLen bounds the packets a session holds back. At 2 Mbit/s and
// 1200-byte packets 100 ms is about 20 of them; at 50 Mbit/s about 500.
const relayQueueLen = 1024

type pacedPacket struct {
	at  time.Time
	p   []byte
	to  *net.UDPAddr
	len int
}

// pacer sends held-back packets of one session at their time, in order.
type pacer struct {
	pc      net.PacketConn
	meter   func(int)
	ch      chan pacedPacket
	pending atomic.Int64
	done    chan struct{}
	stop    chan struct{}
	packets atomic.Int64
	bytes   atomic.Int64
}

func newPacer(pc net.PacketConn, meter func(int)) *pacer {
	q := &pacer{pc: pc, meter: meter, ch: make(chan pacedPacket, relayQueueLen), done: make(chan struct{}), stop: make(chan struct{})}
	go q.run()
	return q
}

func (q *pacer) run() {
	defer close(q.done)
	t := time.NewTimer(time.Hour)
	t.Stop()
	for {
		var pp pacedPacket
		select {
		case pp = <-q.ch:
		case <-q.stop:
			return
		}
		if d := time.Until(pp.at); d > 0 {
			t.Reset(d)
			select {
			case <-t.C:
			case <-q.stop:
				t.Stop()
				return
			}
		}
		if _, err := q.pc.WriteTo(pp.p, pp.to); err == nil {
			q.packets.Add(1)
			q.bytes.Add(int64(pp.len))
			if q.meter != nil {
				q.meter(pp.len)
			}
		}
		q.pending.Add(-1)
	}
}

// push queues a packet; false if the queue is full.
func (q *pacer) push(pp pacedPacket) bool {
	q.pending.Add(1)
	select {
	case q.ch <- pp:
		return true
	default:
		q.pending.Add(-1)
		return false
	}
}

func (q *pacer) close() {
	close(q.stop)
	<-q.done
}

// ServeRelay relays between the two peers that bind to pc with nonce until
// ctx ends or the session has been idle for limits.Idle, then closes pc.
func ServeRelay(ctx context.Context, pc net.PacketConn, nonce []byte, limits RelayLimits) RelayStats {
	defer pc.Close()
	stop := context.AfterFunc(ctx, func() { pc.SetReadDeadline(time.Now()) })
	defer stop()
	if limits.Idle <= 0 {
		limits.Idle = 30 * time.Second
	}
	if limits.MaxDelay == 0 {
		limits.MaxDelay = 100 * time.Millisecond
	}
	maxDelay := max(limits.MaxDelay, 0)
	key := relayKey(nonce)
	ack := relayAck(key)
	rate := max(limits.Rate, 1500)
	// A short burst allowance: the queue, not a deep bucket, absorbs
	// bursts, so that a sender above the rate sees delay early instead of
	// a wall of drops late.
	session := newByteBudget(rate, max(rate/20, 16<<10))
	if limits.MaxDelay < 0 {
		// Plain policing, with the one-second bucket it always had.
		session = newByteBudget(rate, rate)
	}
	q := newPacer(pc, limits.Meter)
	var peers [3]netip.AddrPort // indexed by Role
	var heard [3]time.Time      // when each role's address last sent anything
	var stats RelayStats
	buf := make([]byte, 2048)
	active := time.Now() // the last bind or packet from a bound peer
	for ctx.Err() == nil {
		pc.SetReadDeadline(active.Add(limits.Idle))
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			break // idle, cancelled or closed
		}
		src, ok := addrPortOf(from)
		if !ok {
			continue
		}
		p := buf[:n]
		if n == relayBindLen && hasHeader(p, pktRelayBind) {
			role := Role(p[4])
			if (role == RoleClient || role == RoleHost) && hmac.Equal(p[24:], relayMAC(key, 'B', p[:24])) {
				now := time.Now()
				// A valid bind may also move a peer whose NAT mapping
				// changed, once its old address has gone quiet.
				if !peers[role].IsValid() || peers[role] == src || now.Sub(heard[role]) > relayMoveQuiet {
					peers[role], heard[role], active = src, now, now
					pc.WriteTo(ack, from)
				}
			}
			continue
		}
		if n == relayBindLen && hasHeader(p, pktRelayRelease) {
			role := Role(p[4])
			if (role == RoleClient || role == RoleHost) && src == peers[role] && hmac.Equal(p[24:], relayMAC(key, 'R', p[:24])) {
				// The session is over; let what is queued (its last
				// packets, a connection close among them) go first.
				for deadline := time.Now().Add(maxDelay + 50*time.Millisecond); q.pending.Load() > 0 && time.Now().Before(deadline); {
					time.Sleep(5 * time.Millisecond)
				}
				break
			}
			continue
		}
		var to netip.AddrPort
		now := time.Now()
		switch src {
		case peers[RoleClient]:
			to, heard[RoleClient] = peers[RoleHost], now
		case peers[RoleHost]:
			to, heard[RoleHost] = peers[RoleClient], now
		}
		if to.IsValid() {
			// Live while both sides speak: one side alone (a client that
			// keeps a session up with junk the host never answers) does
			// not hold the relay past Idle.
			active = heard[RoleClient]
			if heard[RoleHost].Before(active) {
				active = heard[RoleHost]
			}
		}
		if !to.IsValid() {
			continue // a stranger, or the other side has not bound yet
		}
		w1, ok := session.reserve(now, n, maxDelay)
		if !ok {
			stats.Dropped++
			continue
		}
		w2, ok := limits.Global.reserve(now, n, maxDelay)
		if !ok {
			session.refund(n)
			stats.Dropped++
			continue
		}
		wait := max(w1, w2)
		dst := net.UDPAddrFromAddrPort(to)
		if wait == 0 && q.pending.Load() == 0 {
			if _, err := pc.WriteTo(p, dst); err == nil {
				stats.Packets++
				stats.Bytes += int64(n)
				if limits.Meter != nil {
					limits.Meter(n)
				}
			}
			continue
		}
		if !q.push(pacedPacket{at: now.Add(wait), p: bytes.Clone(p), to: dst, len: n}) {
			session.refund(n)
			limits.Global.refund(n)
			stats.Dropped++
			continue
		}
		stats.Delayed++
	}
	q.close()
	stats.Packets += q.packets.Load()
	stats.Bytes += q.bytes.Load()
	return stats
}

// BindRelay attaches this socket to the relay session at relay, as role.
// It returns once the relay has acknowledged with proof that it holds the
// session nonce.
func (s *Socket) BindRelay(ctx context.Context, relay netip.AddrPort, nonce []byte, role Role) error {
	if len(nonce) != NonceSize {
		return errors.New("nat: bad session nonce")
	}
	relay = normalize(relay)
	key := relayKey(nonce)
	w := relayWait{ack: relayAck(key), ch: make(chan struct{}, 1)}
	s.mu.Lock()
	if _, busy := s.relayWaits[relay]; busy {
		s.mu.Unlock()
		return errors.New("nat: already binding to this relay")
	}
	s.relayWaits[relay] = w
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.relayWaits, relay)
		s.mu.Unlock()
	}()

	bind := relayBind(key, role)
	interval := 100 * time.Millisecond
	for {
		if err := s.send(bind, relay); err != nil {
			return err
		}
		t := time.NewTimer(interval)
		select {
		case <-w.ch:
			t.Stop()
			return nil
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("the relay at %s did not answer: %w", relay, ctx.Err())
		case <-s.done:
			t.Stop()
			return net.ErrClosed
		case <-t.C:
			interval = min(interval*2, time.Second)
		}
	}
}

// ReleaseRelay tells the relay at relay that this socket's session (of
// nonce, as role) is over, so that it is freed at once. Best effort: the
// relay ends idle sessions by itself anyway.
func (s *Socket) ReleaseRelay(relay netip.AddrPort, nonce []byte, role Role) {
	if len(nonce) != NonceSize {
		return
	}
	p := relayRelease(relayKey(nonce), role)
	relay = normalize(relay)
	s.send(p, relay)
	s.send(p, relay)
}

type relayWait struct {
	ack []byte
	ch  chan struct{}
}
