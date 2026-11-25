package signal

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// Options configures Dial.
type Options struct {
	// Pin, if set, is the rendezvous server's public key (as printed by
	// `remohop signal`); the connection fails if the server presents another.
	//
	// Leaving it empty accepts any server key, and that is a deliberate
	// default rather than a shortcut: nothing a rendezvous server tells us
	// is trusted. The peer it introduces us to must still pass the PAKE,
	// which is bound to the TLS connection between the two peers, so an
	// impostor server (or someone intercepting the way to the real one)
	// can deny service or learn which ID we asked for, but cannot read or
	// alter a session, nor obtain anything to test password guesses
	// against. Pinning only removes those two nuisances; a web-PKI
	// certificate would add nothing on top and would make self-hosting
	// harder.
	Pin string

	// WS is the URL of the server's websocket bridge (signal-ws), for
	// networks that let only HTTP(S) out; empty if none is configured.
	// WSHeader holds extra headers for it (Cloudflare Access tokens).
	WS       string
	WSHeader map[string][]string
	// Transport says how to reach the server: TransportAuto (UDP, and the
	// bridge if UDP does not get through), TransportUDP or TransportWS.
	// Empty means auto.
	Transport string

	// HintDir is the configuration directory where Connect remembers that
	// UDP to the server is blocked from the current network (see
	// UDPBlocked); empty remembers in memory only.
	HintDir string
}

// Client is a connection to a rendezvous server, made from a peer's shared
// socket.
type Client struct {
	q      *quic.Conn
	sock   *nat.Socket
	server netip.AddrPort
	key    ed25519.PublicKey
	// bridged: the connection runs over the websocket bridge.
	bridged bool

	// OnRelay, set by a host before ServeOffers, is called when the server
	// has opened a relay for an earlier offer (identified by its nonce)
	// at the given address. Returning an error declines the relay.
	OnRelay func(nonce []byte, relay netip.AddrPort) error

	natCh     chan nat.Type
	natOnce   sync.Once
	natType   nat.Type
	stopProbe context.CancelFunc
	watching  sync.Once
}

// natGrace is how long after the rendezvous connection is up the NAT
// measurement may still take. It normally finishes during the connection's
// handshake; this only bounds the wait for a lost packet's retransmission
// before giving up and reporting the type as unknown.
const natGrace = 300 * time.Millisecond

// SplitAddr parses a rendezvous server address, adding the default port
// when none is given.
func SplitAddr(addr string) (host, port string, err error) {
	if addr == "" {
		return "", "", errors.New("empty rendezvous server address")
	}
	host, port, err = net.SplitHostPort(addr)
	if err != nil {
		// No port: a bare hostname, IPv4 address, or bracketed IPv6.
		host = addr
		if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
		port = strconv.Itoa(DefaultPort)
		if _, _, err2 := net.SplitHostPort(net.JoinHostPort(host, port)); err2 != nil || host == "" {
			return "", "", fmt.Errorf("malformed rendezvous server address %q", addr)
		}
	}
	if host == "" {
		return "", "", fmt.Errorf("rendezvous server address %q has no host", addr)
	}
	return host, port, nil
}

// Dial connects to the rendezvous server at addr ("host[:port]") from sock.
func Dial(ctx context.Context, sock *nat.Socket, addr string, opts Options) (*Client, error) {
	host, port, err := SplitAddr(addr)
	if err != nil {
		return nil, err
	}
	var pin ed25519.PublicKey
	if opts.Pin != "" {
		if pin, err = auth.DecodeKey(opts.Pin); err != nil {
			return nil, fmt.Errorf("rendezvous server pin: %w", err)
		}
	}
	bridged := wsdgram.IsBridge(sock.LocalAddr())
	// Within ctx, and an address family this machine can route (see there).
	ua, err := nat.ResolveUDP(ctx, sock.LocalAddr(), net.JoinHostPort(host, port))
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("rendezvous server %s: %w", addr, ctx.Err())
		}
		return nil, fmt.Errorf("rendezvous server %s: %w", addr, err)
	}

	ap := ua.AddrPort()
	c := &Client{sock: sock, server: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), natCh: make(chan nat.Type, 1), bridged: bridged}
	// Measure the NAT now, alongside the handshake: the result has to be
	// in hand before an introduction is requested, both to send it along
	// and because punching can change what a NAT answers. Through the
	// bridge there is no NAT to measure: what the server sees is the
	// bridge.
	pctx, stopProbe := context.WithCancel(context.Background())
	c.stopProbe = stopProbe
	if bridged {
		c.natCh <- nat.TypeUnknown
	} else {
		go func() { c.natCh <- sock.ProbeNAT(pctx, c.server) }()
	}

	tlsConf := &tls.Config{
		NextProtos:         []string{ALPN},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // see Options.Pin
		VerifyPeerCertificate: auth.VerifyPeerKey(func(key ed25519.PublicKey) error {
			if pin != nil && !pin.Equal(key) {
				return fmt.Errorf("rendezvous server key %s does not match the pinned key: %w", auth.EncodeKey(key), ErrPinMismatch)
			}
			c.key = key
			return nil
		}),
	}
	q, err := sock.Transport().Dial(ctx, ua, tlsConf, &quic.Config{
		HandshakeIdleTimeout: 5 * time.Second,
		MaxIdleTimeout:       30 * time.Second,
		// Keep-alives are what presence is made of: they hold both the
		// connection and the NAT mapping the server knows us by.
		KeepAlivePeriod:       10 * time.Second,
		MaxIncomingStreams:    16,
		MaxIncomingUniStreams: -1,
		InitialPacketSize:     nat.InitialPacketSize,
	})
	if err != nil {
		stopProbe()
		if noALPN(err) {
			return nil, wrongServer(ctx, sock.Transport(), ua, addr, err)
		}
		return nil, fmt.Errorf("rendezvous server %s unreachable: %w", addr, err)
	}
	c.q = q
	time.AfterFunc(natGrace, stopProbe)
	return c, nil
}

