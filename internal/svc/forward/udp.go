package forward

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// UDP forwarding.
//
// One "forward.udp" stream per forward: request {addr, id, datagrams}, reply
// {ok, error, datagrams}. Every packet carries a 32-bit flow number chosen by
// the client, one per local source address; the host keeps one connected UDP
// socket per flow so replies find their way back. Packets travel as session
// datagrams ([forward id][flow][payload] on the peer.DatagramForwardUDP
// channel) when the session supports them and the packet fits, which keeps
// UDP's no-head-of-line-blocking behaviour;
// anything larger, and everything when datagrams are unavailable, goes as a
// frame ([flow][payload]) on the forward's stream.
const (
	svcUDP = "forward.udp"

	maxUDPFlows  = 1024  // flows per forward, on either end
	maxUDPPacket = 65535 // largest payload accepted from the wire
	udpFrame     = 'P'
	dgramHeader  = 8 // forward id + flow
)

// udpIdle is how long a flow without traffic is kept (nanoseconds). Atomic
// so tests can shorten it while handlers of earlier sessions wind down.
var udpIdle atomic.Int64

func udpIdleTimeout() time.Duration { return time.Duration(udpIdle.Load()) }

// udpDatagrams lets tests force the stream-only transport.
var udpDatagrams atomic.Bool

func init() {
	udpIdle.Store(int64(60 * time.Second))
	udpDatagrams.Store(true)
	perm.Register(svcUDP, perm.Forward)
	peer.Handle(svcUDP, hostUDP)
}

type udpReq struct {
	Addr      string `json:"addr"`
	ID        uint32 `json:"id"`
	Datagrams bool   `json:"datagrams"`
}

type udpReply struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Datagrams bool   `json:"datagrams,omitempty"`
}

// dgramMux shares the session's one forward-UDP datagram channel between
// the UDP forwards on it, dispatching on the forward id that leads every
// datagram. The session layer owns the QUIC datagram queue and hands us only
// the datagrams tagged peer.DatagramForwardUDP.
type dgramMux struct {
	ch *peer.DatagramChannel
	mu sync.RWMutex
	m  map[uint32]func(flow uint32, p []byte)
}

type dgramKey struct{}

// dgramsFor returns the session's datagram mux, or nil when the session
// cannot carry datagrams (peer.ErrNoDatagrams): the stream is used alone.
func dgramsFor(conn *peer.Conn) *dgramMux {
	ch, err := conn.Datagrams(peer.DatagramForwardUDP)
	if err != nil {
		return nil
	}
	v, loaded := conn.LoadOrStoreValue(dgramKey{}, &dgramMux{ch: ch, m: map[uint32]func(uint32, []byte){}})
	m := v.(*dgramMux)
	if !loaded {
		go m.loop(conn.Context())
	}
	return m
}

func (m *dgramMux) loop(ctx context.Context) {
	for {
		b, err := m.ch.Receive(ctx)
		if err != nil {
			return
		}
		if len(b) < dgramHeader {
			continue
		}
		m.mu.RLock()
		h := m.m[binary.BigEndian.Uint32(b)]
		m.mu.RUnlock()
		if h != nil {
			h(binary.BigEndian.Uint32(b[4:]), b[dgramHeader:])
		}
	}
}

// add registers h for id; it fails if the id is taken.
func (m *dgramMux) add(id uint32, h func(uint32, []byte)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.m[id]; dup {
		return false
	}
	m.m[id] = h
	return true
}

func (m *dgramMux) remove(id uint32) {
	m.mu.Lock()
	delete(m.m, id)
	m.mu.Unlock()
}

// udpLink is one end of a UDP forward's transport.
type udpLink struct {
	s   peer.Stream
	wmu sync.Mutex // serialises stream frames
	mux *dgramMux  // nil: stream only
	id  uint32
}

