// Package wake is the RemoHop Wake-on-LAN service: the magic packet itself, a
// host handler that sends it on the host's LAN, a client API, and the
// `remohop wake` / `remohop <ID> wol` commands.
//
// The useful form is the remote one. A sleeping PC cannot be reached over the
// internet, but a small always-on machine on the same LAN can broadcast the
// magic packet for it.
package wake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

const serviceName = "wake"

// DefaultPort is the UDP "discard" port most network cards listen on.
const DefaultPort = 9

const (
	macLen       = 6
	packetLen    = 6 + 16*macLen
	maxTargets   = 64              // broadcast destinations per request
	sendDeadline = 3 * time.Second // one UDP write never blocks long; this is a backstop
	// A single broadcast frame is easily lost (Wi-Fi, a switch port coming
	// out of power saving), so the magic packet goes out sendRepeats times,
	// sendGap apart, as other wake tools do.
	sendRepeats = 3
	sendGap     = 100 * time.Millisecond
	hostBurst    = 10              // remote requests per window a host accepts
	hostWindow   = time.Minute     // length of that window
)

// Options tune a send. The zero value broadcasts on every IPv4 interface to
// port 9.
type Options struct {
	// Broadcast is the destination IPv4 address, usually a subnet broadcast
	// address such as 192.168.1.255. Empty means every broadcast-capable
	// interface of this machine. Only the local (CLI) path honours it: a host
	// never lets a remote peer pick where it sends packets.
	Broadcast string `json:"-"`
	// Port is the UDP destination port; 0 means DefaultPort.
	Port int `json:"port,omitempty"`
}

// Result reports what was sent.
type Result struct {
	MAC     string   `json:"mac"`
	Port    int      `json:"port"`
	Targets []string `json:"targets"` // destinations the packet was sent to
}

// ParseMAC accepts aa:bb:cc:dd:ee:ff, aa-bb-cc-dd-ee-ff, aabb.ccdd.eeff and
// bare aabbccddeeff, in either case. The all-zero and all-ones addresses are
// refused: neither is a machine.
func ParseMAC(s string) (net.HardwareAddr, error) {
	s = strings.TrimSpace(s)
	if len(s) > 17 {
		return nil, fmt.Errorf("invalid MAC address %q", clip(s))
	}
	var hexs string
	switch {
	case len(s) == 12:
		hexs = s
	case len(s) == 17 && (s[2] == ':' || s[2] == '-'):
		sep := s[2:3]
		hexs = strings.ReplaceAll(s, sep, "")
		if len(hexs) != 12 || strings.Count(s, sep) != 5 {
			return nil, fmt.Errorf("invalid MAC address %q", clip(s))
		}
	case len(s) == 14 && s[4] == '.' && s[9] == '.':
		hexs = strings.ReplaceAll(s, ".", "")
	default:
		return nil, fmt.Errorf("invalid MAC address %q (use aa:bb:cc:dd:ee:ff)", clip(s))
	}
	mac := make(net.HardwareAddr, macLen)
	for i := range mac {
		hi, ok1 := unhex(hexs[2*i])
		lo, ok2 := unhex(hexs[2*i+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("invalid MAC address %q (not hexadecimal)", clip(s))
		}
		mac[i] = hi<<4 | lo
	}
	if allEqual(mac, 0x00) || allEqual(mac, 0xff) {
		return nil, fmt.Errorf("invalid MAC address %q (not a unicast address)", clip(s))
	}
	return mac, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func allEqual(b []byte, v byte) bool {
	for _, c := range b {
		if c != v {
			return false
		}
	}
	return true
}

// clip bounds text echoed in an error so a huge argument cannot flood output.
func clip(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// Packet builds the magic packet: six 0xFF bytes then the MAC sixteen times.
func Packet(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, packetLen)
	for range 6 {
		p = append(p, 0xff)
	}
	for range 16 {
		p = append(p, mac...)
	}
	return p
}

func normalizePort(p int) (int, error) {
	if p == 0 {
		return DefaultPort, nil
	}
	if p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid port %d", p)
	}
	return p, nil
}

// destination is one send: the local address to bind (so the packet leaves
// through the intended interface) and where it goes.
type destination struct {
	src net.IP // nil: let the OS choose
	dst net.IP
}

// Send broadcasts the magic packet for mac from this machine.
func Send(ctx context.Context, mac string, opts Options) (*Result, error) {
	hw, err := ParseMAC(mac)
	if err != nil {
		return nil, err
	}
	port, err := normalizePort(opts.Port)
	if err != nil {
		return nil, err
	}
	var dests []destination
	if opts.Broadcast != "" {
		ip := net.ParseIP(opts.Broadcast).To4()
		if ip == nil {
			return nil, fmt.Errorf("invalid broadcast address %q (need an IPv4 address)", clip(opts.Broadcast))
		}
		dests = []destination{{dst: ip}}
	} else {
		dests = interfaceBroadcasts()
		if len(dests) == 0 {
			// No usable interface was found; the limited broadcast at least
			// reaches the default route's network.
			dests = []destination{{dst: net.IPv4bcast}}
		}
	}

	pkt := Packet(hw)
	res := &Result{MAC: hw.String(), Port: port}
	var errs []error
	for _, d := range dests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := sendOne(d, port, pkt); err != nil {
			errs = append(errs, err)
			continue
		}
		res.Targets = append(res.Targets, d.dst.String())
	}
	if len(res.Targets) == 0 {
		return nil, fmt.Errorf("wake: could not send the magic packet: %w", errors.Join(errs...))
	}
	for i := 1; i < sendRepeats; i++ {
		select {
		case <-ctx.Done():
			return res, nil // the first round went out
		case <-time.After(sendGap):
		}
		for _, d := range dests {
			sendOne(d, port, pkt) // the first round decided what works
		}
	}
	return res, nil
}

