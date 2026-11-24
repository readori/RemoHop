// Package peer is the session layer every RemoHop feature is built on.
//
// A Conn is one authenticated, end-to-end encrypted P2P session between a
// controlling client and a controlled host. It multiplexes independent
// bidirectional streams; every stream starts with a small JSON header naming
// the service it is for, so each feature (shell, fs, forward, ...) owns its
// own wire protocol on its own streams and never has to coordinate with the
// others.
//
// Establishing the session (rendezvous, UDP hole punching, the password
// handshake) is not this package's job: internal/dial and internal/hostd do
// that and hand a ready Mux to NewConn.
package peer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/wire"
)

// Role says which end of the session this process is.
type Role int

const (
	// RoleClient is the controlling side (the `remohop <ID> ...` CLI, the MCP server).
	RoleClient Role = iota
	// RoleHost is the controlled side (`remohop host`).
	RoleHost
)

func (r Role) String() string {
	if r == RoleHost {
		return "host"
	}
	return "client"
}

// Stream is one bidirectional, reliable, ordered byte stream inside a Conn.
type Stream interface {
	io.Reader
	io.Writer
	// Close ends the stream gracefully: buffered writes are flushed and the
	// peer sees EOF, and our read side is abandoned.
	Close() error
	// CloseWrite half-closes: the peer sees EOF but we can keep reading.
	CloseWrite() error
	// Reset aborts both directions immediately, discarding buffered data.
	Reset()
	// Context is cancelled once our write side is finished: closed or reset
	// by us, or abandoned by the peer. A handler that is not currently
	// reading uses it to notice that the other end has gone away.
	Context() context.Context
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// Mux is the stream multiplexer a Conn runs on. QUICMux adapts a QUIC
// connection; other implementations can proxy a session over a local socket.
type Mux interface {
	OpenStream(ctx context.Context) (Stream, error)
	AcceptStream(ctx context.Context) (Stream, error)
	// Close tears the whole session down.
	Close(reason string) error
	// Context is cancelled when the session ends for any reason.
	Context() context.Context
	LocalAddr() net.Addr
	RemoteAddr() net.Addr
}

// Handler serves one inbound stream. req is the service-specific request the
// opener passed to Conn.Open. The stream is closed when the handler returns;
// ctx is cancelled when the session ends.
type Handler func(ctx context.Context, c *Conn, s Stream, req json.RawMessage) error

// Registry is a set of named services.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Handler{}} }

// Handle registers h under name, replacing any previous handler.
func (r *Registry) Handle(name string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[name] = h
}

func (r *Registry) lookup(name string) Handler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[name]
}

// Names lists the registered services, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for k := range r.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HostServices is the registry of services a host exposes to authenticated
// clients. Service packages add themselves from init().
var HostServices = NewRegistry()

// Handle registers a host service in HostServices.
func Handle(name string, h Handler) { HostServices.Handle(name, h) }

// Options configures NewConn.
type Options struct {
	Role Role
	// RemoteID is the peer's RemoHop ID, or its address for direct sessions.
	RemoteID string
	// Services are the services the remote side may open on us. Hosts pass
	// HostServices. Clients normally leave it nil: a host must never be able
	// to open a shell on the machine controlling it. Clients opt in to
	// individual callbacks with Conn.HandleLocal.
	Services *Registry
	// Authorize, when set, is asked before every stream the remote side
	// opens on Services; a non-nil error refuses the stream with that
	// message. It is how a host applies per-feature permissions. Handlers
	// registered with HandleLocal are not subject to it.
	Authorize func(service string) error
	// OnClose, when set, runs once, synchronously, when Close or
	// CloseWithReason ends the session (after the transport is closed),
	// so it is done before the process can exit. A dialler uses it to
	// release a relay session.
	OnClose func()
}

// Conn is one authenticated session with a remote peer.
type Conn struct {
	mux  Mux
	opts Options

	mu    sync.Mutex
	local map[string]Handler

	values sync.Map
	dgrams datagramMux

	closeOnce sync.Once

	// Inbound stream handlers still running, so that a host shutting down
	// can let them finish their cleanup (WaitHandlers).
	hmu    sync.Mutex
	hcount int
	hidle  chan struct{} // closed when hcount drops to zero
}

// NewConn wraps an already authenticated mux and starts serving inbound
// streams.
func NewConn(m Mux, opts Options) *Conn {
	c := &Conn{mux: m, opts: opts, local: map[string]Handler{}}
	go c.acceptLoop()
	return c
}

// Role reports which end of the session this is.
func (c *Conn) Role() Role { return c.opts.Role }

// RemoteID is the peer's RemoHop ID (or address for direct sessions).
func (c *Conn) RemoteID() string { return c.opts.RemoteID }

// RemoteAddr is the peer's network address on the P2P path.
func (c *Conn) RemoteAddr() net.Addr { return c.mux.RemoteAddr() }

// LocalAddr is our network address on the P2P path.
func (c *Conn) LocalAddr() net.Addr { return c.mux.LocalAddr() }

// Context is cancelled when the session ends.
func (c *Conn) Context() context.Context { return c.mux.Context() }

// Done is closed when the session ends.
func (c *Conn) Done() <-chan struct{} { return c.mux.Context().Done() }

// Close ends the session and every stream on it.
func (c *Conn) Close() error { return c.CloseWithReason("closed") }

// CloseWithReason ends the session, reporting reason to the peer.
func (c *Conn) CloseWithReason(reason string) error {
	err := c.mux.Close(reason)
	if c.opts.OnClose != nil {
		c.closeOnce.Do(c.opts.OnClose)
	}
	return err
}

