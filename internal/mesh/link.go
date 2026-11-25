package mesh

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// # Wire protocol (service "net")
//
// The device that wants a link logs in to the other's host (with its
// identity key: the other admits account members for this service only)
// and opens a stream with a hello as request. The other answers with its
// own hello; a hello with Error refuses the link. Then both sides send, on
// the stream, frames of a 2-byte big-endian length and a payload:
//
//   - length 0: keep-alive, every few seconds;
//   - a payload starting with '{': a control message (JSON), such as a
//     newer policy;
//   - anything else: an IP packet.
//
// IP packets go in datagrams on the session (peer.DatagramMesh) instead
// when they fit. One link per pair of devices; when both open one at the
// same moment, the one opened by the device with the smaller identity key
// stays.

// Service is the host service the mesh runs on.
const Service = "net"

const (
	protoVersion = 1
	tunnelMTU    = 1400
	minMSS       = 536
	maxMSS       = tunnelMTU - 40
	maxHello     = 64 << 10
	// queueLen bounds the packets held for a peer while its link comes up.
	queueLen = 64
	// sendQueueLen bounds the packets waiting to go out on a link. The
	// interface is read by one loop for all peers: it hands each packet to
	// the link's queue and moves on, so a peer that stops draining (lid
	// closed mid-transfer) loses its own packets instead of freezing
	// everyone else's.
	sendQueueLen = 256
)

// Timing, variables for tests.
var (
	keepAlive   = 4 * time.Second
	deadAfter   = 15 * time.Second
	idleAfter   = 3 * time.Minute
	dialTimeout = 30 * time.Second
)

// hello introduces a device to its peer.
type hello struct {
	V     int    `json:"v"`
	Error string `json:"error,omitempty"`
	Name  string `json:"name,omitempty"`
	Addr4 string `json:"addr4,omitempty"`
	Addr6 string `json:"addr6,omitempty"`
	// Routes are networks this device forwards to for the mesh (subnet
	// router); Exit says it forwards anywhere (exit node).
	Routes []string `json:"routes,omitempty"`
	Exit   bool     `json:"exit,omitempty"`
	// MaxDatagram is the largest packet the sender can put in a datagram
	// right now (0: none).
	MaxDatagram int           `json:"max_datagram,omitempty"`
	Policy      *SignedPolicy `json:"policy,omitempty"`
}

// control is a message on an established link.
type control struct {
	Policy *SignedPolicy `json:"policy,omitempty"`
}

// link is an established tunnel to one peer.
type link struct {
	n      *Node
	key    string // the peer's identity key, raw
	label  string
	conn   *peer.Conn
	s      peer.Stream
	d      *peer.DatagramChannel
	dgram  bool // both sides take datagrams
	mss    uint16
	dialer bool // this device opened it
	// the peer's offer
	routes []netip.Prefix
	exit   bool
	path   string
	remote netip.AddrPort
	since  time.Time
	pinned atomic.Bool // kept up although idle (exit node, routes)

	heard    atomic.Int64 // unix nanos of the last frame from the peer
	active   atomic.Int64 // unix nanos of the last IP packet either way
	out      chan []byte  // packets to send; see sendQueueLen
	wmu      sync.Mutex
	done     chan struct{}
	once     sync.Once
	closeErr atomic.Value

	tx, rx, txBytes, rxBytes, dropped atomic.Int64
}

// datagrams returns the session's mesh datagram channel and the largest
// packet it takes, or (nil, 0).
func datagrams(c *peer.Conn) (*peer.DatagramChannel, int) {
	d, err := c.Datagrams(peer.DatagramMesh)
	if err != nil {
		return nil, 0
	}
	var tooLarge *quic.DatagramTooLargeError
	if err := d.Send(make([]byte, 1<<16)); errors.As(err, &tooLarge) {
		if n := int(tooLarge.MaxDatagramPayloadSize) - peer.DatagramOverhead; n > 0 {
			return d, n
		}
	}
	return nil, 0
}

func pickMSS(a, b int) uint16 {
	if a <= 0 || b <= 0 {
		return maxMSS
	}
	return uint16(max(minMSS, min(min(a, b)-40, maxMSS)))
}