func sendOne(d destination, port int, pkt []byte) error {
	var local *net.UDPAddr
	if d.src != nil {
		local = &net.UDPAddr{IP: d.src}
	}
	c, err := net.DialUDP("udp4", local, &net.UDPAddr{IP: d.dst, Port: port})
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetWriteDeadline(time.Now().Add(sendDeadline))
	_, err = c.Write(pkt)
	return err
}

// interfaceBroadcasts lists a directed-broadcast destination for every up,
// broadcast-capable, non-loopback IPv4 address, bound to that address.
func interfaceBroadcasts() []destination {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []destination
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || len(ipn.Mask) != net.IPv4len && len(ipn.Mask) != net.IPv6len {
				continue
			}
			mask := ipn.Mask
			if len(mask) == net.IPv6len {
				mask = mask[12:]
			}
			if ones, _ := mask.Size(); ones == 0 || ones >= 31 {
				continue // no subnet to broadcast on (/31 and /32 have no broadcast address)
			}
			bc := make(net.IP, net.IPv4len)
			for i := range bc {
				bc[i] = ip[i] | ^mask[i]
			}
			out = append(out, destination{src: ip, dst: bc})
			if len(out) == maxTargets {
				return out
			}
		}
	}
	return out
}

// ---- remote -------------------------------------------------------------

type request struct {
	MAC  string `json:"mac"`
	Port int    `json:"port,omitempty"`
}

type reply struct {
	Result
	Error string `json:"error,omitempty"`
}

// Remote asks the host behind conn to send the magic packet on its LAN. Only
// opts.Port is used; the host decides the destinations.
func Remote(ctx context.Context, conn *peer.Conn, mac string, opts Options) (*Result, error) {
	hw, err := ParseMAC(mac)
	if err != nil {
		return nil, err
	}
	port, err := normalizePort(opts.Port)
	if err != nil {
		return nil, err
	}
	s, err := conn.Open(ctx, serviceName, request{MAC: hw.String(), Port: port})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()

	var rep reply
	if err := wire.ReadJSONLimit(s, &rep, 64<<10); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("wake: %w", err)
	}
	if rep.Error != "" {
		return nil, fmt.Errorf("wake: host: %s", rep.Error)
	}
	if len(rep.Targets) > maxTargets {
		rep.Targets = rep.Targets[:maxTargets]
	}
	return &rep.Result, nil
}

// sendFn is Send, replaceable in tests so they never broadcast on a real LAN.
var sendFn = Send

// limiter bounds how often a peer can make the host emit packets.
var limiter = struct {
	sync.Mutex
	start time.Time
	n     int
}{}

func allow(now time.Time) bool {
	limiter.Lock()
	defer limiter.Unlock()
	if now.Sub(limiter.start) > hostWindow {
		limiter.start, limiter.n = now, 0
	}
	if limiter.n >= hostBurst {
		return false
	}
	limiter.n++
	return true
}

func init() {
	peer.Handle(serviceName, handle)
}

func handle(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, reply{Error: "bad request"})
	}
	if !allow(time.Now()) {
		return wire.WriteJSON(s, reply{Error: "too many wake requests, try again in a minute"})
	}
	// Broadcast is deliberately not taken from the peer: an authenticated
	// client must not be able to aim packets at arbitrary addresses.
	res, err := sendFn(ctx, req.MAC, Options{Port: req.Port})
	if err != nil {
		return wire.WriteJSON(s, reply{Error: err.Error()})
	}
	return wire.WriteJSON(s, reply{Result: *res})
}
