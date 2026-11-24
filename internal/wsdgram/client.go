package wsdgram

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Options configures a client Conn.
type Options struct {
	// URL is the server's endpoint, ws:// or wss://. A URL without a path
	// gets DefaultPath.
	URL string
	// Header holds extra request headers, such as Cloudflare Access
	// service-token headers. They are sent to the server (and through a
	// proxy only inside the TLS tunnel for wss://), never logged.
	Header http.Header
	// TLSConfig, if set, is used for wss:// (tests pass their own roots).
	TLSConfig *tls.Config
	// Proxy picks the HTTP proxy for a request URL (https:// for wss://,
	// http:// for ws://); nil means the environment: HTTPS_PROXY,
	// HTTP_PROXY and NO_PROXY, in either case. A proxy is used with
	// CONNECT.
	Proxy func(*url.URL) (*url.URL, error)
	// DialContext opens TCP connections; nil means a net.Dialer.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	// HandshakeTimeout bounds connecting, proxy, TLS and WebSocket
	// handshakes included. Default 10 s.
	HandshakeTimeout time.Duration
	// Logger receives connection events at debug level; nil discards them.
	Logger *slog.Logger
}

// ParseURL checks a server endpoint URL, adding DefaultPath when it has no
// path. It refuses user information in the URL, which would end up in logs.
func ParseURL(s string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("websocket URL: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http", "https":
		return nil, fmt.Errorf("websocket URL %q: use wss:// (or ws://), not %s://", s, u.Scheme)
	default:
		return nil, fmt.Errorf("websocket URL %q: must start with wss:// or ws://", s)
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("websocket URL %q has no host", s)
	}
	if u.User != nil {
		return nil, errors.New("websocket URL must not contain a user name or password (use the access-token settings)")
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = DefaultPath
	}
	u.Fragment, u.RawFragment = "", ""
	return u, nil
}

// Addr is the local address of a Conn: the endpoint, not a UDP address,
// so code that lists a socket's own UDP addresses finds none.
type Addr struct{ url string }

// Network is "websocket".
func (a Addr) Network() string { return "websocket" }
func (a Addr) String() string  { return a.url }

// IsBridge reports whether a is the local address of a Conn.
func IsBridge(a net.Addr) bool {
	_, ok := a.(Addr)
	return ok
}

// Conn is a net.PacketConn whose only peer is the server behind a
// WebSocket endpoint, addressed as ServerIP and the server's UDP port.
//
// The WebSocket is opened on first use (or by Connect) and opened again
// after it drops; datagrams written while it is down are queued briefly or
// dropped, as a UDP socket would lose them. Reads block until a datagram
// arrives, the read deadline passes or the Conn is closed.
type Conn struct {
	opts Options
	u    *url.URL
	log  *slog.Logger
	in   chan inPacket
	done chan struct{}

	mu       sync.Mutex
	closed   bool
	sess     *session
	dialing  bool
	attempts int // dial attempts finished
	lastErr  error
	backoff  time.Duration
	notAfter time.Time // no automatic dial before this
	queue    [][]byte
	mainPort uint16
	altPort  uint16
	changed  chan struct{} // closed and replaced on every state change

	dl deadline
}

type inPacket struct {
	port uint16
	data []byte
}

type session struct {
	ws  *websocket.Conn
	wmu sync.Mutex
}

const (
	maxQueue      = 64
	inQueue       = 512
	writeTimeout  = 5 * time.Second
	minBackoff    = 500 * time.Millisecond
	maxBackoff    = 30 * time.Second
	defaultHSTime = 10 * time.Second
)

// New prepares a Conn; it does not connect yet.
func New(opts Options) (*Conn, error) {
	u, err := ParseURL(opts.URL)
	if err != nil {
		return nil, err
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHSTime
	}
	if opts.DialContext == nil {
		d := &net.Dialer{Timeout: opts.HandshakeTimeout, KeepAlive: 30 * time.Second}
		opts.DialContext = d.DialContext
	}
	if opts.Proxy == nil {
		opts.Proxy = func(u *url.URL) (*url.URL, error) { return http.ProxyFromEnvironment(&http.Request{URL: u}) }
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Conn{
		opts:    opts,
		u:       u,
		log:     log,
		in:      make(chan inPacket, inQueue),
		done:    make(chan struct{}),
		backoff: minBackoff,
		changed: make(chan struct{}),
		dl:      deadline{ch: make(chan struct{})},
	}, nil
}

// URL is the endpoint, without query parameters.
func (c *Conn) URL() string { return redact(c.u) }

func redact(u *url.URL) string {
	v := *u
	v.RawQuery, v.User = "", nil
	return v.String()
}

// Connect opens the WebSocket now, if it is not open, and waits for the
// server's hello. It reports why connecting failed, which datagram writes
// cannot.
func (c *Conn) Connect(ctx context.Context) error {
	c.mu.Lock()
	for {
		if c.closed {
			c.mu.Unlock()
			return net.ErrClosed
		}
		if c.sess != nil {
			c.mu.Unlock()
			return nil
		}
		if !c.dialing {
			c.startDial()
		}
		want := c.attempts + 1
		ch := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			c.mu.Lock()
			err := c.lastErr
			c.mu.Unlock()
			if err != nil {
				return fmt.Errorf("%w (last attempt: %v)", ctx.Err(), err)
			}
			return ctx.Err()
		case <-ch:
		}
		c.mu.Lock()
		if c.sess == nil && !c.dialing && c.attempts >= want && c.lastErr != nil {
			err := c.lastErr
			c.mu.Unlock()
			return err
		}
	}
}

