package forward

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// ErrSessionClosed is the Err of a Forwarder whose session ended.
var ErrSessionClosed = errors.New("session closed")

// Dial opens a connection to addr (host:port) as seen from the host and
// returns it as a net.Conn. network is "tcp", "tcp4" or "tcp6". The returned
// connection also has a CloseWrite method for half-close. A refused or
// unreachable target is reported as an error.
func Dial(ctx context.Context, conn *peer.Conn, network, addr string) (net.Conn, error) {
	return dialStream(ctx, conn, network, addr)
}

func dialStream(ctx context.Context, conn *peer.Conn, network, addr string) (*streamConn, error) {
	if err := checkNetwork(network); err != nil {
		return nil, err
	}
	s, err := conn.Open(ctx, svcDial, dialReq{Network: network, Addr: addr})
	if err != nil {
		return nil, err
	}
	if err := readStatus(ctx, s, &status{}); err != nil {
		s.Reset()
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("forward to %s: %w", addr, err)
	}
	return newStreamConn(s, network, addr), nil
}

// Stats is a snapshot of a Forwarder's counters.
type Stats struct {
	Connections uint64 // connections accepted so far
	Active      int64  // connections open now
	BytesUp     int64  // from the accepted connection towards the target
	BytesDown   int64  // from the target back to the accepted connection
}

// Forwarder is a running local or reverse forward.
type Forwarder struct {
	spec    Spec
	reverse bool
	addr    net.Addr
	conn    *peer.Conn
	id      string // reverse only

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	conns  atomic.Uint64
	active atomic.Int64
	up     atomic.Int64
	down   atomic.Int64
}

// Spec is the spec the forwarder was started with. For port 0 the actual
// port is in Addr.
func (f *Forwarder) Spec() Spec { return f.spec }

// Reverse reports whether the host listens (-R) rather than the client.
func (f *Forwarder) Reverse() bool { return f.reverse }

// Addr is the address being listened on: on the client for a local forward,
// on the host for a reverse one.
func (f *Forwarder) Addr() net.Addr { return f.addr }

// Target is the address connections are carried to.
func (f *Forwarder) Target() string { return f.spec.Target() }

// Done is closed once the forwarder has stopped and released its resources.
func (f *Forwarder) Done() <-chan struct{} { return f.done }

// Err says why the forwarder stopped: nil after Close, the context's error
// after cancellation, ErrSessionClosed when the session died, or the failure
// that killed the listener. It is nil while the forwarder is running.
func (f *Forwarder) Err() error {
	select {
	case <-f.done:
	default:
		return nil
	}
	cause := context.Cause(f.ctx)
	if errors.Is(cause, errClosed) {
		return nil
	}
	return cause
}

var errClosed = errors.New("closed")

// Stats returns the current counters.
func (f *Forwarder) Stats() Stats {
	return Stats{f.conns.Load(), f.active.Load(), f.up.Load(), f.down.Load()}
}

// Close stops listening, aborts open connections and waits for everything to
// wind down. It is safe to call more than once.
func (f *Forwarder) Close() error {
	f.cancel(errClosed)
	<-f.done
	return nil
}

func newForwarder(ctx context.Context, conn *peer.Conn, spec Spec, reverse bool) *Forwarder {
	f := &Forwarder{spec: spec, reverse: reverse, conn: conn, done: make(chan struct{})}
	f.ctx, f.cancel = context.WithCancelCause(ctx)
	context.AfterFunc(conn.Context(), func() { f.cancel(ErrSessionClosed) })
	return f
}

// Limits on host-initiated connections to the client's reverse targets, so a
// misbehaving host cannot make the client dial without bound. The session
// cap is below peer.MaxStreams so reverse traffic cannot use every stream.
const (
	maxReversePerForward = 256
	maxReversePerSession = 512
)

// enter registers one in-flight connection; it fails once shutdown began or
// when limit connections are already active.
func (f *Forwarder) enter(limit int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.active.Load() >= limit {
		return false
	}
	f.wg.Add(1)
	f.active.Add(1)
	f.conns.Add(1)
	return true
}

func (f *Forwarder) exit() {
	f.active.Add(-1)
	f.wg.Done()
}

// supervise runs until the forwarder's context ends, then releases cleanup
// (listener, control stream), waits for in-flight connections and signals
// Done.
func (f *Forwarder) supervise(cleanup func()) {
	<-f.ctx.Done()
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	cleanup()
	f.wg.Wait()
	close(f.done)
}

// Listen starts a local forward: the client listens on spec's bind address
// and every connection is tunnelled to spec's target as seen from the host.
// The forward runs until Close, ctx is cancelled or the session ends.
func Listen(ctx context.Context, conn *peer.Conn, spec Spec) (*Forwarder, error) {
	ln, err := net.Listen("tcp", spec.Bind())
	if err != nil {
		return nil, err
	}
	f := newForwarder(ctx, conn, spec, false)
	f.addr = ln.Addr()
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		acceptLoop(f.ctx, ln, &f.wg, f.handleLocal)
		f.cancel(fmt.Errorf("listener %s failed", ln.Addr()))
	}()
	go f.supervise(func() { ln.Close() })
	return f, nil
}