func remoteOf(c *peer.Conn) netip.AddrPort {
	if ua, ok := c.RemoteAddr().(*net.UDPAddr); ok {
		ap := ua.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}

func newLink(n *Node, key []byte, label string, c *peer.Conn, s peer.Stream, dialer bool, own, theirs int, h *hello) *link {
	l := &link{n: n, key: string(key), label: label, conn: c, s: s, dialer: dialer, mss: pickMSS(own, theirs),
		remote: remoteOf(c), since: time.Now(), done: make(chan struct{}), out: make(chan []byte, sendQueueLen)}
	l.d, _ = datagrams(c)
	l.dgram = l.d != nil && own > 0 && theirs > 0
	l.exit = h.Exit
	for _, r := range h.Routes {
		if p, err := netip.ParsePrefix(r); err == nil && p == p.Masked() && p.Addr().Is4() && p.Bits() > 0 && len(l.routes) < 32 {
			l.routes = append(l.routes, p)
		}
	}
	now := time.Now().UnixNano()
	l.heard.Store(now)
	l.active.Store(now)
	return l
}

// errQueueFull is a packet dropped because the link's queue is full.
var errQueueFull = errors.New("the link's send queue is full")

// send queues one IP packet for the link's sender (sendLoop) and returns at
// once; pkt may be reused when it returns. A full queue drops the packet,
// as a congested network would.
func (l *link) send(pkt []byte) error {
	if l.closed() {
		return net.ErrClosed
	}
	select {
	case l.out <- bytes.Clone(pkt):
		return nil
	default:
		return errQueueFull
	}
}

// sendLoop sends the queued packets until the link ends. It runs apart from
// run's other goroutines: a send blocked on a peer that stopped draining
// must not hold up the link's end (it returns when the session closes).
func (l *link) sendLoop() {
	for {
		select {
		case <-l.done:
			return
		case pkt := <-l.out:
			if err := l.transmit(pkt); err != nil {
				l.dropped.Add(1)
			}
		}
	}
}

// transmit sends one IP packet now: in a datagram if it fits, else as a
// frame on the stream.
func (l *link) transmit(pkt []byte) error {
	if l.dgram {
		err := l.d.Send(pkt)
		if err == nil {
			l.tx.Add(1)
			l.txBytes.Add(int64(len(pkt)))
			l.active.Store(time.Now().UnixNano())
			return nil
		}
		var tooLarge *quic.DatagramTooLargeError
		if !errors.As(err, &tooLarge) && !errors.Is(err, peer.ErrNoDatagrams) {
			return err
		}
	}
	if err := l.frame(pkt); err != nil {
		return err
	}
	l.tx.Add(1)
	l.txBytes.Add(int64(len(pkt)))
	l.active.Store(time.Now().UnixNano())
	return nil
}

// frame writes one frame on the stream.
func (l *link) frame(p []byte) error {
	if len(p) > 0xffff {
		return errors.New("frame too large")
	}
	buf := make([]byte, 2+len(p))
	binary.BigEndian.PutUint16(buf, uint16(len(p)))
	copy(buf[2:], p)
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.s.SetWriteDeadline(time.Now().Add(deadAfter))
	_, err := l.s.Write(buf)
	return err
}

func (l *link) sendControl(c control) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return l.frame(b)
}

// close ends the link; the session too when this side opened it.
func (l *link) close(why error) {
	l.once.Do(func() {
		if why != nil {
			l.closeErr.Store(why.Error())
		}
		close(l.done)
		l.s.Reset()
		if l.dialer {
			l.conn.CloseWithReason("mesh link closed")
		}
	})
}