// Err is why the last attempt to open the WebSocket failed, or nil.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// ServerAddr is the rendezvous server's main address as reached through
// this Conn; valid once Connect has succeeded.
func (c *Conn) ServerAddr() netip.AddrPort {
	c.mu.Lock()
	defer c.mu.Unlock()
	return netip.AddrPortFrom(ServerIP, c.mainPort)
}

// Connected reports whether the WebSocket is open right now.
func (c *Conn) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

// broadcast wakes everyone waiting for a state change; c.mu is held.
func (c *Conn) broadcast() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// startDial starts a dial attempt in the background; c.mu is held.
func (c *Conn) startDial() {
	c.dialing = true
	go c.runDial()
}

func (c *Conn) runDial() {
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.HandshakeTimeout)
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	ws, mainPort, altPort, err := c.dial(ctx)
	cancel()

	c.mu.Lock()
	c.dialing = false
	c.attempts++
	if err == nil && c.closed {
		err = net.ErrClosed
		ws.Close()
	}
	if err != nil {
		c.lastErr = err
		c.notAfter = time.Now().Add(c.backoff)
		c.backoff = min(2*c.backoff, maxBackoff)
		c.queue = nil
		c.broadcast()
		c.mu.Unlock()
		c.log.Debug("websocket bridge: cannot connect", "url", c.URL(), "err", err)
		return
	}
	s := &session{ws: ws}
	s.wmu.Lock()
	c.sess, c.lastErr, c.backoff = s, nil, minBackoff
	c.mainPort, c.altPort = mainPort, altPort
	queued := c.queue
	c.queue = nil
	c.broadcast()
	c.mu.Unlock()
	c.log.Debug("websocket bridge connected", "url", c.URL(), "port", mainPort)
	go c.readLoop(s)
	var werr error
	for _, m := range queued {
		if werr = s.sendLocked(m); werr != nil {
			break
		}
	}
	s.wmu.Unlock()
	if werr != nil {
		c.endSession(s, werr)
	}
}

func (s *session) sendLocked(m []byte) error {
	s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return websocket.Message.Send(s.ws, m)
}

func (s *session) send(m []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.sendLocked(m)
}

func (c *Conn) endSession(s *session, err error) {
	c.mu.Lock()
	if c.sess == s {
		c.sess = nil
		if !c.closed {
			c.lastErr = fmt.Errorf("websocket closed: %w", err)
		}
		c.broadcast()
	}
	c.mu.Unlock()
	s.ws.Close()
}

func (c *Conn) readLoop(s *session) {
	for {
		var m []byte
		if err := websocket.Message.Receive(s.ws, &m); err != nil {
			c.endSession(s, err)
			return
		}
		port, payload, err := ParseData(m)
		if err != nil {
			continue
		}
		select {
		case c.in <- inPacket{port: port, data: payload}:
		default: // full: dropped, as a UDP socket would
		}
	}
}

// ReadFrom returns the next datagram from the server.
func (c *Conn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		t, changed := c.dl.get()
		var tm *time.Timer
		var timer <-chan time.Time
		if !t.IsZero() {
			d := time.Until(t)
			if d <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
			tm = time.NewTimer(d)
			timer = tm.C
		}
		var p inPacket
		var err error
		got := false
		select {
		case p = <-c.in:
			got = true
		case <-c.done:
			err = net.ErrClosed
		case <-timer:
			err = os.ErrDeadlineExceeded
		case <-changed:
		}
		if tm != nil {
			tm.Stop()
		}
		if got {
			n := copy(b, p.data)
			return n, &net.UDPAddr{IP: ServerIP.AsSlice(), Port: int(p.port)}, nil
		}
		if err != nil {
			return 0, nil, err
		}
	}
}

