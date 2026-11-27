package signal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// The websocket bridge lets peers whose network passes only HTTP(S) reach
// the server: each WebSocket connection gets a UDP socket of its own on
// this machine, and datagrams travel between the two (see internal/wsdgram
// for the framing). Only the server's own ports are ever addressed: the
// rendezvous port, the NAT-probe port and the ports of relay sessions open
// right now. So the bridge cannot be used to send UDP anywhere else, and
// what passes through it is the same QUIC ciphertext a UDP peer would
// send.
//
// To the rest of the server a bridged peer is an ordinary connection
// coming from its bridge socket. The server knows those sockets, so it
// counts the peer's limits against the address the WebSocket came from,
// and it tells the other side of an introduction that this peer can only
// be reached through the relay: hole punching to a bridge cannot work.

// WSConfig configures the websocket bridge. Zero fields take the defaults
// documented on each.
type WSConfig struct {
	// Path is the one URL path served. Default wsdgram.DefaultPath.
	Path string
	// Target is the address the server's UDP sockets are reachable at
	// from this machine; bridge sockets are bound to it. Default
	// 127.0.0.1.
	Target netip.Addr
	// MaxConns bounds open WebSocket connections, MaxConnsPerSource those
	// from one source address. Defaults 512 and 32.
	MaxConns          int
	MaxConnsPerSource int
	// Rate is each connection's budget in bytes per second, both
	// directions together; TotalRate the budget of all of them. Defaults:
	// the relay's per-session and total rates plus 32 KB/s for
	// rendezvous traffic, or 32 KB/s and 1 MB/s without a relay.
	Rate      int
	TotalRate int
	// Idle closes a connection that has carried nothing for this long.
	// Default 2 min; peers send keep-alives every 10 s.
	Idle time.Duration
	// RealIPHeader names a request header holding the client's address,
	// set by a reverse proxy in front of the bridge (CF-Connecting-IP
	// behind a Cloudflare Tunnel, X-Forwarded-For behind most others; for
	// a list the last entry counts). Requests without it are refused.
	// Empty means the TCP peer's address, which behind a proxy is the
	// proxy's own, so all clients share one source's limits.
	RealIPHeader string
}

const wsRendezvousRate = 32 << 10

func (c *WSConfig) defaults(relay *RelayConfig) {
	if c.Path == "" {
		c.Path = wsdgram.DefaultPath
	}
	if !c.Target.IsValid() {
		c.Target = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	if c.MaxConns <= 0 {
		c.MaxConns = 512
	}
	if c.MaxConnsPerSource <= 0 {
		c.MaxConnsPerSource = 32
	}
	if c.Rate <= 0 {
		c.Rate = wsRendezvousRate
		if relay != nil {
			c.Rate += relay.Rate
		}
	}
	if c.TotalRate <= 0 {
		c.TotalRate = 1 << 20
		if relay != nil {
			c.TotalRate = relay.TotalRate + wsRendezvousRate
		}
	}
	if c.Idle <= 0 {
		c.Idle = 2 * time.Minute
	}
}

// wsRegistry is the server's knowledge of its bridges.
type wsRegistry struct {
	// bridges maps a bridge socket's address to the client behind it.
	bridges sync.Map // netip.AddrPort → netip.Addr
	// relayPorts holds the ports of relay sessions open now.
	relayPorts sync.Map // uint16 → struct{}

	// cfg is the bridge's configuration in force, for the panel.
	cfg atomic.Pointer[WSConfig]

	mu        sync.Mutex
	conns     int
	perSource map[netip.Addr]int
	warned    time.Time
}

// bridgedClient returns the client behind a bridge socket address.
func (r *wsRegistry) bridgedClient(addr netip.AddrPort) (netip.Addr, bool) {
	v, ok := r.bridges.Load(addr)
	if !ok {
		return netip.Addr{}, false
	}
	return v.(netip.Addr), true
}

func (r *wsRegistry) admit(src netip.Addr, cfg *WSConfig) (release func(), reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.perSource == nil {
		r.perSource = map[netip.Addr]int{}
	}
	switch {
	case r.conns >= cfg.MaxConns:
		return nil, "too many websocket connections"
	case r.perSource[src] >= cfg.MaxConnsPerSource:
		return nil, "too many websocket connections from this address"
	}
	r.conns++
	r.perSource[src]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.conns--
		if r.perSource[src]--; r.perSource[src] <= 0 {
			delete(r.perSource, src)
		}
	}, ""
}

// shouldWarn throttles warnings about refused connections.
func (r *wsRegistry) shouldWarn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.warned) < 10*time.Second {
		return false
	}
	r.warned = time.Now()
	return true
}

// allowedPort reports whether a bridge may address port on this server.
func (s *Server) allowedPort(port uint16) bool {
	if port == 0 {
		return false
	}
	if int32(port) == s.mainPort.Load() || int32(port) == s.altPort.Load() {
		return true
	}
	_, ok := s.ws.relayPorts.Load(port)
	return ok
}