// send carries one packet to the other end.
func (l *udpLink) send(flow uint32, p []byte) error {
	if l.mux != nil {
		buf := make([]byte, dgramHeader+len(p))
		binary.BigEndian.PutUint32(buf, l.id)
		binary.BigEndian.PutUint32(buf[4:], flow)
		copy(buf[dgramHeader:], p)
		// The channel adds its own tag byte (peer.DatagramOverhead) and
		// reports when the result does not fit a QUIC packet.
		err := l.mux.ch.Send(buf)
		var tooLarge *quic.DatagramTooLargeError
		if err == nil {
			return nil
		} else if !errors.As(err, &tooLarge) && !errors.Is(err, peer.ErrNoDatagrams) {
			return err
		}
		// Does not fit, or datagrams are gone: fall through to the stream.
	}
	buf := make([]byte, 4+len(p))
	binary.BigEndian.PutUint32(buf, flow)
	copy(buf[4:], p)
	l.wmu.Lock()
	defer l.wmu.Unlock()
	return wire.WriteFrame(l.s, udpFrame, buf)
}

// readFrames delivers stream-carried packets until the stream ends. It
// returns nil on a clean EOF.
func (l *udpLink) readFrames(deliver func(flow uint32, p []byte)) error {
	var buf []byte
	for {
		typ, p, err := wire.ReadFrame(l.s, buf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if cap(p) <= 4+maxUDPPacket {
			buf = p[:0]
		}
		if typ != udpFrame || len(p) < 4 || len(p) > 4+maxUDPPacket {
			return fmt.Errorf("forward: bad UDP frame (type %d, %d bytes)", typ, len(p))
		}
		deliver(binary.BigEndian.Uint32(p), p[4:])
	}
}

// hostFlow is the host's socket for one client-side source address.
type hostFlow struct {
	c    *net.UDPConn
	last atomic.Int64 // unix nanos of the latest packet either way
}

// hostUDP serves "forward.udp".
func hostUDP(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req udpReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, udpReply{Error: "bad request"})
		return err
	}
	err := checkHostPort(req.Addr)
	var raddr *net.UDPAddr
	if err == nil {
		raddr, err = net.ResolveUDPAddr("udp", req.Addr)
	}
	if err != nil {
		wire.WriteJSON(s, udpReply{Error: err.Error()})
		return err
	}
	link := &udpLink{s: s, id: req.ID}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu    sync.Mutex
		flows = map[uint32]*hostFlow{}
		wg    sync.WaitGroup
	)
	drop := func(id uint32, f *hostFlow) {
		mu.Lock()
		if flows[id] == f {
			delete(flows, id)
		}
		mu.Unlock()
		f.c.Close()
	}
	deliver := func(id uint32, p []byte) {
		mu.Lock()
		f := flows[id]
		if f == nil {
			if len(flows) >= maxUDPFlows || ctx.Err() != nil {
				mu.Unlock()
				return
			}
			uc, err := net.DialUDP("udp", nil, raddr)
			if err != nil {
				mu.Unlock()
				slog.Debug("forward: udp dial failed", "addr", req.Addr, "err", err)
				return
			}
			f = &hostFlow{c: uc}
			flows[id] = f
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer drop(id, f)
				buf := make([]byte, maxUDPPacket)
				for {
					n, err := uc.Read(buf)
					if err != nil {
						// ICMP errors (port unreachable) surface here on a
						// connected socket; they do not end the flow.
						if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
							return
						}
						continue
					}
					f.last.Store(time.Now().UnixNano())
					if link.send(id, buf[:n]) != nil {
						return
					}
				}
			}()
		}
		f.last.Store(time.Now().UnixNano())
		mu.Unlock()
		// A failed send (e.g. EMSGSIZE: macOS caps a datagram at
		// net.inet.udp.maxdgram, 9216 bytes by default) loses this packet
		// only, as the network would; the flow stays.
		if _, err := f.c.Write(p); err != nil {
			slog.Debug("forward: udp send to target failed", "addr", req.Addr, "bytes", len(p), "err", err)
		}
	}

	if req.Datagrams {
		if m := dgramsFor(c); m != nil && m.add(req.ID, deliver) {
			link.mux = m
			defer m.remove(req.ID)
		}
	}
	if err := wire.WriteJSON(s, udpReply{OK: true, Datagrams: link.mux != nil}); err != nil {
		return err
	}

	wg.Add(1)
	go func() { // idle flows
		defer wg.Done()
		t := time.NewTicker(udpIdleTimeout() / 4)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				mu.Lock()
				for _, f := range flows {
					if now.UnixNano()-f.last.Load() > udpIdle.Load() {
						f.c.Close() // its reader removes it
					}
				}
				mu.Unlock()
			}
		}
	}()

	// The stream ending (client closed the forward, or the session died)
	// ends everything.
	err = link.readFrames(deliver)
	cancel()
	mu.Lock()
	for _, f := range flows {
		f.c.Close()
	}
	mu.Unlock()
	wg.Wait()
	return err
}