// NATType returns the socket's NAT type as measured against this server
// while connecting, waiting at most natGrace after Dial returned for the
// measurement to finish. The result is also on the socket (nat.Socket.NATType).
func (c *Client) NATType() nat.Type {
	c.natOnce.Do(func() { c.natType = <-c.natCh })
	return c.natType
}

// Bridged reports whether the connection runs over the server's websocket
// bridge, so that only the relay can reach the other peer.
func (c *Client) Bridged() bool { return c.bridged }

// ServerKey is the public key the server presented.
func (c *Client) ServerKey() ed25519.PublicKey { return c.key }

// Close ends the connection; a registered host goes offline.
func (c *Client) Close() error {
	c.stopProbe()
	return c.q.CloseWithError(0, "")
}

// Done is closed when the connection has ended.
func (c *Client) Done() <-chan struct{} { return c.q.Context().Done() }

// Err reports why the connection ended.
func (c *Client) Err() error { return context.Cause(c.q.Context()) }

func (c *Client) roundTrip(ctx context.Context, fn func(st *quic.Stream) error) error {
	st, err := c.q.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	defer st.CancelRead(0)
	stop := context.AfterFunc(ctx, func() { st.CancelRead(0); st.CancelWrite(0) })
	defer stop()
	if err := fn(st); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// Registration is what the server tells a registered host.
type Registration struct {
	ID        string         // the host's 9-digit ID
	Reflexive netip.AddrPort // the socket's public address as the server sees it
	Alt       netip.AddrPort // the server's NAT-probe address; invalid if it has none
}

// Register proves possession of id's key and puts the host online under
// the ID the server keeps for that key. The host stays online until the
// connection ends.
func (c *Client) Register(ctx context.Context, id *auth.Identity) (*Registration, error) {
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, request{T: "register", V: protoVersion, Key: id.Public()}); err != nil {
			return err
		}
		var ch response
		if err := readMsg(st, &ch); err != nil {
			return err
		}
		if err := ch.err(); err != nil {
			return err
		}
		if len(ch.Challenge) != challengeSize {
			return errors.New("signal: malformed challenge")
		}
		cs := c.q.ConnectionState().TLS
		exporter, err := cs.ExportKeyingMaterial(exporterLabel, nil, 32)
		if err != nil {
			return err
		}
		if err := writeMsg(st, proof{Sig: id.Sign(registerMessage(ch.Challenge, exporter))}); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	if err != nil {
		return nil, err
	}
	if !validID(resp.ID) {
		return nil, errors.New("signal: server assigned a malformed ID")
	}
	reg := &Registration{ID: resp.ID, Alt: c.alt(resp.AltPort)}
	reg.Reflexive, _ = parseAddr(resp.You)
	c.watching.Do(func() { go c.watch() })
	return reg, nil
}

// A registered host's connection outlives what it was made for when the
// machine sleeps (the server drops it meanwhile, and the host would find out
// only by its idle timeout, offline until then: just when Wake-on-LAN or
// the user expects it back) or moves to another network (the server's view
// of its address and NAT go stale). watch notices both and ends the
// connection, so the host registers again at once.

// watchEvery is how often a registered host checks, in nanoseconds;
// tests shorten it.
var watchEvery atomic.Int64