// WSHandler returns the HTTP handler of the websocket bridge. It serves
// cfg.Path only and needs Serve to be running.
func (s *Server) WSHandler(cfg WSConfig) http.Handler {
	cfg.defaults(s.cfg.Relay)
	eff := cfg
	s.ws.cfg.Store(&eff)
	total := nat.NewByteBudget(cfg.TotalRate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cfg.Path {
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "this is a remohop websocket endpoint; point `remohop config set signal-ws` at it", http.StatusBadRequest)
			return
		}
		client, err := clientAddr(r, cfg.RealIPHeader)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != wsdgram.OriginValue {
			// A browser: some web page tries to use its visitors.
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if s.mainPort.Load() == 0 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		src, _ := sourceOf(client)
		release, reason := s.ws.admit(src, &cfg)
		if release == nil {
			s.stats.add(stWSRefused)
			if s.ws.shouldWarn() {
				s.log.Warn("websocket bridge: refused a connection", "client", client, "reason", reason)
			}
			http.Error(w, reason, http.StatusServiceUnavailable)
			return
		}
		defer release()
		ws := websocket.Server{
			Handshake: func(c *websocket.Config, _ *http.Request) error {
				if !slices.Contains(c.Protocol, wsdgram.Subprotocol) {
					return errors.New("missing subprotocol")
				}
				c.Protocol = []string{wsdgram.Subprotocol}
				return nil
			},
			Handler: func(conn *websocket.Conn) { s.serveBridge(conn, client, &cfg, total) },
		}
		ws.ServeHTTP(w, r)
	})
}

// clientAddr is the address a bridge request comes from.
func clientAddr(r *http.Request, header string) (netip.Addr, error) {
	if header != "" {
		v := r.Header.Values(header)
		if len(v) == 0 {
			return netip.Addr{}, fmt.Errorf("missing %s header", header)
		}
		last := v[len(v)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		ip, err := netip.ParseAddr(strings.TrimSpace(last))
		if err != nil {
			return netip.Addr{}, fmt.Errorf("malformed %s header", header)
		}
		return ip.Unmap().WithZone(""), nil
	}
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, errors.New("unknown client address")
	}
	return ap.Addr().Unmap().WithZone(""), nil
}

type bridgeStats struct {
	in, out, dropped atomic.Int64
	last             atomic.Int64 // unix nanoseconds of the last datagram
}

// serveBridge passes datagrams between one WebSocket and the server's
// ports until either side ends, the bridge idles or the server stops.
func (s *Server) serveBridge(conn *websocket.Conn, client netip.Addr, cfg *WSConfig, total *nat.ByteBudget) {
	defer conn.Close()
	conn.MaxPayloadBytes = wsdgram.MaxMessage
	conn.PayloadType = websocket.BinaryFrame
	udp, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(cfg.Target, 0)))
	if err != nil {
		s.log.Error("websocket bridge: cannot open a socket", "err", err)
		return
	}
	defer udp.Close()
	la := udp.LocalAddr().(*net.UDPAddr).AddrPort()
	local := netip.AddrPortFrom(la.Addr().Unmap(), la.Port())
	s.ws.bridges.Store(local, client)
	defer s.ws.bridges.Delete(local)

	s.mu.Lock()
	ctx := s.relayCtx
	s.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.Message.Send(conn, wsdgram.Hello(uint16(s.mainPort.Load()), uint16(s.altPort.Load()))); err != nil {
		return
	}
	start := time.Now()
	s.stats.add(stWSOpened)
	s.log.Info("websocket bridge opened", "client", client)
	var st bridgeStats
	st.last.Store(start.UnixNano())
	budget := nat.NewByteBudget(cfg.Rate)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer conn.Close()
		s.bridgeFromUDP(conn, udp, cfg.Target, budget, total, &st)
	}()

	target := func(port uint16) *net.UDPAddr {
		return net.UDPAddrFromAddrPort(netip.AddrPortFrom(cfg.Target, port))
	}
	for {
		// The idle limit counts both directions: a peer that only
		// receives (a long download through the relay) still acks.
		deadline := time.Unix(0, st.last.Load()).Add(cfg.Idle)
		if !time.Now().Before(deadline) {
			break
		}
		conn.SetReadDeadline(deadline)
		var m []byte
		if err := websocket.Message.Receive(conn, &m); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // re-check against the latest activity
			}
			break
		}
		port, payload, err := wsdgram.ParseData(m)
		if err != nil || !s.allowedPort(port) {
			st.dropped.Add(1)
			continue
		}
		if !budget.Take(len(m)) || !total.Take(len(m)) {
			st.dropped.Add(1)
			continue
		}
		if _, err := udp.WriteToUDP(payload, target(port)); err == nil {
			st.in.Add(int64(len(payload)))
			st.last.Store(time.Now().UnixNano())
		}
	}
	conn.Close()
	udp.Close()
	<-done
	s.log.Info("websocket bridge closed", "client", client, "duration", time.Since(start).Round(time.Second),
		"bytes_in", st.in.Load(), "bytes_out", st.out.Load(), "dropped", st.dropped.Load())
}

// bridgeFromUDP passes datagrams from the server's ports to the WebSocket.
func (s *Server) bridgeFromUDP(conn *websocket.Conn, udp *net.UDPConn, target netip.Addr, budget, total *nat.ByteBudget, st *bridgeStats) {
	buf := make([]byte, wsdgram.MaxDatagram)
	msg := make([]byte, 0, wsdgram.MaxMessage)
	for {
		n, from, err := udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		// Only the server itself talks to a bridge socket.
		if from.Addr().Unmap() != target || !s.allowedPort(from.Port()) {
			st.dropped.Add(1)
			continue
		}
		msg = wsdgram.AppendData(msg[:0], from.Port(), buf[:n])
		if !budget.Take(len(msg)) || !total.Take(len(msg)) {
			st.dropped.Add(1)
			continue
		}
		conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := websocket.Message.Send(conn, msg); err != nil {
			return
		}
		st.out.Add(int64(n))
		st.last.Store(time.Now().UnixNano())
	}
}

// bridgedPeerAddr is what the other side of an introduction is told about
// a bridged peer's address: nothing reachable (see wsdgram.ServerIP). It
// only keeps the field well-formed; the "via" marker is what counts.
var bridgedPeerAddr = netip.AddrPortFrom(wsdgram.ServerIP, 9)

const viaWS = "ws"
