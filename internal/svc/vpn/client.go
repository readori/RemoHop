package vpn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Options says what a tunnel should carry.
type Options struct {
	// Routes are host-side networks to reach through the tunnel.
	Routes []netip.Prefix
	// Exit sends all traffic through the host, except the local networks,
	// the session's own path and Bypass.
	Exit bool
	// Bypass are networks that keep their current route when Exit is set.
	Bypass []netip.Prefix
}

// Tunnel is an established tunnel, seen from the client.
type Tunnel struct {
	Device string       // interface name
	Local  netip.Addr   // this machine's tunnel address
	Peer   netip.Addr   // the host's tunnel address
	Net    netip.Prefix // the tunnel network
	MTU    int
	MSS    int // TCP connections through the tunnel are clamped to this
	// Notes are things the user should know (which network was chosen
	// instead of the default, what happened to DNS).
	Notes []string
	Stats Stats

	run func(ctx context.Context) error
}

// Run carries packets until ctx ends, the session is lost or the host
// closes the tunnel, then removes everything Up set up. It returns nil
// when ctx ended it.
func (t *Tunnel) Run(ctx context.Context) error { return t.run(ctx) }

// Up asks the host on c for a tunnel and configures this machine's end.
// The caller must call Run, which also cleans up.
func Up(ctx context.Context, c *peer.Conn, opts Options) (*Tunnel, error) {
	if err := sys.privileged(); err != nil {
		return nil, err
	}
	for _, list := range [][]netip.Prefix{opts.Routes, opts.Bypass} {
		for _, r := range list {
			if !r.Addr().Is4() || r != r.Masked() {
				return nil, fmt.Errorf("%s is not an IPv4 network", r)
			}
		}
	}
	used := sys.usedNets()
	candidates := freeNets(used, maxCandidates)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no free tunnel network left in %s on this machine", pool)
	}
	l := &link{}
	var own int
	l.d, own = datagrams(c)
	req := Request{V: protoVersion, Exit: opts.Exit, MaxDatagram: own}
	for _, n := range candidates {
		req.Nets = append(req.Nets, n.String())
	}
	for _, r := range opts.Routes {
		req.Routes = append(req.Routes, r.String())
	}
	if _, err := parseRoutes(req.Routes); err != nil {
		return nil, err
	}

	s, err := c.Open(ctx, Service, req)
	if err != nil {
		return nil, err
	}
	l.s = s
	stop := context.AfterFunc(ctx, s.Reset)
	var rep Reply
	err = wire.ReadJSONLimit(s, &rep, 4096)
	if !stop() {
		err = ctx.Err()
	}
	if err != nil {
		s.Reset()
		return nil, fmt.Errorf("vpn: %w", err)
	}
	if rep.Error != "" {
		s.Close()
		return nil, errors.New(auth.CleanText(rep.Error, 300))
	}
	// The host chooses among our proposals and nothing else.
	network, ok := parseNet(rep.Net)
	proposed := false
	for _, n := range candidates {
		proposed = proposed || n == network
	}
	if !ok || !proposed || rep.MTU != tunnelMTU || rep.MSS < minMSS || rep.MSS > maxMSS {
		s.Reset()
		return nil, errors.New("vpn: the host answered with a tunnel network or MTU that was not on offer")
	}
	hostIP, clientIP := endpoints(network)
	t := &Tunnel{Local: clientIP, Peer: hostIP, Net: network, MTU: rep.MTU, MSS: rep.MSS}
	if network != candidates[0] || network.Addr() != pool.Addr() {
		t.Notes = append(t.Notes, fmt.Sprintf("%s is in use on one of the two machines; the tunnel uses %s instead", netip.PrefixFrom(pool.Addr(), 30), network))
	}

	dev, err := sys.open("client:" + c.RemoteID())
	if err != nil {
		s.Reset()
		return nil, err
	}
	t.Device = dev.Name()
	st, err := newState("client", dev.Name())
	if err != nil {
		dev.Close()
		s.Reset()
		return nil, err
	}
	if u, ok := dev.(interface{ undoSteps() []undoStep }); ok {
		for _, step := range u.undoSteps() {
			st.record(step)
		}
	}
	fail := func(err error) (*Tunnel, error) {
		dev.Close()
		st.undo()
		s.Reset()
		return nil, err
	}
	if err := sys.configure(dev.Name(), clientIP, hostIP, rep.MTU); err != nil {
		return fail(err)
	}
	routing := clientRouting{routes: opts.Routes, exit: opts.Exit, bypass: opts.Bypass}
	if opts.Exit {
		// The session must keep its own way to the host (or the relay).
		if ua, ok := c.RemoteAddr().(*net.UDPAddr); ok {
			if a, ok := netip.AddrFromSlice(ua.IP); ok && a.Unmap().Is4() {
				routing.keep = append(routing.keep, a.Unmap())
			}
		}
		for _, d := range rep.DNS {
			// Resolvers the host offers are used only if they are plain
			// addresses; at most three.
			if a, err := netip.ParseAddr(d); err == nil && a.Is4() && len(routing.dns) < 3 {
				routing.dns = append(routing.dns, a)
			}
		}
	}
	notes, err := sys.clientRoutes(st, dev.Name(), routing)
	if err != nil {
		return fail(err)
	}
	t.Notes = append(t.Notes, notes...)

	// Out of this machine: only packets the kernel itself sourced from the
	// tunnel address, so nothing of another network is forwarded into the
	// tunnel. Into it: only packets addressed to the tunnel address, from
	// where the user asked to reach; a host cannot use the tunnel to talk
	// to this machine's other addresses or the networks behind it.
	out := policy{src: only(clientIP), dst: within(opts.Routes, opts.Exit, hostIP)}
	in := policy{src: within(opts.Routes, opts.Exit, hostIP), dst: only(clientIP)}
	datagrams := own > 0
	t.run = func(ctx context.Context) error {
		defer st.undo()
		// The session ending ends the tunnel.
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-c.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		err := pump(ctx, dev, l, rep.MTU, rep.MSS, datagrams, out, in, &t.Stats)
		select {
		case <-c.Done():
			return errors.New("the session to the host was lost; the tunnel is down")
		default:
		}
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return t, nil
}