func init() { watchEvery.Store(int64(5 * time.Second)) }

// sleepJump is how much longer than expected a check may come before the
// machine is taken to have slept.
const sleepJump = 15 * time.Second

// slept reports whether the time between two checks says the machine was
// asleep: the monotonic clock stands still during sleep on most systems,
// the wall clock does not, and where the monotonic clock runs on the check
// itself comes late.
func slept(wall, mono, every time.Duration) bool {
	return wall-mono > sleepJump || mono > every+sleepJump
}

// netPrint summarises the machine's addresses: the IPv4 addresses and the
// IPv6 /64s (temporary IPv6 addresses come and go within their /64; that is
// no network change).
func netPrint(addrs []netip.Addr) string {
	var keys []string
	for _, a := range addrs {
		if a.Is6() {
			a = netip.PrefixFrom(a, 64).Masked().Addr()
		}
		keys = append(keys, a.String())
	}
	slices.Sort(keys)
	return strings.Join(slices.Compact(keys), ",")
}

func (c *Client) watch() {
	every := time.Duration(watchEvery.Load())
	t := time.NewTicker(every)
	defer t.Stop()
	prev := time.Now()
	prevNet := netPrint(c.sock.Locals())
	for {
		select {
		case <-c.q.Context().Done():
			return
		case <-t.C:
		}
		now := time.Now()
		wall, mono := now.Round(0).Sub(prev.Round(0)), now.Sub(prev)
		prev = now
		if slept(wall, mono, every) {
			c.q.CloseWithError(0, "woke from sleep; registering again")
			return
		}
		if n := netPrint(c.sock.Locals()); n != prevNet {
			c.q.CloseWithError(0, "the network changed; registering again")
			return
		}
	}
}

func (c *Client) alt(port int) netip.AddrPort {
	if port <= 0 || port > 65535 {
		return netip.AddrPort{}
	}
	return netip.AddrPortFrom(c.server.Addr(), uint16(port))
}

func parseAddr(s string) (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
}

// Peer is one side's reachability as relayed by the server.
type Peer struct {
	Reflexive netip.AddrPort   // public address of the peer's socket, observed by the server
	Local     []netip.AddrPort // interface addresses the peer reported itself
	NAT       nat.Type         // the peer's own NAT measurement
	// Bridged: the peer reaches the server through its websocket bridge;
	// it has no address to punch and only the relay reaches it.
	Bridged bool
}

// Candidates lists every address worth probing: the reflexive one, which
// the server vouches for, then the self-reported local ones.
func (p Peer) Candidates() []netip.AddrPort {
	if p.Bridged {
		return nil
	}
	var out []netip.AddrPort
	if p.Reflexive.IsValid() {
		out = append(out, p.Reflexive)
	}
	return append(out, p.Local...)
}

func parsePeer(pi *peerInfo) (Peer, error) {
	if pi == nil {
		return Peer{}, errors.New("signal: introduction without peer")
	}
	p := Peer{NAT: nat.ParseType(pi.NAT), Bridged: pi.Via == viaWS}
	var ok bool
	if p.Reflexive, ok = parseAddr(pi.Reflexive); !ok {
		return Peer{}, errors.New("signal: malformed peer address")
	}
	var err error
	// The same filter the server applies, applied again: the server is
	// not trusted to have done it.
	if p.Local, err = parseCandidates(pi.Local, p.Reflexive.Addr()); err != nil {
		return Peer{}, fmt.Errorf("signal: %w", err)
	}
	if p.Bridged {
		p.Local = nil
	}
	return p, nil
}

// Introduction is the result of a successful connect request.
type Introduction struct {
	Nonce     []byte         // shared with the host, for punch probes
	Peer      Peer           // the host
	Reflexive netip.AddrPort // our own public address as the server sees it
	Alt       netip.AddrPort // the server's NAT-probe address; invalid if it has none
	// RelayOffered says the server would relay this session if asked.
	RelayOffered bool
}

// Relay asks the server to relay the session of an introduction whose punch
// failed. It returns the relay's address once the host has attached to it;
// the caller attaches with nat.Socket.BindRelay and dials the host there.
func (c *Client) Relay(ctx context.Context, nonce []byte) (netip.AddrPort, error) {
	return c.RelayAs(ctx, nonce, nil)
}

