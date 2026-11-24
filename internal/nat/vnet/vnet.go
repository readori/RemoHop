// Package vnet is an in-process UDP network with simulated NAT boxes, for
// testing rendezvous and hole punching without real routers.
//
// A Net is the public internet. Hosts attach to it directly (public
// address) or behind a NAT that owns one public address and translates for
// the private hosts on its LAN. Host.Listen returns a net.PacketConn, so
// real protocol stacks (quic-go) run over the simulation unchanged.
//
// The NAT models follow the classic taxonomy:
//
//   - FullCone: endpoint-independent mapping, no inbound filtering.
//   - RestrictedCone: endpoint-independent mapping; inbound allowed only
//     from IP addresses the socket has sent to.
//   - PortRestrictedCone: endpoint-independent mapping; inbound allowed
//     only from exact address:port pairs the socket has sent to.
//   - Symmetric: a fresh public port per destination address:port, inbound
//     allowed only from that destination.
//   - ConntrackCone: a port-restricted cone that, like a Linux router whose
//     input chain accepts unsolicited packets, remembers a packet it had no
//     mapping for. When the inside socket later sends to that packet's
//     source, the remembered flow is in the way and the NAT gives this one
//     destination a fresh public port.
//
// Packets carry a TTL. A NAT forwards a packet only if it arrives with a
// TTL of at least 2 and decrements it; the public internet between any two
// public addresses counts as a configurable number of routers (SetHops)
// that do the same. A packet whose TTL runs out is dropped silently.
//
// It is test support: delivery is immediate (plus optional latency and
// loss), there is no MTU, and mappings never expire.
package vnet

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// NATKind selects a NAT box's mapping and filtering behaviour.
type NATKind int

const (
	FullCone NATKind = iota
	RestrictedCone
	PortRestrictedCone
	Symmetric
	ConntrackCone
)

// defaultTTL is what sockets send with unless told otherwise.
const defaultTTL = 64

func (k NATKind) String() string {
	switch k {
	case FullCone:
		return "full-cone"
	case RestrictedCone:
		return "restricted-cone"
	case PortRestrictedCone:
		return "port-restricted-cone"
	case Symmetric:
		return "symmetric"
	case ConntrackCone:
		return "conntrack-cone"
	}
	return fmt.Sprintf("NATKind(%d)", int(k))
}

// Net is the simulated public internet.
type Net struct {
	mu      sync.Mutex
	hosts   map[netip.Addr]*Host
	nats    map[netip.Addr]*NAT
	latency time.Duration
	loss    float64
	hops    int
}

// New returns an empty network whose public internet is three routers wide.
func New() *Net {
	return &Net{hosts: map[netip.Addr]*Host{}, nats: map[netip.Addr]*NAT{}, hops: 3}
}

// SetHops sets how many routers a packet crosses between two public
// addresses. Zero puts every public address on one segment.
func (n *Net) SetHops(hops int) {
	n.mu.Lock()
	n.hops = hops
	n.mu.Unlock()
}

// SetLatency delays every packet crossing the public internet by d.
func (n *Net) SetLatency(d time.Duration) {
	n.mu.Lock()
	n.latency = d
	n.mu.Unlock()
}

// SetLoss drops each packet crossing the public internet with probability p.
func (n *Net) SetLoss(p float64) {
	n.mu.Lock()
	n.loss = p
	n.mu.Unlock()
}

// Host attaches a machine with a public address.
func (n *Net) Host(ip string) *Host {
	h := &Host{net: n, ip: netip.MustParseAddr(ip), conns: map[uint16]*Conn{}}
	n.mu.Lock()
	n.hosts[h.ip] = h
	n.mu.Unlock()
	return h
}

// NAT attaches a NAT box with the given public address.
func (n *Net) NAT(publicIP string, kind NATKind) *NAT {
	b := &NAT{
		net:      n,
		pub:      netip.MustParseAddr(publicIP),
		kind:     kind,
		lan:      map[netip.Addr]*Host{},
		byKey:    map[mapKey]*mapping{},
		byPort:   map[uint16]*mapping{},
		stray:    map[strayKey]bool{},
		nextPort: 40000,
	}
	n.mu.Lock()
	n.nats[b.pub] = b
	n.mu.Unlock()
	return b
}

