package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// SettingExit is the host setting that allows clients to use this machine
// as their exit to networks it is not directly attached to, the internet
// included. It is separate from the "vpn" feature switch, and off unless
// the owner turns it on: reaching the host's own LAN is one grant, sending
// arbitrary traffic out under the host's address is a bigger one.
const SettingExit = "vpn-exit"

func init() {
	perm.Register(Service, perm.VPN)
	peer.Handle(Service, serve)
	config.RegisterSetting(config.Setting{Key: SettingExit, Default: "off", Validate: config.ValidateOnOff,
		Help: "let VPN clients reach networks beyond this host's own LAN, including the internet (`vpn up --exit`)"})
}

// reserved are the tunnel networks in use by this process, so that
// tunnels being set up at the same moment do not pick the same one.
var reserved struct {
	mu   sync.Mutex
	nets map[netip.Prefix]bool
}

func reserve(candidates []netip.Prefix, used []netip.Prefix) (netip.Prefix, bool) {
	reserved.mu.Lock()
	defer reserved.mu.Unlock()
	if reserved.nets == nil {
		reserved.nets = map[netip.Prefix]bool{}
	}
next:
	for _, c := range candidates {
		if reserved.nets[c] {
			continue
		}
		for _, u := range used {
			if u.Overlaps(c) {
				continue next
			}
		}
		reserved.nets[c] = true
		return c, true
	}
	return netip.Prefix{}, false
}

func release(n netip.Prefix) {
	reserved.mu.Lock()
	delete(reserved.nets, n)
	reserved.mu.Unlock()
}

type sessionKey struct{}

func exitAllowed() bool {
	v, _ := config.Get(SettingExit)
	return config.Bool(v, false)
}

// serve is the host end of a tunnel.
func serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	refuse := func(format string, args ...any) error {
		msg := fmt.Sprintf(format, args...)
		wire.WriteJSON(s, Reply{Error: msg})
		return errors.New(msg)
	}
	var req Request
	if err := peer.DecodeReq(raw, &req); err != nil || req.V != protoVersion {
		return refuse("unsupported VPN request (version %d)", req.V)
	}
	routes, err := parseRoutes(req.Routes)
	if err != nil {
		return refuse("%v", err)
	}
	if len(req.Nets) == 0 || len(req.Nets) > maxCandidates {
		return refuse("malformed VPN request")
	}
	var candidates []netip.Prefix
	for _, n := range req.Nets {
		p, ok := parseNet(n)
		if !ok {
			return refuse("malformed VPN request")
		}
		candidates = append(candidates, p)
	}

	// What may this client reach? The host's own networks with the vpn
	// feature alone (the session layer has already checked that switch);
	// anything further only if the owner allows exit traffic.
	forwarding := req.Exit || len(routes) > 0
	if forwarding {
		if err := sys.hostSupported(); err != nil {
			return refuse("%v", err)
		}
		nets := connected()
		beyond := req.Exit
		for _, r := range routes {
			if !isLocalRoute(r, nets) {
				beyond = true
			}
		}
		if beyond && !exitAllowed() {
			what := "routes beyond the host's own networks"
			if req.Exit {
				what = "using the host as the exit for all traffic"
			}
			return refuse("the host does not allow %s (its owner can allow it with `remohop config vpn-exit on`)", what)
		}
	}
	if err := sys.privileged(); err != nil {
		return refuse("the host cannot create a tunnel: %v", err)
	}
	v, _ := c.LoadOrStoreValue(sessionKey{}, new(atomic.Bool))
	if inUse := v.(*atomic.Bool); !inUse.CompareAndSwap(false, true) {
		return refuse("this session already has a VPN tunnel")
	} else {
		defer inUse.Store(false)
	}

	network, ok := reserve(candidates, sys.usedNets())
	if !ok {
		return refuse("none of the proposed tunnel networks is free on the host")
	}
	defer release(network)
	hostIP, clientIP := endpoints(network)

	l := &link{s: s}
	var own int
	l.d, own = datagrams(c)
	mtu, mss := tunnelMTU, pickMSS(req.MaxDatagram, own)

	dev, err := sys.open("host:" + c.RemoteID())
	if err != nil {
		return refuse("the host cannot create a tunnel: %v", err)
	}
	defer dev.Close()
	st, err := newState("host", dev.Name())
	if err != nil {
		return refuse("the host cannot record the tunnel: %v", err)
	}
	if u, ok := dev.(interface{ undoSteps() []undoStep }); ok {
		for _, step := range u.undoSteps() {
			st.record(step)
		}
	}
	defer st.undo()
	if err := sys.configure(dev.Name(), hostIP, clientIP, mtu); err != nil {
		slog.Error("vpn: configuring the tunnel interface failed", "err", err)
		return refuse("the host could not configure its tunnel interface")
	}
	if forwarding {
		defer sys.hostRelease()
		if err := sys.hostForward(st, dev.Name(), clientIP); err != nil {
			slog.Error("vpn: setting up forwarding failed", "err", err)
			return refuse("the host could not set up forwarding for the tunnel")
		}
	}
	rep := Reply{Net: network.String(), MTU: mtu, MSS: mss}
	if req.Exit {
		rep.DNS = sys.resolvers()
	}
	if err := wire.WriteJSON(s, rep); err != nil {
		return err
	}
	slog.Info("vpn: tunnel up", "peer", c.RemoteID(), "dev", dev.Name(), "net", network, "routes", req.Routes, "exit", req.Exit, "mtu", mtu, "mss", mss)

	// From the tunnel into this machine and beyond: only the client's own
	// address as source, only granted destinations. Into the tunnel: only
	// to the client's address.
	in := policy{src: only(clientIP), dst: within(routes, req.Exit, hostIP)}
	out := policy{src: anyAddr, dst: only(clientIP)}
	var stats Stats
	err = pump(ctx, dev, l, mtu, mss, own > 0 && req.MaxDatagram > 0, out, in, &stats)
	slog.Info("vpn: tunnel down", "peer", c.RemoteID(), "dev", dev.Name(), "sent", stats.Sent.Load(), "received", stats.Received.Load(),
		"refused", stats.DroppedIn.Load())
	return err
}