// HandleLocal registers a service the remote side may open on this specific
// session, on top of Options.Services. It is how a client accepts callbacks
// from the host it is controlling (for example reverse port forwards).
func (c *Conn) HandleLocal(name string, h Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.local[name] = h
}

// SetValue attaches per-session state under key.
func (c *Conn) SetValue(key, value any) { c.values.Store(key, value) }

// Value returns per-session state stored with SetValue.
func (c *Conn) Value(key any) (any, bool) { return c.values.Load(key) }

// LoadOrStoreValue returns the existing value for key, or stores and returns
// value.
func (c *Conn) LoadOrStoreValue(key, value any) (actual any, loaded bool) {
	return c.values.LoadOrStore(key, value)
}

type streamHeader struct {
	Service string          `json:"service"`
	Req     json.RawMessage `json:"req,omitempty"`
}

type streamReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// RemoteError is returned by Open when the remote side refuses the stream.
type RemoteError struct {
	Service string
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("remote refused %q: %s", e.Service, e.Message)
}

// ErrUnknownService is the message a peer answers with when nothing is
// registered under the requested name.
const ErrUnknownService = "unknown service"

// Open starts a new stream for service on the remote peer. req is marshalled
// to JSON and delivered to the remote handler; it may be nil. Open returns
// once the remote side has accepted the stream; after that the stream carries
// whatever protocol the service defines.
func (c *Conn) Open(ctx context.Context, service string, req any) (Stream, error) {
	hdr := streamHeader{Service: service}
	if req != nil {
		raw, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		hdr.Req = raw
	}
	s, err := c.mux.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	// Honour ctx for the header exchange only.
	stop := context.AfterFunc(ctx, s.Reset)
	fail := func(err error) (Stream, error) {
		stop()
		s.Reset()
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, err
	}
	if err := wire.WriteJSON(s, hdr); err != nil {
		return fail(err)
	}
	var rep streamReply
	if err := wire.ReadJSON(s, &rep); err != nil {
		return fail(fmt.Errorf("open %q: %w", service, err))
	}
	if !stop() {
		return fail(ctx.Err())
	}
	if !rep.OK {
		s.Close()
		return nil, &RemoteError{Service: service, Message: rep.Error}
	}
	return s, nil
}

// clientHeaderLimit bounds the stream headers a client accepts from the host
// it controls. The only streams a host opens are small callbacks, and unlike
// the host the client has no reason to trust the other end.
const clientHeaderLimit = 64 << 10

func (c *Conn) headerLimit() int {
	if c.opts.Role == RoleClient {
		return clientHeaderLimit
	}
	return wire.MaxJSON
}

// handler finds the handler for an inbound stream, or the reason to refuse it.
func (c *Conn) handler(name string) (Handler, string) {
	c.mu.Lock()
	h := c.local[name]
	c.mu.Unlock()
	if h != nil {
		return h, ""
	}
	if c.opts.Services != nil {
		h = c.opts.Services.lookup(name)
	}
	if h == nil {
		return nil, ErrUnknownService
	}
	if c.opts.Authorize != nil {
		if err := c.opts.Authorize(name); err != nil {
			return nil, err.Error()
		}
	}
	return h, ""
}

func (c *Conn) acceptLoop() {
	ctx := c.mux.Context()
	for {
		s, err := c.mux.AcceptStream(ctx)
		if err != nil {
			return
		}
		c.handlerStarted()
		go func() {
			defer c.handlerEnded()
			c.serve(ctx, s)
		}()
	}
}

func (c *Conn) handlerStarted() {
	c.hmu.Lock()
	if c.hcount == 0 {
		c.hidle = make(chan struct{})
	}
	c.hcount++
	c.hmu.Unlock()
}

func (c *Conn) handlerEnded() {
	c.hmu.Lock()
	if c.hcount--; c.hcount == 0 {
		close(c.hidle)
	}
	c.hmu.Unlock()
}

// WaitHandlers waits, at most timeout, until every handler of a stream the
// remote side opened has returned, and reports whether they all did. After
// the session ends, handlers still undo what they set up (a USB device
// handed back to its driver, say); a host that is shutting down waits for
// that before the process exits.
func (c *Conn) WaitHandlers(timeout time.Duration) bool {
	c.hmu.Lock()
	if c.hcount == 0 {
		c.hmu.Unlock()
		return true
	}
	idle := c.hidle
	c.hmu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-idle:
		return true
	case <-t.C:
		return false
	}
}

func (c *Conn) serve(ctx context.Context, s Stream) {
	defer s.Close()
	var hdr streamHeader
	s.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := wire.ReadJSONLimit(s, &hdr, c.headerLimit()); err != nil {
		s.Reset()
		return
	}
	s.SetReadDeadline(time.Time{})
	h, refusal := c.handler(hdr.Service)
	if h == nil {
		wire.WriteJSON(s, streamReply{Error: refusal})
		return
	}
	if err := wire.WriteJSON(s, streamReply{OK: true}); err != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("service handler panic", "service", hdr.Service, "panic", r)
			s.Reset()
		}
	}()
	if err := h(ctx, c, s, hdr.Req); err != nil && !errors.Is(err, context.Canceled) {
		slog.Debug("service handler error", "service", hdr.Service, "peer", c.RemoteID(), "err", err)
	}
}

// DecodeReq unmarshals a handler's req into v, treating an absent request as
// the zero value.
func DecodeReq(req json.RawMessage, v any) error {
	if len(req) == 0 {
		return nil
	}
	return json.Unmarshal(req, v)
}