// RelayAs is Relay with proof that the request comes from the holder of
// id, which the server needs to apply a better tier for devices of an
// account on it. A nil id asks anonymously; the server then never learns
// this machine's identity key.
func (c *Client) RelayAs(ctx context.Context, nonce []byte, id *auth.Identity) (netip.AddrPort, error) {
	req := request{T: "relay", V: protoVersion, Nonce: nonce}
	if id != nil {
		cs := c.q.ConnectionState().TLS
		exp, err := cs.ExportKeyingMaterial(relayExporterLabel, nil, 32)
		if err != nil {
			return netip.AddrPort{}, err
		}
		req.Key, req.Proof = id.Public(), relayProof(id, nonce, exp)
	}
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, req); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	if err != nil {
		return netip.AddrPort{}, err
	}
	if resp.RelayPort <= 0 || resp.RelayPort > 65535 {
		return netip.AddrPort{}, errors.New("signal: malformed relay answer")
	}
	return netip.AddrPortFrom(c.server.Addr(), uint16(resp.RelayPort)), nil
}

// Presence asks the server which of ids are online. The result maps each ID
// to one of the Presence* states.
func (c *Client) Presence(ctx context.Context, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	if len(ids) > MaxPresenceIDs {
		return nil, fmt.Errorf("at most %d IDs per presence request", MaxPresenceIDs)
	}
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		if err := writeMsg(st, request{T: "presence", V: protoVersion, IDs: ids}); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Presence) != len(ids) {
		return nil, errors.New("signal: malformed presence answer")
	}
	out := make(map[string]string, len(ids))
	for i, id := range ids {
		switch p := resp.Presence[i]; p {
		case PresenceOnline, PresenceOffline, PresenceUnknown, PresenceLimited:
			out[id] = p
		default:
			out[id] = PresenceUnknown
		}
	}
	return out, nil
}

// Connect asks the server to introduce us to the host with the given ID.
// local are our own interface candidates and natType our NAT measurement,
// both passed on to the host. Failures reported by the server are *Error.
func (c *Client) Connect(ctx context.Context, id string, local []netip.AddrPort, natType nat.Type) (*Introduction, error) {
	var resp response
	err := c.roundTrip(ctx, func(st *quic.Stream) error {
		req := request{T: "connect", V: protoVersion, ID: id, Candidates: formatCandidates(local), NAT: string(natType)}
		if err := writeMsg(st, req); err != nil {
			return err
		}
		if err := readMsg(st, &resp); err != nil {
			return err
		}
		return resp.err()
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Nonce) != nat.NonceSize {
		return nil, errors.New("signal: malformed introduction")
	}
	peer, err := parsePeer(resp.Peer)
	if err != nil {
		return nil, err
	}
	in := &Introduction{Nonce: resp.Nonce, Peer: peer, Alt: c.alt(resp.AltPort), RelayOffered: resp.Relay}
	in.Reflexive, _ = parseAddr(resp.You)
	return in, nil
}

// Offer is an introduction delivered to a host: a client wants to connect.
type Offer struct {
	Nonce []byte
	Peer  Peer // the client
}

// Answer is the host's reply to an offer.
type Answer struct {
	// Decline, if not empty, refuses the offer with this reason.
	Decline string
	Local   []netip.AddrPort // the host's interface candidates right now
	NAT     nat.Type
}

// ServeOffers handles introductions for a registered host, calling handle
// for each, until the connection ends or ctx is cancelled; it returns the
// reason. handle must be quick: the client is waiting for the answer.
func (c *Client) ServeOffers(ctx context.Context, handle func(Offer) Answer) error {
	for {
		st, err := c.q.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			defer st.Close()
			defer st.CancelRead(0)
			st.SetDeadline(time.Now().Add(offerTimeout))
			var req request
			if err := readMsg(st, &req); err != nil {
				return
			}
			if req.T == "relay" {
				relay := netip.AddrPortFrom(c.server.Addr(), uint16(req.RelayPort))
				if c.OnRelay == nil || len(req.Nonce) != nat.NonceSize || req.RelayPort <= 0 || req.RelayPort > 65535 {
					writeMsg(st, response{Code: CodeNoRelay})
				} else if err := c.OnRelay(req.Nonce, relay); err != nil {
					writeMsg(st, response{Code: CodeNoRelay})
				} else {
					writeMsg(st, response{OK: true})
				}
				return
			}
			if req.T != "offer" {
				return
			}
			peer, err := parsePeer(req.Peer)
			if err != nil || len(req.Nonce) != nat.NonceSize {
				writeMsg(st, response{Code: CodeBadRequest, Error: "malformed offer"})
				return
			}
			ans := handle(Offer{Nonce: req.Nonce, Peer: peer})
			if ans.Decline != "" {
				writeMsg(st, response{Code: CodeDeclined, Error: ans.Decline})
				return
			}
			writeMsg(st, response{OK: true, Candidates: formatCandidates(ans.Local), NAT: string(ans.NAT)})
		}()
	}
}