// route delivers a packet that is on the public internet.
func (n *Net) route(src, dst netip.AddrPort, data []byte, ttl int) {
	n.mu.Lock()
	host, box := n.hosts[dst.Addr()], n.nats[dst.Addr()]
	latency, loss, hops := n.latency, n.loss, n.hops
	n.mu.Unlock()
	// Each router on the way needs the packet to arrive with TTL >= 2.
	if ttl <= hops {
		return
	}
	if loss > 0 && rand.Float64() < loss {
		return
	}
	deliver := func() {
		switch {
		case host != nil:
			host.deliver(src, dst.Port(), data)
		case box != nil:
			box.inbound(src, dst.Port(), data)
		}
	}
	if latency > 0 {
		time.AfterFunc(latency, deliver)
		return
	}
	deliver()
}

// NAT is one address-translating router.
type NAT struct {
	net  *Net
	pub  netip.Addr
	kind NATKind

	mu       sync.Mutex
	hairpin  bool
	lan      map[netip.Addr]*Host
	byKey    map[mapKey]*mapping
	byPort   map[uint16]*mapping
	stray    map[strayKey]bool // ConntrackCone: unsolicited packets seen
	nextPort uint16
}

// strayKey identifies an unsolicited inbound flow: a remote address that
// sent to one of the NAT's public ports without being allowed in.
type strayKey struct {
	port   uint16
	remote netip.AddrPort
}

type mapKey struct {
	inside netip.AddrPort
	dst    netip.AddrPort // zero for endpoint-independent mappings
}

type mapping struct {
	inside netip.AddrPort
	port   uint16
	// sent records where the inside socket has sent through this mapping:
	// full addresses, and bare IPs (port 0) for address-restricted filtering.
	sent map[netip.AddrPort]bool
}

// SetHairpin controls whether a packet from the LAN addressed to the NAT's
// own public address is looped back inside. Off by default, like many
// consumer routers.
func (b *NAT) SetHairpin(on bool) {
	b.mu.Lock()
	b.hairpin = on
	b.mu.Unlock()
}

// Host attaches a machine to the NAT's LAN with a private address.
func (b *NAT) Host(privateIP string) *Host {
	h := &Host{net: b.net, nat: b, ip: netip.MustParseAddr(privateIP), conns: map[uint16]*Conn{}}
	b.mu.Lock()
	b.lan[h.ip] = h
	b.mu.Unlock()
	return h
}

// outbound translates and forwards a packet leaving the LAN.
func (b *NAT) outbound(src, dst netip.AddrPort, data []byte, ttl int) {
	if ttl < 2 {
		return // expired at this router, before any mapping is made
	}
	ttl--
	b.mu.Lock()
	key := mapKey{inside: src}
	if b.kind == Symmetric {
		key.dst = dst
	}
	m := b.byKey[key]
	if b.kind == ConntrackCone && m != nil && b.stray[strayKey{m.port, dst}] {
		// The translation every other destination gets is taken, for
		// this destination, by the flow the stray packet created.
		key.dst = dst
		m = b.byKey[key]
	}
	if m == nil {
		m = &mapping{inside: src, port: b.nextPort, sent: map[netip.AddrPort]bool{}}
		b.nextPort++
		b.byKey[key] = m
		b.byPort[m.port] = m
	}
	m.sent[dst] = true
	m.sent[netip.AddrPortFrom(dst.Addr(), 0)] = true
	ext := netip.AddrPortFrom(b.pub, m.port)
	hairpin := b.hairpin
	b.mu.Unlock()

	if dst.Addr() == b.pub {
		if hairpin {
			b.inbound(ext, dst.Port(), data)
		}
		return
	}
	b.net.route(ext, dst, data, ttl)
}

// inbound filters and forwards a packet arriving at the public address.
func (b *NAT) inbound(src netip.AddrPort, port uint16, data []byte) {
	b.mu.Lock()
	m := b.byPort[port]
	var host *Host
	if m != nil {
		allowed := false
		switch b.kind {
		case FullCone:
			allowed = true
		case RestrictedCone:
			allowed = m.sent[netip.AddrPortFrom(src.Addr(), 0)]
		case PortRestrictedCone, Symmetric, ConntrackCone:
			allowed = m.sent[src]
		}
		if allowed {
			host = b.lan[m.inside.Addr()]
		}
	}
	if host == nil && b.kind == ConntrackCone {
		b.stray[strayKey{port, src}] = true
	}
	b.mu.Unlock()
	if host != nil {
		host.deliver(src, m.inside.Port(), data)
	}
}

// Host is one machine: a set of UDP sockets on a single address.
type Host struct {
	net *Net
	nat *NAT // nil for hosts on the public internet
	ip  netip.Addr

	mu    sync.Mutex
	conns map[uint16]*Conn
	next  uint16
}

