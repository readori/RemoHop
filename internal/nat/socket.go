package nat

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// maxSessions bounds concurrent punch sessions on one socket, i.e. how many
// simultaneous connection attempts a host entertains. A session whose punch
// succeeds is over in a couple of seconds; only failing ones run their full
// time.
const maxSessions = 128

// Socket is the one UDP socket a peer does everything on: QUIC (through
// Transport) and the non-QUIC punch and reflect packets handled here.
type Socket struct {
	pc net.PacketConn
	tr *quic.Transport

	// Locals lists the machine's own addresses for candidate gathering and
	// NAT detection. It defaults to InterfaceAddrs; tests on a virtual
	// network replace it before use.
	Locals func() []netip.Addr

	// Excluded reports addresses no punched path may use: neither
	// advertised, nor probed, nor accepted as the source of a probe or an
	// ack. It defaults to the package's Excluded (networks the mesh
	// registered with ExcludeNetworks); tests replace it before use.
	Excluded func(netip.Addr) bool

	cancel context.CancelFunc
	done   chan struct{}

	// ttl sends a packet with a given IP TTL, or is nil where the socket
	// cannot do that per packet.
	ttl func(p []byte, to netip.AddrPort, ttl int) error

	mu         sync.Mutex
	sessions   map[[8]byte]*Session
	reflects   map[[12]byte]reflectWait
	relayWaits map[netip.AddrPort]relayWait
	natType    Type
}

type reflectWait struct {
	server netip.AddrPort
	ch     chan reflection
}

// ttlPacketConn is implemented by packet connections that can send a
// single packet with a chosen TTL (the virtual network's sockets do).
type ttlPacketConn interface {
	WriteToTTL(p []byte, addr net.Addr, ttl int) (int, error)
}

// Listen opens a UDP socket on addr ("" or ":0" for any port on all
// interfaces) and wraps it. A socket on all interfaces is dual-stack and,
// where NAT64Wanted, reaches IPv4 addresses through the network's NAT64
// when there is no IPv4 (see nat64.go).
func Listen(addr string) (*Socket, error) {
	if addr == "" {
		addr = ":0"
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	uc, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	var pc net.PacketConn = uc
	if NAT64Wanted() && (ua.IP == nil || ua.IP.IsUnspecified()) {
		pc = WrapNAT64(uc) // IPv4 literals on IPv6-only networks (nat64.go)
	}
	// Outermost: the NAT64 wrapper acts on "network is unreachable" itself.
	pc = softErrors(pc) // a second without a route is packet loss (softsend.go)
	// quic-go prints a one-time notice through the log package when the
	// kernel refuses large UDP buffers (common in containers). It is
	// tuning advice for server operators, not something a `remohop` user can
	// act on.
	if os.Getenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING") == "" {
		os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	}
	return NewSocket(pc)
}

// NewSocket wraps an existing packet connection, which the Socket then owns.
func NewSocket(pc net.PacketConn) (*Socket, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Socket{
		pc:         pc,
		tr:         &quic.Transport{Conn: pc},
		Locals:     InterfaceAddrs,
		Excluded:   Excluded,
		cancel:     cancel,
		done:       make(chan struct{}),
		sessions:   map[[8]byte]*Session{},
		reflects:   map[[12]byte]reflectWait{},
		relayWaits: map[netip.AddrPort]relayWait{},
		natType:    TypeUnknown,
	}
	if tc, ok := pc.(ttlPacketConn); ok {
		s.ttl = func(p []byte, to netip.AddrPort, ttl int) error {
			_, err := tc.WriteToTTL(p, net.UDPAddrFromAddrPort(to), ttl)
			return err
		}
	} else {
		s.ttl = udpTTLWriter(pc)
	}
	// The transport only starts queueing non-QUIC packets once somebody has
	// asked for one. Ask now, synchronously, so no probe that arrives after
	// NewSocket returns is thrown away.
	primed, stop := context.WithCancel(context.Background())
	stop()
	if _, _, err := s.tr.ReadNonQUICPacket(primed, nil); err != nil && !errors.Is(err, context.Canceled) {
		cancel()
		s.tr.Close()
		pc.Close()
		return nil, err
	}
	go s.readLoop(ctx)
	return s, nil
}

// Transport is the QUIC transport on this socket, for Listen and Dial.
func (s *Socket) Transport() *quic.Transport { return s.tr }

// LocalAddr is the address the socket is bound to.
func (s *Socket) LocalAddr() net.Addr { return s.pc.LocalAddr() }

// Close shuts the socket down, abruptly ending any QUIC connection still
// using it; close those first.
func (s *Socket) Close() error {
	s.cancel()
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.Close()
	}
	s.tr.Close()
	err := s.pc.Close()
	<-s.done
	return err
}

func (s *Socket) readLoop(ctx context.Context) {
	defer close(s.done)
	buf := make([]byte, 256)
	for {
		n, from, err := s.tr.ReadNonQUICPacket(ctx, buf)
		if err != nil {
			return
		}
		src, ok := addrPortOf(from)
		if !ok || n < 4 {
			continue
		}
		p := buf[:n]
		switch p[0] {
		case pktProbe, pktProbeAck:
			if len(p) != probeLen || !hasHeader(p, p[0]) {
				continue
			}
			s.mu.Lock()
			sess := s.sessions[[8]byte(p[4:12])]
			s.mu.Unlock()
			if sess != nil {
				sess.handle(p, src)
			}
		case pktRelayAck:
			s.mu.Lock()
			w, found := s.relayWaits[src]
			s.mu.Unlock()
			if found && hmac.Equal(p, w.ack) {
				select {
				case w.ch <- struct{}{}:
				default:
				}
			}
		case pktReflectResp:
			txid, r, ok := parseReflectResp(p)
			if !ok {
				continue
			}
			s.mu.Lock()
			w, found := s.reflects[txid]
			s.mu.Unlock()
			// Only the server we asked may answer.
			if found && w.server == src {
				select {
				case w.ch <- r:
				default:
				}
			}
		}
	}
}

