package portmap

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// lanAddr reports whether ip is an address only a local network uses:
// RFC 1918 or unique-local, or link-local. Only such an address may be a
// gateway we talk to or the host of a UPnP URL we fetch; a public address
// never is (a forged SSDP answer must not make us send requests to
// somebody on the internet, or to a service on this machine).
func lanAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// dialGateway opens a UDP socket connected to server. Its local address
// is the one this machine sends to the gateway from, which is the LAN
// address the router forwards to.
func dialGateway(ctx context.Context, server netip.AddrPort) (*net.UDPConn, netip.Addr, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp4", server.String())
	if err != nil {
		return nil, netip.Addr{}, err
	}
	uc := c.(*net.UDPConn)
	local := uc.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if !local.Is4() {
		uc.Close()
		return nil, netip.Addr{}, errors.New("no IPv4 address towards the gateway")
	}
	return uc, local, nil
}

// localIPFor is the address this machine sends to ip from.
func localIPFor(ip netip.Addr) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	c, local, err := dialGateway(ctx, netip.AddrPortFrom(ip, 9))
	if err != nil {
		return netip.Addr{}, err
	}
	c.Close()
	return local, nil
}

// exchange sends req to the connected socket c until a reply that accept
// takes arrives, retransmitting at doubling intervals (RFC 6886 starts at
// 250 ms), for at most wait or until ctx ends. Datagrams that accept
// refuses are ignored; a connected UDP socket only receives from the
// address it is connected to.
func exchange(ctx context.Context, c *net.UDPConn, req []byte, wait time.Duration, accept func([]byte) bool) ([]byte, error) {
	deadline := time.Now().Add(wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	stop := context.AfterFunc(ctx, func() { c.SetReadDeadline(time.Now()) })
	defer stop()
	buf := make([]byte, 1100) // the largest PCP message is 1100 bytes
	interval := 250 * time.Millisecond
	for {
		if _, err := c.Write(req); err != nil {
			return nil, err
		}
		next := time.Now().Add(interval)
		if next.After(deadline) {
			next = deadline
		}
		c.SetReadDeadline(next)
		if ctx.Err() != nil { // ended before the deadline above was set
			return nil, ctx.Err()
		}
		for {
			n, err := c.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					break
				}
				return nil, err // e.g. ICMP port unreachable: nobody listens
			}
			if accept(buf[:n]) {
				return buf[:n], nil
			}
		}
		if !time.Now().Before(deadline) {
			return nil, errors.New("no answer")
		}
		interval *= 2
	}
}