// UDPStats is a snapshot of a UDPForwarder's counters.
type UDPStats struct {
	Flows       int    // source addresses currently tracked
	PacketsUp   uint64 // local -> remote target (accepted from local senders)
	PacketsDown uint64 // remote target -> local
	BytesUp     uint64
	BytesDown   uint64
	Dropped     uint64 // packets dropped (flow table full, or transport error)
}

type clientFlow struct {
	id   uint32
	addr net.Addr
	last atomic.Int64
}

// UDPForwarder is a running local UDP forward.
type UDPForwarder struct {
	spec Spec
	pc   net.PacketConn
	link *udpLink

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu     sync.Mutex
	bySrc  map[string]*clientFlow
	byID   map[uint32]*clientFlow
	nextID uint32

	datagrams                                bool
	pktUp, pktDown, bytesUp, bytesDown, drop atomic.Uint64
}

// ListenUDP starts a local UDP forward: the client listens on spec's bind
// address and every packet is carried to spec's target as seen from the
// host; replies return to the source address they answer. A source address
// that is silent for a minute is forgotten. The forward runs until Close,
// ctx is cancelled or the session ends.
func ListenUDP(ctx context.Context, conn *peer.Conn, spec Spec) (*UDPForwarder, error) {
	pc, err := net.ListenPacket("udp", spec.Bind())
	if err != nil {
		return nil, err
	}
	f := &UDPForwarder{spec: spec, pc: pc, done: make(chan struct{}),
		bySrc: map[string]*clientFlow{}, byID: map[uint32]*clientFlow{}}
	mux := dgramsFor(conn)
	if !udpDatagrams.Load() {
		mux = nil
	}
	var id uint32
	for mux != nil { // a session-unique id for the datagram demux
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			pc.Close()
			return nil, err
		}
		id = binary.BigEndian.Uint32(b[:])
		if mux.add(id, f.deliver) {
			break
		}
	}
	fail := func(err error) (*UDPForwarder, error) {
		if mux != nil {
			mux.remove(id)
		}
		pc.Close()
		return nil, err
	}
	s, err := conn.Open(ctx, svcUDP, udpReq{Addr: spec.Target(), ID: id, Datagrams: mux != nil})
	if err != nil {
		return fail(err)
	}
	var rep udpReply
	stop := context.AfterFunc(ctx, s.Reset)
	err = wire.ReadJSON(s, &rep)
	if !stop() {
		err = ctx.Err()
	} else if err == nil && !rep.OK {
		err = fmt.Errorf("forward udp to %s: %s", spec.Target(), rep.Error)
	}
	if err != nil {
		s.Reset()
		return fail(err)
	}
	f.link = &udpLink{s: s, id: id}
	if mux != nil && rep.Datagrams {
		f.link.mux, f.datagrams = mux, true
	}

	f.ctx, f.cancel = context.WithCancelCause(ctx)
	stopSess := context.AfterFunc(conn.Context(), func() { f.cancel(ErrSessionClosed) })
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { // packets from local applications
		defer wg.Done()
		f.readLocal()
	}()
	go func() { // stream-carried packets from the host
		defer wg.Done()
		if err := f.link.readFrames(f.deliver); err != nil || conn.Context().Err() != nil {
			f.cancel(ErrSessionClosed)
		} else {
			f.cancel(errors.New("host closed the forward"))
		}
	}()
	go func() { // idle flows
		defer wg.Done()
		t := time.NewTicker(udpIdleTimeout() / 4)
		defer t.Stop()
		for {
			select {
			case <-f.ctx.Done():
				return
			case now := <-t.C:
				f.expire(now)
			}
		}
	}()
	go func() {
		<-f.ctx.Done()
		stopSess()
		pc.Close()
		s.Close()
		if mux != nil {
			mux.remove(id)
		}
		wg.Wait()
		close(f.done)
	}()
	return f, nil
}