// WriteTo sends a datagram to the server port named by addr, which must be
// at ServerIP. Like UDP it does not report loss: while the WebSocket is
// being (re)opened a few datagrams are queued and the rest dropped.
func (c *Conn) WriteTo(b []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, &net.OpError{Op: "write", Net: "websocket", Addr: addr, Err: errors.New("not a UDP address")}
	}
	ap := ua.AddrPort()
	if ap.Addr().Unmap() != ServerIP || ap.Port() == 0 {
		return 0, &net.OpError{Op: "write", Net: "websocket", Addr: addr, Err: errors.New("only the rendezvous server is reachable over the websocket bridge")}
	}
	if len(b) > MaxDatagram {
		return 0, &net.OpError{Op: "write", Net: "websocket", Addr: addr, Err: errors.New("datagram too large")}
	}
	m := AppendData(make([]byte, 0, headerLen+len(b)), ap.Port(), b)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	if s := c.sess; s != nil {
		c.mu.Unlock()
		if err := s.send(m); err != nil {
			c.endSession(s, err)
		}
		return len(b), nil
	}
	if len(c.queue) == maxQueue {
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, m)
	if !c.dialing && !time.Now().Before(c.notAfter) {
		c.startDial()
	}
	c.mu.Unlock()
	return len(b), nil
}

// Close closes the WebSocket and ends pending reads.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return net.ErrClosed
	}
	c.closed = true
	close(c.done)
	s := c.sess
	c.sess = nil
	c.broadcast()
	c.mu.Unlock()
	if s != nil {
		s.ws.Close()
	}
	return nil
}

// LocalAddr is an Addr naming the endpoint.
func (c *Conn) LocalAddr() net.Addr { return Addr{url: c.URL()} }

// SetDeadline sets the read deadline; writes never block for long.
func (c *Conn) SetDeadline(t time.Time) error { return c.SetReadDeadline(t) }

// SetReadDeadline makes pending and future reads fail after t.
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.dl.set(t)
	return nil
}

// SetWriteDeadline is accepted and ignored: writes are bounded by an
// internal timeout.
func (c *Conn) SetWriteDeadline(time.Time) error { return nil }

type deadline struct {
	mu sync.Mutex
	t  time.Time
	ch chan struct{}
}

func (d *deadline) get() (time.Time, <-chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.t, d.ch
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	d.t = t
	close(d.ch)
	d.ch = make(chan struct{})
	d.mu.Unlock()
}

// dial opens the TCP connection (through a proxy if one applies), TLS for
// wss://, and the WebSocket, and reads the server's hello.
func (c *Conn) dial(ctx context.Context) (*websocket.Conn, uint16, uint16, error) {
	u := c.u
	secure := u.Scheme == "wss"
	hostport := u.Host
	if u.Port() == "" {
		port := "80"
		if secure {
			port = "443"
		}
		hostport = net.JoinHostPort(u.Hostname(), port)
	}
	reqScheme := "http"
	if secure {
		reqScheme = "https"
	}
	proxyURL, err := c.opts.Proxy(&url.URL{Scheme: reqScheme, Host: u.Host})
	if err != nil {
		return nil, 0, 0, fmt.Errorf("proxy setting: %w", err)
	}
	var nc net.Conn
	if proxyURL != nil {
		nc, err = dialProxy(ctx, c.opts.DialContext, proxyURL, hostport)
	} else {
		nc, err = c.opts.DialContext(ctx, "tcp", hostport)
	}
	if err != nil {
		return nil, 0, 0, err
	}
	ok := false
	defer func() {
		if !ok {
			nc.Close()
		}
	}()
	if d, has := ctx.Deadline(); has {
		nc.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if secure {
		conf := &tls.Config{}
		if c.opts.TLSConfig != nil {
			conf = c.opts.TLSConfig.Clone()
		}
		if conf.ServerName == "" {
			conf.ServerName = u.Hostname()
		}
		conf.MinVersion = max(conf.MinVersion, tls.VersionTLS12)
		conf.NextProtos = []string{"http/1.1"}
		tc := tls.Client(nc, conf)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, 0, 0, fmt.Errorf("TLS with %s: %w", hostport, err)
		}
		nc = tc
	}
	header := c.opts.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	if header.Get("User-Agent") == "" {
		header.Set("User-Agent", "remohop-wsdgram/1")
	}
	cfg := &websocket.Config{
		Location: u,
		Origin:   &url.URL{Scheme: "remohop", Opaque: "remohop"},
		Version:  websocket.ProtocolVersionHybi13,
		Protocol: []string{Subprotocol},
		Header:   header,
	}
	rec := &statusRecorder{Conn: nc}
	ws, err := websocket.NewClient(cfg, rec)
	if err != nil {
		if errors.Is(err, websocket.ErrBadStatus) {
			return nil, 0, 0, statusError(rec.statusLine())
		}
		return nil, 0, 0, fmt.Errorf("websocket handshake with %s: %w", redact(u), err)
	}
	ws.MaxPayloadBytes = MaxMessage
	var hello []byte
	if err := websocket.Message.Receive(ws, &hello); err != nil {
		ws.Close()
		return nil, 0, 0, fmt.Errorf("websocket %s: no hello from the server: %w", redact(u), err)
	}
	mainPort, altPort, err := parseHello(hello)
	if err != nil {
		ws.Close()
		return nil, 0, 0, fmt.Errorf("websocket %s is not a remohop endpoint: %w", redact(u), err)
	}
	stop()
	nc.SetDeadline(time.Time{})
	ok = true
	return ws, mainPort, altPort, nil
}