func (l *link) closed() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// run carries packets until the link ends, and returns why.
func (l *link) run(ctx context.Context) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go l.sendLoop()
	var wg sync.WaitGroup
	// stream frames
	wg.Go(func() {
		var hdr [2]byte
		buf := make([]byte, 65536)
		for {
			if _, err := io.ReadFull(l.s, hdr[:]); err != nil {
				if errors.Is(err, io.EOF) {
					err = errors.New("the peer closed the link")
				}
				cancel(err)
				return
			}
			k := int(binary.BigEndian.Uint16(hdr[:]))
			if _, err := io.ReadFull(l.s, buf[:k]); err != nil {
				cancel(err)
				return
			}
			l.heard.Store(time.Now().UnixNano())
			switch {
			case k == 0:
			case buf[0] == '{':
				var c control
				if json.Unmarshal(buf[:k], &c) == nil {
					l.n.onControl(l, c)
				}
			default:
				l.n.deliver(l, buf[:k])
			}
		}
	})
	if l.d != nil {
		wg.Go(func() {
			for {
				p, err := l.d.Receive(ctx)
				if err != nil {
					cancel(err)
					return
				}
				l.heard.Store(time.Now().UnixNano())
				l.n.deliver(l, p)
			}
		})
	}
	// keep-alive, liveness and idleness
	wg.Go(func() {
		t := time.NewTicker(keepAlive)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.done:
				cancel(errors.New("closed"))
				return
			case <-l.conn.Done():
				cancel(errors.New("the session to the peer ended"))
				return
			case <-t.C:
			}
			if time.Since(time.Unix(0, l.heard.Load())) > deadAfter {
				cancel(errors.New("the peer stopped answering"))
				return
			}
			if !l.pinned.Load() && time.Since(time.Unix(0, l.active.Load())) > idleAfter {
				cancel(errIdle)
				return
			}
			if err := l.frame(nil); err != nil {
				cancel(err)
				return
			}
		}
	})
	<-ctx.Done()
	err := context.Cause(ctx)
	l.close(err)
	wg.Wait()
	return err
}

var errIdle = errors.New("idle")

// helloFor builds this device's hello.
func (n *Node) helloFor(c *peer.Conn) (*hello, int) {
	v := n.currentView()
	_, own := datagrams(c)
	h := &hello{V: protoVersion, Name: v.self.Label, Addr4: v.self.Addr4.String(), MaxDatagram: own, Exit: n.cfg.exit, Policy: v.signed}
	if v.self.Addr6.IsValid() {
		h.Addr6 = v.self.Addr6.String()
	}
	for _, r := range n.cfg.routes {
		h.Routes = append(h.Routes, r.String())
	}
	return h, own
}

// checkHello verifies that the peer's hello matches what this device
// computed for its key.
func checkHello(v *view, key []byte, h *hello) (*peerInfo, error) {
	if h.V != protoVersion {
		return nil, fmt.Errorf("unsupported mesh protocol version %d", h.V)
	}
	p, ok := v.byKey[string(key)]
	if !ok || p.Self {
		return nil, fmt.Errorf("key %s is not a device of this mesh", auth.Fingerprint(key))
	}
	if h.Addr4 != p.Addr4.String() || (h.Addr6 != "" && p.Addr6.IsValid() && h.Addr6 != p.Addr6.String()) {
		return nil, fmt.Errorf("the two devices disagree about %s's mesh address (%s here, %s there): "+
			"their account device lists differ; run `remohop account sync` on both", p.Label, p.Addr4, auth.CleanText(h.Addr4, 64))
	}
	return p, nil
}

// dialLink opens a link to p.
func (n *Node) dialLink(ctx context.Context, p *peerInfo) (*link, error) {
	if p.ID == "" {
		return nil, fmt.Errorf("%s has never registered as a host (it must run `remohop host` or the host service)", p.Label)
	}
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, err := n.dialPeer(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			c.CloseWithReason("mesh link failed")
		}
	}()
	h, own := n.helloFor(c)
	s, err := c.Open(ctx, Service, h)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, s.Reset)
	var rep hello
	err = readHello(s, &rep)
	if !stop() {
		err = ctx.Err()
	}
	if err != nil {
		s.Reset()
		return nil, err
	}
	if rep.Error != "" {
		s.Close()
		return nil, &refusedError{auth.CleanText(rep.Error, 300)}
	}
	if _, err := checkHello(n.currentView(), p.Key, &rep); err != nil {
		s.Reset()
		return nil, err
	}
	l := newLink(n, p.Key, p.Label, c, s, true, own, rep.MaxDatagram, &rep)
	l.path = n.pathOf(c)
	n.offerPolicy(l, rep.Policy)
	ok = true
	return l, nil
}

type refusedError struct{ msg string }

func (e *refusedError) Error() string { return "refused by the peer: " + e.msg }

func readHello(s peer.Stream, h *hello) error { return wire.ReadJSONLimit(s, h, maxHello) }

func writeHello(s peer.Stream, h *hello) error { return wire.WriteJSON(s, h) }