func (s *Socket) send(p []byte, to netip.AddrPort) error {
	_, err := s.tr.WriteTo(p, net.UDPAddrFromAddrPort(to))
	return err
}

// Candidates returns the socket's local candidates: its port on each
// address in Locals, or just the bound address if the socket is bound to
// one.
func (s *Socket) Candidates() []netip.AddrPort {
	bound, ok := addrPortOf(s.pc.LocalAddr())
	if !ok {
		return nil
	}
	if !bound.Addr().IsUnspecified() {
		if !usableLocal(bound.Addr()) || s.excludes(bound.Addr()) {
			return nil
		}
		return []netip.AddrPort{bound}
	}
	var out []netip.AddrPort
	for _, ip := range s.Locals() {
		if len(out) == MaxCandidates {
			break
		}
		if s.excludes(ip) {
			continue // see exclude.go
		}
		out = append(out, netip.AddrPortFrom(ip, bound.Port()))
	}
	return out
}

// Reflexive asks a reflect server (see ServeReflect) for the public address
// this socket's packets arrive from. The request is retransmitted until an
// answer arrives or ctx ends.
func (s *Socket) Reflexive(ctx context.Context, server netip.AddrPort) (netip.AddrPort, error) {
	r, err := s.reflect(ctx, server)
	return r.addr, err
}

func (s *Socket) reflect(ctx context.Context, server netip.AddrPort) (reflection, error) {
	server = normalize(server)
	pkt, txid := newReflectReq()
	w := reflectWait{server: server, ch: make(chan reflection, 1)}
	s.mu.Lock()
	s.reflects[txid] = w
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.reflects, txid)
		s.mu.Unlock()
	}()

	interval := 100 * time.Millisecond
	for {
		if err := s.send(pkt, server); err != nil {
			return reflection{}, err
		}
		t := time.NewTimer(interval)
		select {
		case r := <-w.ch:
			t.Stop()
			return r, nil
		case <-ctx.Done():
			t.Stop()
			return reflection{}, fmt.Errorf("no answer from reflect server %s: %w", server, ctx.Err())
		case <-s.done:
			t.Stop()
			return reflection{}, net.ErrClosed
		case <-t.C:
			interval = min(interval*2, 2*time.Second)
		}
	}
}

// ProbeNAT classifies the NAT in front of this socket with the help of a
// rendezvous server at server (its main address, where ServeReflectQUIC
// runs), and remembers the answer for NATType. It asks the main port and
// the server's second reflect port what public address its packets come
// from; a different answer means the NAT picks the mapping per destination.
//
// It must run before the socket starts probing peers: a NAT that has seen
// punch traffic for this socket can answer differently afterwards. It can
// run while the rendezvous connection is still being established: the
// second port is almost always the one after the main port, so that is
// asked right away, and the main port's answer says if it is another one.
//
// A NAT that maps per destination address but not per port would be taken
// for a cone here, since both observations come from one server address;
// such NATs are rare and the punch itself is the final judge anyway. When
// either port does not answer before ctx ends the result is TypeUnknown.
func (s *Socket) ProbeNAT(ctx context.Context, server netip.AddrPort) Type {
	server = normalize(server)
	typ := s.probeNAT(ctx, server)
	s.mu.Lock()
	s.natType = typ
	s.mu.Unlock()
	return typ
}

func (s *Socket) probeNAT(ctx context.Context, server netip.AddrPort) Type {
	type result struct {
		r   reflection
		err error
	}
	ask := func(to netip.AddrPort) <-chan result {
		ch := make(chan result, 1)
		go func() {
			r, err := s.reflect(ctx, to)
			ch <- result{r, err}
		}()
		return ch
	}
	guess := netip.AddrPortFrom(server.Addr(), server.Port()+1)
	mainCh, guessCh := ask(server), ask(guess)

	primary := <-mainCh
	if primary.err != nil || primary.r.altPort == 0 {
		return TypeUnknown
	}
	second := guessCh
	if alt := netip.AddrPortFrom(server.Addr(), primary.r.altPort); alt != guess {
		second = ask(alt)
	}
	other := <-second
	if other.err != nil {
		return TypeUnknown
	}
	if other.r.addr != primary.r.addr {
		return TypeSymmetric
	}
	if primary.r.addr.Addr().IsLoopback() {
		return TypeNone
	}
	for _, ip := range s.Locals() {
		if ip == primary.r.addr.Addr() {
			return TypeNone
		}
	}
	return TypeCone
}

// NATType returns the result of the last ProbeNAT, or TypeUnknown.
func (s *Socket) NATType() Type {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.natType
}

// excludes reports whether no punched path may use ip (see Excluded).
func (s *Socket) excludes(ip netip.Addr) bool { return s.Excluded != nil && s.Excluded(ip) }