// statusRecorder keeps the first bytes read from a connection, so that a
// failed WebSocket handshake can say what the server answered.
type statusRecorder struct {
	net.Conn
	head []byte
}

func (r *statusRecorder) Read(b []byte) (int, error) {
	n, err := r.Conn.Read(b)
	if len(r.head) < 128 {
		r.head = append(r.head, b[:min(n, 128-len(r.head))]...)
	}
	return n, err
}

func (r *statusRecorder) statusLine() string {
	line, _, _ := bytes.Cut(r.head, []byte("\r\n"))
	s := strings.ToValidUTF8(string(line), "?")
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StatusError is a refusal of the WebSocket handshake by the server or
// something in front of it.
type StatusError struct {
	Code int
	Line string
}

func (e *StatusError) Error() string {
	hint := ""
	switch e.Code {
	case 401, 403:
		hint = " (refused: behind Cloudflare Access this means a missing or wrong service token, see signal-ws-access-id)"
	case 404:
		hint = " (no websocket endpoint at this URL: check the path, default " + DefaultPath + ")"
	case 426, 400:
		hint = " (the endpoint did not accept the WebSocket upgrade)"
	case 502, 503, 504, 530:
		hint = " (the server behind the endpoint is down or not reachable from its proxy)"
	}
	return "websocket endpoint answered " + e.Line + hint
}

func statusError(line string) error {
	e := &StatusError{Line: line}
	if line == "" {
		e.Line = "with an error"
	}
	if _, rest, ok := strings.Cut(line, " "); ok {
		fmt.Sscanf(rest, "%d", &e.Code)
	}
	return e
}

// dialProxy opens a tunnel to target through an HTTP(S) proxy with
// CONNECT. Credentials in the proxy URL are sent as Basic authorization
// and appear in no error message.
func dialProxy(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), p *url.URL, target string) (net.Conn, error) {
	var port string
	switch p.Scheme {
	case "http", "":
		port = "80"
	case "https":
		port = "443"
	default:
		return nil, fmt.Errorf("proxy %s: only http:// and https:// proxies are supported", p.Scheme+"://"+p.Host)
	}
	addr := p.Host
	if p.Port() == "" {
		addr = net.JoinHostPort(p.Hostname(), port)
	}
	nc, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", addr, err)
	}
	ok := false
	defer func() {
		if !ok {
			nc.Close()
		}
	}()
	if d, has := ctx.Deadline(); has {
		nc.SetDeadline(d)
	}
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if p.Scheme == "https" {
		tc := tls.Client(nc, &tls.Config{ServerName: p.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("proxy %s: TLS: %w", addr, err)
		}
		nc = tc
	}
	var req strings.Builder
	fmt.Fprintf(&req, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: remohop-wsdgram/1\r\n", target, target)
	if p.User != nil {
		pass, _ := p.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(p.User.Username() + ":" + pass))
		fmt.Fprintf(&req, "Proxy-Authorization: Basic %s\r\n", cred)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(nc, req.String()); err != nil {
		return nil, fmt.Errorf("proxy %s: %w", addr, err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("proxy %s: %w", addr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusProxyAuthRequired {
			hint = " (put the proxy's user and password in HTTPS_PROXY: http://USER:PASSWORD@proxy:port)"
		}
		return nil, fmt.Errorf("proxy %s refused to connect to %s: %s%s", addr, target, resp.Status, hint)
	}
	stop()
	nc.SetDeadline(time.Time{})
	ok = true
	if br.Buffered() > 0 {
		head, _ := br.Peek(br.Buffered())
		return &prefixConn{Conn: nc, r: io.MultiReader(bytes.NewReader(bytes.Clone(head)), nc)}, nil
	}
	return nc, nil
}

type prefixConn struct {
	net.Conn
	r io.Reader
}

func (p *prefixConn) Read(b []byte) (int, error) { return p.r.Read(b) }