// Addr is the host's own (interface) address.
func (h *Host) Addr() netip.Addr { return h.ip }

// Listen opens a UDP socket on port, or on a free port if port is 0.
func (h *Host) Listen(port int) (*Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := uint16(port)
	if p == 0 {
		for {
			h.next++
			p = 50000 + h.next
			if h.conns[p] == nil {
				break
			}
		}
	}
	if h.conns[p] != nil {
		return nil, fmt.Errorf("vnet: %s:%d already in use", h.ip, p)
	}
	c := &Conn{
		host:   h,
		local:  netip.AddrPortFrom(h.ip, p),
		inbox:  make(chan packet, 2048),
		closed: make(chan struct{}),
		wake:   make(chan struct{}),
	}
	h.conns[p] = c
	return c, nil
}

// MustListen is Listen for test setup code.
func (h *Host) MustListen(port int) *Conn {
	c, err := h.Listen(port)
	if err != nil {
		panic(err)
	}
	return c
}

func (h *Host) deliver(src netip.AddrPort, port uint16, data []byte) {
	h.mu.Lock()
	c := h.conns[port]
	h.mu.Unlock()
	if c == nil {
		return
	}
	select {
	case c.inbox <- packet{src: src, data: data}:
	default: // receive buffer full: drop, like a real socket
	}
}

func (h *Host) send(src, dst netip.AddrPort, data []byte, ttl int) {
	if ttl < 1 {
		return
	}
	if h.nat == nil {
		h.net.route(src, dst, data, ttl)
		return
	}
	// Same LAN: delivered by the switch, never touching the NAT.
	h.nat.mu.Lock()
	sibling := h.nat.lan[dst.Addr()]
	h.nat.mu.Unlock()
	if sibling != nil {
		sibling.deliver(src, dst.Port(), data)
		return
	}
	if dst.Addr().IsPrivate() {
		return // some other LAN's address: unroutable from here
	}
	h.nat.outbound(src, dst, data, ttl)
}

type packet struct {
	src  netip.AddrPort
	data []byte
}

// Conn is a simulated UDP socket.
type Conn struct {
	host  *Host
	local netip.AddrPort
	inbox chan packet

	closeOnce sync.Once
	closed    chan struct{}

	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{} // closed and replaced whenever deadline changes
}

var _ net.PacketConn = (*Conn)(nil)

// ReadFrom implements net.PacketConn.
func (c *Conn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, wake := c.deadline, c.wake
		c.mu.Unlock()

		var timeout <-chan time.Time
		if !deadline.IsZero() {
			d := time.Until(deadline)
			if d <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			t := time.NewTimer(d)
			defer t.Stop()
			timeout = t.C
		}
		select {
		case pkt := <-c.inbox:
			return copy(p, pkt.data), net.UDPAddrFromAddrPort(pkt.src), nil
		case <-c.closed:
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case <-wake:
			// deadline changed; re-evaluate
		}
	}
}

// WriteTo implements net.PacketConn.
func (c *Conn) WriteTo(p []byte, addr net.Addr) (int, error) {
	return c.WriteToTTL(p, addr, defaultTTL)
}

// WriteToTTL sends one packet with the given TTL.
func (c *Conn) WriteToTTL(p []byte, addr net.Addr, ttl int) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, errors.New("vnet: not a UDP address")
	}
	dst := ua.AddrPort()
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	if dst.Addr().Is4() != c.local.Addr().Is4() {
		// The simulated hosts are single-stack.
		return 0, &net.OpError{Op: "write", Net: "udp", Addr: addr, Err: errors.New("network is unreachable")}
	}
	c.host.send(c.local, dst, append([]byte(nil), p...), ttl)
	return len(p), nil
}

// Close implements net.PacketConn.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.host.mu.Lock()
		delete(c.host.conns, c.local.Port())
		c.host.mu.Unlock()
	})
	return nil
}

// LocalAddr implements net.PacketConn.
func (c *Conn) LocalAddr() net.Addr { return net.UDPAddrFromAddrPort(c.local) }

// SetDeadline implements net.PacketConn. Writes never block, so only the
// read deadline has an effect.
func (c *Conn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetReadDeadline implements net.PacketConn.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
	return nil
}

// SetWriteDeadline implements net.PacketConn.
func (c *Conn) SetWriteDeadline(time.Time) error { return nil }

// SetReadBuffer and SetWriteBuffer exist so quic-go does not warn about a
// socket whose buffers it cannot size.
func (c *Conn) SetReadBuffer(int) error  { return nil }
func (c *Conn) SetWriteBuffer(int) error { return nil }