func (f *Forwarder) handleLocal(nc net.Conn) {
	// acceptLoop already counted this goroutine in wg; mirror enter's stats.
	f.active.Add(1)
	f.conns.Add(1)
	defer f.active.Add(-1)
	te := tcpEnd{nc}
	dctx, cancel := context.WithTimeout(f.ctx, 2*dialTimeout)
	sc, err := dialStream(dctx, f.conn, "tcp", f.spec.Target())
	cancel()
	if err != nil {
		slog.Debug("forward: tunnel failed", "target", f.spec.Target(), "err", err)
		te.Abort()
		return
	}
	stop := context.AfterFunc(f.ctx, func() { sc.Abort(); te.Abort() })
	defer stop()
	pipe(te, sc, &f.up, &f.down)
}

// ListenReverse starts a reverse forward: the host listens on spec's bind
// address and every connection is carried back to the client, which dials
// spec's target. Addr is the address the host is listening on.
func ListenReverse(ctx context.Context, conn *peer.Conn, spec Spec) (*Forwarder, error) {
	var idb [12]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return nil, err
	}
	f := newForwarder(ctx, conn, spec, true)
	f.id = hex.EncodeToString(idb[:])
	reg := registryFor(conn)
	reg.add(f.id, f)

	fail := func(err error) (*Forwarder, error) {
		reg.remove(f.id)
		f.cancel(errClosed)
		close(f.done)
		return nil, err
	}
	s, err := conn.Open(ctx, svcListen, listenReq{Network: "tcp", Bind: spec.Bind(), ID: f.id})
	if err != nil {
		return fail(err)
	}
	var rep listenReply
	if err := readStatus(ctx, s, &rep); err != nil {
		s.Reset()
		return fail(fmt.Errorf("host cannot listen on %s: %w", spec.Bind(), err))
	}
	// Parse as a literal only: ResolveTCPAddr would do a DNS lookup of a
	// name the host chose.
	ap, err := netip.ParseAddrPort(rep.Addr)
	if err != nil {
		s.Reset()
		return fail(fmt.Errorf("host sent bad listen address %q", cli.Sanitize(rep.Addr)))
	}
	f.addr = net.TCPAddrFromAddrPort(ap)
	// The host never writes on the control stream; EOF or an error means it
	// gave up the listener.
	go func() {
		// A read error means the session or stream broke; a clean EOF means
		// the host deliberately dropped the listener.
		if _, err := io.Copy(io.Discard, s); err != nil || conn.Context().Err() != nil {
			f.cancel(ErrSessionClosed)
			return
		}
		f.cancel(errors.New("host closed the listener"))
	}()
	go f.supervise(func() {
		reg.remove(f.id)
		s.Close()
	})
	return f, nil
}

// registry maps reverse-forward ids to their forwarders so the host's
// "forward.accept" callbacks reach the right target. Only ids this client
// registered are served: the host cannot make the client dial anything else.
type registry struct {
	mu     sync.Mutex
	m      map[string]*Forwarder
	active atomic.Int64 // reverse connections open in this session
}

type registryKey struct{}

func registryFor(conn *peer.Conn) *registry {
	r, loaded := conn.LoadOrStoreValue(registryKey{}, &registry{m: map[string]*Forwarder{}})
	reg := r.(*registry)
	if !loaded {
		conn.HandleLocal(svcAccept, reg.serve)
	}
	return reg
}

func (r *registry) add(id string, f *Forwarder) {
	r.mu.Lock()
	r.m[id] = f
	r.mu.Unlock()
}

func (r *registry) remove(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

func (r *registry) get(id string) *Forwarder {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

// serve handles one connection the host accepted for a reverse forward.
func (r *registry) serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req acceptReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, status{Error: "bad request"})
		return err
	}
	f := r.get(req.ID)
	if f == nil {
		wire.WriteJSON(s, status{Error: "no such reverse forward"})
		return errors.New("unknown reverse forward id")
	}
	if r.active.Add(1) > maxReversePerSession {
		r.active.Add(-1)
		wire.WriteJSON(s, status{Error: "too many concurrent connections in this session"})
		return errors.New("reverse session connection limit reached")
	}
	defer r.active.Add(-1)
	if !f.enter(maxReversePerForward) {
		wire.WriteJSON(s, status{Error: "no such reverse forward, or too many concurrent connections"})
		return errors.New("reverse forward closed or at its connection limit")
	}
	defer f.exit()

	d := net.Dialer{Timeout: dialTimeout}
	nc, err := d.DialContext(ctx, "tcp", f.spec.Target())
	if err != nil {
		slog.Debug("forward: reverse target unreachable", "target", f.spec.Target(), "err", err)
		wire.WriteJSON(s, status{Error: err.Error()})
		return err
	}
	if err := wire.WriteJSON(s, status{OK: true}); err != nil {
		nc.Close()
		return err
	}
	te := tcpEnd{nc}
	sc := newStreamConn(s, "tcp", f.spec.Target())
	stop := context.AfterFunc(f.ctx, func() { sc.Abort(); te.Abort() })
	defer stop()
	// For a reverse forward "up" is host-side listener -> client target,
	// i.e. stream -> tcp.
	pipe(sc, te, &f.up, &f.down)
	return nil
}