func (f *UDPForwarder) readLocal() {
	buf := make([]byte, maxUDPPacket)
	for {
		n, src, err := f.pc.ReadFrom(buf)
		if err != nil {
			if f.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue // e.g. ICMP-induced errors on some platforms
		}
		key := src.String()
		f.mu.Lock()
		fl := f.bySrc[key]
		if fl == nil {
			if len(f.bySrc) >= maxUDPFlows {
				f.mu.Unlock()
				f.drop.Add(1)
				continue
			}
			f.nextID++
			fl = &clientFlow{id: f.nextID, addr: src}
			f.bySrc[key], f.byID[fl.id] = fl, fl
		}
		fl.last.Store(time.Now().UnixNano())
		f.mu.Unlock()
		// Counted before sending so the counters never lag behind a reply.
		f.pktUp.Add(1)
		f.bytesUp.Add(uint64(n))
		if err := f.link.send(fl.id, buf[:n]); err != nil {
			f.drop.Add(1)
		}
	}
}

// deliver writes a packet from the host to the source address of its flow.
// Packets for flows we no longer know are dropped: only addresses that sent
// something recently can be reached through the forward.
func (f *UDPForwarder) deliver(flow uint32, p []byte) {
	f.mu.Lock()
	fl := f.byID[flow]
	f.mu.Unlock()
	if fl == nil {
		f.drop.Add(1)
		return
	}
	fl.last.Store(time.Now().UnixNano())
	f.pktDown.Add(1)
	f.bytesDown.Add(uint64(len(p)))
	if _, err := f.pc.WriteTo(p, fl.addr); err != nil {
		// Too large for this OS, or the source went away: drop the packet,
		// keep the flow.
		f.drop.Add(1)
		slog.Debug("forward: udp send to local client failed", "addr", fl.addr, "bytes", len(p), "err", err)
	}
}

func (f *UDPForwarder) expire(now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, fl := range f.bySrc {
		if now.UnixNano()-fl.last.Load() > udpIdle.Load() {
			delete(f.bySrc, k)
			delete(f.byID, fl.id)
		}
	}
}

// Spec is the spec the forwarder was started with.
func (f *UDPForwarder) Spec() Spec { return f.spec }

// Addr is the local UDP address being listened on.
func (f *UDPForwarder) Addr() net.Addr { return f.pc.LocalAddr() }

// Target is the address packets are carried to.
func (f *UDPForwarder) Target() string { return f.spec.Target() }

// Datagrams reports whether packets travel as QUIC datagrams (when they fit)
// rather than only on the forward's stream.
func (f *UDPForwarder) Datagrams() bool { return f.datagrams }

// Done is closed once the forwarder has stopped and released its resources.
func (f *UDPForwarder) Done() <-chan struct{} { return f.done }

// Err says why the forwarder stopped, like Forwarder.Err.
func (f *UDPForwarder) Err() error {
	select {
	case <-f.done:
	default:
		return nil
	}
	if cause := context.Cause(f.ctx); !errors.Is(cause, errClosed) {
		return cause
	}
	return nil
}

// Stats returns the current counters.
func (f *UDPForwarder) Stats() UDPStats {
	f.mu.Lock()
	n := len(f.bySrc)
	f.mu.Unlock()
	return UDPStats{Flows: n, PacketsUp: f.pktUp.Load(), PacketsDown: f.pktDown.Load(),
		BytesUp: f.bytesUp.Load(), BytesDown: f.bytesDown.Load(), Dropped: f.drop.Load()}
}

// Close stops the forward and waits for it to wind down.
func (f *UDPForwarder) Close() error {
	f.cancel(errClosed)
	<-f.done
	return nil
}
