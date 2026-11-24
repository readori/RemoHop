package mesh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/svc/vpn"
)

// Node is this device's part of the mesh: its tunnel interface, its links
// to the other devices and its DNS responder. It runs inside the host
// process (see service.go).
type Node struct {
	dir string
	log *slog.Logger
	id  *auth.Identity
	cfg settings

	// dialPeer opens a session to another device's host; dial.Dial by
	// default.
	dialPeer func(ctx context.Context, id string) (*peer.Conn, error)
	// refreshDevices fetches the account's devices (IDs, presence).
	refreshDevices func(ctx context.Context) ([]account.Device, error)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	dev     vpn.Device
	changes *vpn.Changes
	notes   []string
	v6      bool // the interface has its IPv6 address
	names   names
	flows   *flows

	view atomic.Pointer[view]

	mu      sync.Mutex
	links   map[string]*link
	pending map[string]*pendingDial
	backoff map[string]backoffState
	lastErr map[string]string
	learned map[netip.Prefix]string // accepted subnet route → peer key
	devices []account.Device
	exitOn  bool // exit routes installed
	kept    map[netip.Addr]bool
	push    bool // dial every online peer once to hand over a new policy

	restart atomic.Bool // the view changed in a way that needs a new interface
	dirty   chan struct{}
}

// pendingDial holds packets for a peer whose link is coming up: dialled by
// us (dialing), or being accepted from the peer right now.
type pendingDial struct {
	queue   [][]byte
	dialing bool
}

type backoffState struct {
	until time.Time
	delay time.Duration
}

// settings are the net-* config keys in force for one run of a node.
type settings struct {
	exit         bool
	routes       []netip.Prefix
	useExit      string
	acceptRoutes bool
	dns          string
}

func (s settings) equal(o settings) bool {
	return s.exit == o.exit && slices.Equal(s.routes, o.routes) && s.useExit == o.useExit && s.acceptRoutes == o.acceptRoutes && s.dns == o.dns
}

// errStopped is how a stopped node refuses.
var errStopped = errors.New("the mesh is not up on this device")

func (n *Node) currentView() *view { return n.view.Load() }

func (n *Node) markDirty() {
	select {
	case n.dirty <- struct{}{}:
	default:
	}
}

// startNode brings the mesh up on this device.
func startNode(parent context.Context, h *hostd.Host, cfg settings) (*Node, error) {
	dir := h.Dir()
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return nil, err
	}
	n := &Node{dir: dir, log: h.Logger().With("component", "net"), id: id, cfg: cfg, flows: newFlows(),
		links: map[string]*link{}, pending: map[string]*pendingDial{}, backoff: map[string]backoffState{}, lastErr: map[string]string{},
		learned: map[netip.Prefix]string{}, kept: map[netip.Addr]bool{}, dirty: make(chan struct{}, 1)}
	sigAddr, sopts := h.Signal()
	if _, _, err := signal.SplitAddr(sigAddr); err != nil {
		sigAddr = ""
	}
	n.dialPeer = func(ctx context.Context, target string) (*peer.Conn, error) {
		return dial.Dial(ctx, target, dial.Options{Dir: dir, Logger: n.log, Signal: sigAddr, SignalWS: sopts.WS, Transport: sopts.Transport})
	}
	n.refreshDevices = func(ctx context.Context) ([]account.Device, error) {
		d, _, err := account.Devices(ctx, account.Options{Dir: dir, Logger: n.log})
		return d, err
	}
	n.ctx, n.cancel = context.WithCancel(parent)

	// Who is in the mesh: from the account's verified log, without waiting
	// for the server; IDs and presence follow.
	st, err := account.Load(dir)
	if err != nil {
		n.cancel()
		return nil, errNoAccount
	}
	devs, _, _ := account.CachedDevices(dir)
	sp, err := loadPolicy(dir)
	if err != nil {
		n.log.Warn("net: ignoring the stored policy", "err", err)
	}
	v, err := buildView(id.Public(), st, devs, sp)
	if err != nil {
		n.cancel()
		return nil, err
	}
	n.view.Store(v)
	n.devices = devs
	n.setNames(v)

	if err := sysSupported(); err != nil {
		n.cancel()
		return nil, err
	}
	if err := vpn.Privileged(); err != nil {
		n.cancel()
		return nil, err
	}
	if err := checkPrefixFree(v.prefix4); err != nil {
		n.cancel()
		return nil, err
	}
	if n.dev, err = vpn.Open("mesh"); err != nil {
		n.cancel()
		return nil, err
	}
	fail := func(err error) (*Node, error) {
		n.stopSystem()
		n.cancel()
		return nil, err
	}
	if n.changes, err = vpn.NewChanges("mesh", n.dev); err != nil {
		return fail(err)
	}
	v6, notes, err := sysConfigure(n.dev.Name(), netip.PrefixFrom(v.self.Addr4, v.prefix4.Bits()), netip.PrefixFrom(v.self.Addr6, 64), tunnelMTU)
	if err != nil {
		return fail(err)
	}
	n.v6 = v6
	n.setNames(v)
	n.notes = append(n.notes, notes...)
	if cfg.exit || len(cfg.routes) > 0 {
		release, err := vpn.Forward(n.changes, n.dev.Name(), v.prefix4)
		if err != nil {
			return fail(fmt.Errorf("serving as exit node or subnet router: %w", err))
		}
		context.AfterFunc(n.ctx, release)
	}
	// DNS: the responder on our own mesh address, then (net-dns) the
	// system pointed at it for .remohop.
	for _, a := range []netip.Addr{v.self.Addr4, v.self.Addr6} {
		if !a.IsValid() || (a.Is6() && !n.v6) {
			continue
		}
		pc, err := net.ListenPacket("udp", netip.AddrPortFrom(a, 53).String())
		if err != nil {
			if a.Is4() {
				n.notes = append(n.notes, fmt.Sprintf("no DNS responder on %s: %v", a, err))
			}
			continue
		}
		n.wg.Go(func() { n.names.serveDNS(n.ctx, pc) })
	}
	n.notes = append(n.notes, n.setupDNS(v)...)

	n.wg.Go(n.readLoop)
	n.wg.Go(n.maintain)
	n.log.Info("net: up", "dev", n.dev.Name(), "name", v.self.FQDN(), "addr4", v.self.Addr4, "addr6", v.self.Addr6, "peers", len(v.peers))
	n.writeStatus("")
	return n, nil
}

// stop takes the mesh down: links, DNS, routes, firewall rules, interface.
func (n *Node) stop() {
	n.cancel()
	n.mu.Lock()
	links := make([]*link, 0, len(n.links))
	for _, l := range n.links {
		links = append(links, l)
	}
	n.mu.Unlock()
	for _, l := range links {
		l.close(errStopped)
	}
	n.stopSystem()
	n.wg.Wait()
	n.log.Info("net: down")
}

func (n *Node) stopSystem() {
	n.cleanupDNS()
	if n.changes != nil {
		n.changes.Undo()
	}
	if n.dev != nil {
		n.dev.Close()
	}
}

// readLoop moves packets from the interface to the links.
func (n *Node) readLoop() {
	buf := make([]byte, 65536)
	for {
		k, err := n.dev.Read(buf)
		if err != nil {
			if n.ctx.Err() == nil {
				n.log.Error("net: reading the interface failed", "err", err)
				n.cancel()
			}
			return
		}
		pkt := buf[:k]
		p, ok := parsePacket(pkt)
		if !ok || k > tunnelMTU {
			continue
		}
		target := n.route(p.dst)
		if target == nil {
			continue
		}
		n.flows.out(p)
		n.sendTo(target, pkt, p)
	}
}

// route picks the peer a packet for dst goes to: the device that holds
// dst, else a subnet router that offers it, else the exit node in use.
func (n *Node) route(dst netip.Addr) *peerInfo {
	v := n.currentView()
	if p, ok := v.byAddr[dst]; ok {
		if p.Self {
			return nil
		}
		return p
	}
	if v.prefix4.Contains(dst) || v.prefix6.Contains(dst) {
		return nil // an address of nobody in the mesh
	}
	n.mu.Lock()
	best, bestBits := "", -1
	for r, key := range n.learned {
		if r.Contains(dst) && r.Bits() > bestBits {
			best, bestBits = key, r.Bits()
		}
	}
	n.mu.Unlock()
	if best != "" {
		return v.byKey[best]
	}
	if n.cfg.useExit != "" && dst.Is4() {
		if p, ok := v.byName(n.cfg.useExit); ok && !p.Self {
			return p
		}
	}
	return nil
}

// sendTo sends a packet over the peer's link, bringing one up if needed.
func (n *Node) sendTo(p *peerInfo, pkt []byte, k packet) {
	key := string(p.Key)
	n.mu.Lock()
	l := n.links[key]
	if l == nil {
		pd := n.pending[key]
		if pd == nil {
			if b, ok := n.backoff[key]; ok && time.Now().Before(b.until) {
				n.mu.Unlock()
				return
			}
			pd = &pendingDial{dialing: true}
			n.pending[key] = pd
			n.wg.Go(func() { n.connect(p) })
		}
		if len(pd.queue) < queueLen {
			pd.queue = append(pd.queue, bytes.Clone(pkt))
		}
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	clampMSS(pkt, k, l.mss)
	if err := l.send(pkt); err != nil {
		l.dropped.Add(1)
	}
}

// connect brings up a link to p and sends what was queued for it.
func (n *Node) connect(p *peerInfo) {
	key := string(p.Key)
	l, err := n.dialLink(n.ctx, p)
	if err == nil {
		err = n.register(l)
	}
	n.mu.Lock()
	pd := n.pending[key]
	delete(n.pending, key)
	if err != nil {
		b := n.backoff[key]
		b.delay = min(max(2*b.delay, 2*time.Second), time.Minute)
		b.until = time.Now().Add(b.delay)
		n.backoff[key] = b
		n.lastErr[key] = err.Error()
		cur := n.links[key] // the peer's own link may have won meanwhile
		n.mu.Unlock()
		if cur != nil && pd != nil {
			for _, pkt := range pd.queue {
				n.forward(cur, pkt)
			}
		}
		if n.ctx.Err() == nil && cur == nil {
			n.log.Info("net: no link to "+p.Label, "err", err)
		}
		n.markDirty()
		return
	}
	delete(n.backoff, key)
	delete(n.lastErr, key)
	n.mu.Unlock()
	if pd != nil {
		for _, pkt := range pd.queue {
			n.forward(l, pkt)
		}
	}
	n.wg.Go(func() { n.runLink(l) })
}

func (n *Node) forward(l *link, pkt []byte) {
	if k, ok := parsePacket(pkt); ok {
		clampMSS(pkt, k, l.mss)
	}
	if err := l.send(pkt); err != nil {
		l.dropped.Add(1)
	}
}

// preferred decides between two links to the same peer: the one opened by
// the device with the smaller key stays. Both devices decide the same.
func (n *Node) preferred(a, b *link) *link {
	self := n.id.Public()
	opener := func(l *link) []byte {
		if l.dialer {
			return self
		}
		return []byte(l.key)
	}
	if bytes.Compare(opener(a), opener(b)) <= 0 {
		return a
	}
	return b
}

// register makes l the link to its peer, unless a better one exists.
func (n *Node) register(l *link) error {
	if n.ctx.Err() != nil {
		l.close(errStopped)
		return errStopped
	}
	n.mu.Lock()
	old := n.links[l.key]
	if old != nil && !old.closed() && n.preferred(old, l) == old {
		n.mu.Unlock()
		l.close(errors.New("a link to this peer already exists"))
		return errDuplicate
	}
	n.links[l.key] = l
	n.mu.Unlock()
	if old != nil {
		old.close(errors.New("replaced by a new link"))
	}
	n.log.Info("net: link up", "peer", l.label, "path", l.path, "remote", l.remote, "opened_here", l.dialer)
	n.learnRoutes(l)
	n.maybeExit(l)
	n.markDirty()
	return nil
}

var errDuplicate = errors.New("a link to this peer already exists")

// runLink runs l until it ends and forgets it.
func (n *Node) runLink(l *link) {
	err := l.run(n.ctx)
	n.mu.Lock()
	if n.links[l.key] == l {
		delete(n.links, l.key)
	}
	n.mu.Unlock()
	if n.ctx.Err() == nil && !errors.Is(err, errIdle) {
		n.log.Info("net: link down", "peer", l.label, "err", err)
	}
	n.markDirty()
}

// accept serves a link the peer opened (the "net" service handler).
func (n *Node) accept(ctx context.Context, c *peer.Conn, s peer.Stream, key ed25519.PublicKey, h *hello) error {
	refuse := func(msg string) error {
		writeHello(s, &hello{V: protoVersion, Error: msg})
		return errors.New(msg)
	}
	if n.ctx.Err() != nil {
		return refuse(errStopped.Error())
	}
	v := n.currentView()
	p, err := checkHello(v, key, h)
	if err != nil {
		return refuse(err.Error())
	}
	rep, own := n.helloFor(c)
	l := newLink(n, key, p.Label, c, s, false, own, h.MaxDatagram, h)
	l.path = n.pathOf(c)
	n.mu.Lock()
	old := n.links[string(key)]
	if old != nil && !old.closed() && n.preferred(old, l) == old {
		n.mu.Unlock()
		return refuse(errDuplicate.Error())
	}
	// Until the link is registered, packets for the peer (our answers to
	// what it sends the moment it has our hello) wait here instead of
	// making us dial a second link.
	pd := n.pending[string(key)]
	if pd == nil {
		pd = &pendingDial{}
		n.pending[string(key)] = pd
	}
	n.mu.Unlock()
	takeQueue := func() [][]byte {
		n.mu.Lock()
		defer n.mu.Unlock()
		q := pd.queue
		pd.queue = nil
		if !pd.dialing && n.pending[string(key)] == pd {
			delete(n.pending, string(key))
		}
		return q
	}
	if err := writeHello(s, rep); err != nil {
		takeQueue()
		return err
	}
	n.offerPolicy(l, h.Policy)
	if err := n.register(l); err != nil {
		takeQueue()
		return err
	}
	// Also what our own dial, if one is under way, had queued: it will
	// end as a duplicate or replace this link, either way harmlessly.
	for _, pkt := range takeQueue() {
		n.forward(l, pkt)
	}
	n.runLink(l)
	return nil
}

// deliver hands a packet that arrived over l to the interface, if the
// mesh's rules let it in.
func (n *Node) deliver(l *link, pkt []byte) {
	k, ok := parsePacket(pkt)
	if !ok || len(pkt) > tunnelMTU {
		l.dropped.Add(1)
		return
	}
	if !n.admit(l, k) {
		l.dropped.Add(1)
		return
	}
	clampMSS(pkt, k, l.mss)
	if _, err := n.dev.Write(pkt); err != nil {
		l.dropped.Add(1)
		return
	}
	l.rx.Add(1)
	l.rxBytes.Add(int64(len(pkt)))
	l.active.Store(time.Now().UnixNano())
}

// admit is the receiving side's whole decision about a packet from l's
// peer: may it come from where it says, go where it is headed, and does the
// policy let it in.
func (n *Node) admit(l *link, k packet) bool {
	v := n.currentView()
	from, ok := v.byKey[l.key]
	if !ok {
		return false // removed from the account meanwhile
	}
	self := v.self
	ownSrc := k.src == from.Addr4 || (from.Addr6.IsValid() && k.src == from.Addr6)
	toSelf := k.dst == self.Addr4 || (self.Addr6.IsValid() && k.dst == self.Addr6)
	switch {
	case toSelf && ownSrc:
	case toSelf:
		// From behind the peer: a machine on its subnet, or the internet
		// if it is our exit node. Only answers to what we sent.
		if !n.routedVia(l, k.src) {
			return false
		}
		return n.flows.reply(k) || k.isICMPReply()
	case ownSrc && (n.cfg.exit || n.servesRoute(k.dst)):
		// To be forwarded by us, as subnet router or exit node.
		if !forwardable(k.dst) || v.prefix4.Contains(k.dst) || v.prefix6.Contains(k.dst) {
			return false
		}
		return k.isICMPReply() || v.rules.allows(from.device(), self.device(), k.dst, true, k)
	default:
		return false
	}
	if n.flows.reply(k) || k.isICMPReply() {
		return true
	}
	return v.rules.allows(from.device(), self.device(), k.dst, false, k)
}

// routedVia reports whether addresses behind l's peer may be the source of
// packets to us: it is our exit node, or offers a route we accepted.
func (n *Node) routedVia(l *link, src netip.Addr) bool {
	if l.exit && n.cfg.useExit != "" {
		if p, ok := n.currentView().byName(n.cfg.useExit); ok && string(p.Key) == l.key {
			return true
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for r, key := range n.learned {
		if key == l.key && r.Contains(src) {
			return true
		}
	}
	return false
}

func (n *Node) servesRoute(dst netip.Addr) bool {
	for _, r := range n.cfg.routes {
		if r.Contains(dst) {
			return true
		}
	}
	return false
}

// forwardable: an address a peer may reach through us at all.
func forwardable(a netip.Addr) bool {
	return a.Is4() && !a.IsLoopback() && !a.IsMulticast() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() &&
		a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// learnRoutes installs the subnets l's peer offers, with net-accept-routes.
func (n *Node) learnRoutes(l *link) {
	if !n.cfg.acceptRoutes || len(l.routes) == 0 {
		return
	}
	v := n.currentView()
	for _, r := range l.routes {
		if r.Overlaps(v.prefix4) {
			continue
		}
		n.mu.Lock()
		have, known := n.learned[r]
		if !known {
			n.learned[r] = l.key
		}
		n.mu.Unlock()
		if known {
			if have != l.key {
				n.log.Warn("net: two devices offer the same route; keeping the first", "route", r, "ignored", l.label)
			}
			continue
		}
		if err := sysRoute(n.dev.Name(), r); err != nil {
			n.log.Warn("net: could not add a route through the mesh", "route", r, "via", l.label, "err", err)
			continue
		}
		n.log.Info("net: route through the mesh", "route", r, "via", l.label)
	}
}

// maybeExit installs the default route through the mesh once the link to
// the exit node in use is up (net-use-exit).
func (n *Node) maybeExit(l *link) {
	if n.cfg.useExit == "" {
		return
	}
	v := n.currentView()
	p, ok := v.byName(n.cfg.useExit)
	if !ok || string(p.Key) != l.key {
		return
	}
	if !l.exit {
		n.log.Warn("net: the device named by net-use-exit does not offer to be an exit node (`remohop net up --exit` there)", "peer", l.label)
		return
	}
	n.mu.Lock()
	if n.exitOn {
		n.mu.Unlock()
		return
	}
	n.exitOn = true
	// The sessions the mesh runs on must keep their own way out: every
	// current link, and the rendezvous server for future ones.
	var keep []netip.Addr
	for _, x := range n.links {
		if a := x.remote.Addr(); a.IsValid() && a.Is4() && !n.kept[a] {
			keep = append(keep, a)
			n.kept[a] = true
		}
	}
	n.mu.Unlock()
	keep = append(keep, n.signalAddrs()...)
	notes, err := vpn.ClientRoutes(n.changes, n.dev.Name(), nil, true, keep, nil, nil)
	if err != nil {
		n.log.Error("net: could not route through the exit node", "peer", l.label, "err", err)
		n.mu.Lock()
		n.exitOn = false
		n.mu.Unlock()
		return
	}
	for _, note := range notes {
		n.log.Info("net: " + note)
	}
	n.log.Info("net: all traffic goes through the exit node", "peer", l.label)
}

// signalAddrs are the rendezvous server's addresses (and its websocket
// bridge's), which must stay off an exit route.
func (n *Node) signalAddrs() []netip.Addr {
	var out []netip.Addr
	addr, opts, _ := signal.Resolve("")
	hosts := []string{}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		hosts = append(hosts, h)
	}
	if opts.WS != "" {
		if u := strings.TrimPrefix(strings.TrimPrefix(opts.WS, "wss://"), "ws://"); u != "" {
			h := strings.SplitN(u, "/", 2)[0]
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			hosts = append(hosts, h)
		}
	}
	for _, h := range hosts {
		ctx, cancel := context.WithTimeout(n.ctx, 3*time.Second)
		ips, _ := net.DefaultResolver.LookupNetIP(ctx, "ip4", h)
		cancel()
		for _, ip := range ips {
			ip = ip.Unmap()
			n.mu.Lock()
			if !n.kept[ip] {
				n.kept[ip] = true
				out = append(out, ip)
			}
			n.mu.Unlock()
		}
	}
	return out
}

// pathOf names the path of a link's session.
func (n *Node) pathOf(c *peer.Conn) string {
	if info := dial.InfoOf(c); info != nil {
		if info.Transport == dial.TransportWebSocket {
			return string(info.Path) + " (websocket)"
		}
		return string(info.Path)
	}
	if s := hostd.SessionOf(c); s != nil {
		return s.Path
	}
	return ""
}

// onControl handles a control message from a peer.
func (n *Node) onControl(l *link, c control) {
	if c.Policy != nil {
		n.offerPolicy(l, c.Policy)
	}
}

// offerPolicy adopts sp if it is valid and newer than ours, and passes it
// on to the other links.
func (n *Node) offerPolicy(from *link, sp *SignedPolicy) {
	if sp == nil {
		return
	}
	v := n.currentView()
	p, err := v.verifyPolicy(sp)
	if err != nil {
		if from != nil {
			n.log.Warn("net: ignoring a policy from a peer", "peer", from.label, "err", err)
		}
		return
	}
	if !newer(sp, p.Version, v.signed, v.version) {
		return
	}
	if err := savePolicy(n.dir, sp); err != nil {
		n.log.Warn("net: could not store the policy", "err", err)
	}
	n.adoptPolicy(sp, p)
	n.spread(from, sp)
}

// adoptPolicy builds the view anew with sp.
func (n *Node) adoptPolicy(sp *SignedPolicy, p *Policy) {
	old := n.currentView()
	nv := *old
	nv.setPolicy(sp)
	pfx, _ := ParsePrefix4(p.Prefix4)
	if pfx != old.prefix4 {
		// Every address changes: the interface has to be set up anew.
		n.restart.Store(true)
	}
	n.view.Store(&nv)
	n.log.Info("net: policy adopted", "version", p.Version, "rules", len(p.Rules), "signer", auth.Fingerprint(sp.Signer))
	n.markDirty()
}

// spread sends sp to every link but from.
func (n *Node) spread(from *link, sp *SignedPolicy) {
	n.mu.Lock()
	var ls []*link
	for _, l := range n.links {
		if l != from {
			ls = append(ls, l)
		}
	}
	n.mu.Unlock()
	for _, l := range ls {
		l.sendControl(control{Policy: sp})
	}
}

// maintain keeps the view current, pinned links up and the status file
// fresh.
func (n *Node) maintain() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var lastRefresh time.Time
	policyAt := policyMtime(n.dir)
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-t.C:
		case <-n.dirty:
		}
		if time.Since(lastRefresh) > 30*time.Second {
			lastRefresh = time.Now()
			n.refresh()
		}
		// A policy set locally (`remohop net policy set`) while we run.
		if m := policyMtime(n.dir); !m.Equal(policyAt) {
			policyAt = m
			if sp, err := loadPolicy(n.dir); err == nil && sp != nil {
				v := n.currentView()
				if p, err := v.verifyPolicy(sp); err == nil && newer(sp, p.Version, v.signed, v.version) {
					n.adoptPolicy(sp, p)
					n.spread(nil, sp)
					n.mu.Lock()
					n.push = true
					n.mu.Unlock()
				}
			}
		}
		n.keepPinned()
		n.writeStatus("")
	}
}

// refresh fetches the account's devices and rebuilds the view.
func (n *Node) refresh() {
	ctx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	devs, err := n.refreshDevices(ctx)
	cancel()
	if err != nil {
		n.log.Debug("net: could not refresh the account's devices", "err", err)
		devs, _, _ = account.CachedDevices(n.dir)
	}
	st, err := account.Load(n.dir)
	if err != nil {
		return
	}
	old := n.currentView()
	nv, err := buildView(n.id.Public(), st, devs, old.signed)
	if err != nil {
		n.log.Warn("net: the account no longer allows a mesh here", "err", err)
		n.restart.Store(true)
		return
	}
	if nv.prefix4 != old.prefix4 || nv.self.Addr4 != old.self.Addr4 {
		n.restart.Store(true)
	}
	n.view.Store(nv)
	n.setNames(nv)
	n.mu.Lock()
	n.devices = devs
	var gone []*link
	for k, l := range n.links {
		if !nv.members[k] {
			gone = append(gone, l)
		}
	}
	n.mu.Unlock()
	for _, l := range gone {
		n.log.Info("net: closing the link of a device that left the account", "peer", l.label)
		l.close(errors.New("no longer a member of the account"))
	}
	n.rewriteHosts(nv)
}

// keepPinned dials the links that are kept up whatever the traffic: the
// exit node in use, every online device with net-accept-routes (to learn
// their routes), and every online device once after a policy change.
func (n *Node) keepPinned() {
	v := n.currentView()
	n.mu.Lock()
	push := n.push
	n.push = false
	n.mu.Unlock()
	for _, p := range v.peers {
		pin := n.cfg.acceptRoutes && p.Online
		if n.cfg.useExit != "" {
			if e, ok := v.byName(n.cfg.useExit); ok && e == p {
				pin = true
			}
		}
		if !pin && !(push && p.Online) {
			continue
		}
		key := string(p.Key)
		n.mu.Lock()
		l, dialing := n.links[key], n.pending[key] != nil
		b, backing := n.backoff[key]
		if l != nil {
			if pin {
				l.pinned.Store(true)
			}
			n.mu.Unlock()
			continue
		}
		if dialing || (backing && time.Now().Before(b.until)) {
			n.mu.Unlock()
			continue
		}
		n.pending[key] = &pendingDial{dialing: true}
		n.mu.Unlock()
		n.wg.Go(func() {
			n.connect(p)
			if pin {
				n.mu.Lock()
				if l := n.links[key]; l != nil {
					l.pinned.Store(true)
				}
				n.mu.Unlock()
			}
		})
	}
}

// snapshot describes the node for the status file.
func (n *Node) snapshot(errText string) Status {
	v := n.currentView()
	st := Status{Up: n.ctx.Err() == nil, Error: errText, Name: v.self.Label, FQDN: v.self.FQDN(), Addr4: v.self.Addr4.String(),
		Prefix4: v.prefix4.String(), Prefix6: v.prefix6.String(), Identity: v.self.FP, Exit: n.cfg.exit, UseExit: n.cfg.useExit,
		AcceptRoutes: n.cfg.acceptRoutes, DNS: n.cfg.dns, Notes: n.notes, PolicyVersion: v.version, PolicyRules: -1}
	if v.self.Addr6.IsValid() && n.v6 {
		st.Addr6 = v.self.Addr6.String()
	}
	if n.dev != nil {
		st.Device = n.dev.Name()
	}
	if v.policy != nil {
		st.PolicyRules = len(v.policy.Rules)
		st.PolicySigner = auth.Fingerprint(v.signed.Signer)
	}
	for _, r := range n.cfg.routes {
		st.Routes = append(st.Routes, r.String())
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st.ExitActive = n.exitOn
	for r, key := range n.learned {
		if p, ok := v.byKey[key]; ok {
			st.Learned = append(st.Learned, r.String()+" via "+p.Label)
		}
	}
	sort.Strings(st.Learned)
	for _, p := range v.peers {
		ps := PeerStatus{Name: p.Label, FQDN: p.FQDN(), Identity: p.FP, ID: p.ID, Addr4: p.Addr4.String(), Online: p.Online,
			LastError: n.lastErr[string(p.Key)]}
		if p.Addr6.IsValid() {
			ps.Addr6 = p.Addr6.String()
		}
		if l := n.links[string(p.Key)]; l != nil {
			ps.Link, ps.Path, ps.Since = "up", l.path, l.since.UTC().Truncate(time.Second)
			if l.remote.IsValid() {
				ps.Remote = l.remote.String()
			}
			ps.Tx, ps.Rx, ps.Dropped = l.tx.Load(), l.rx.Load(), l.dropped.Load()
			ps.Exit = l.exit
			for _, r := range l.routes {
				ps.Routes = append(ps.Routes, r.String())
			}
			ps.OpenedHere = l.dialer
		} else if n.pending[string(p.Key)] != nil {
			ps.Link = "connecting"
		}
		st.Peers = append(st.Peers, ps)
	}
	return st
}

// setNames gives the DNS responder the view's names; IPv6 addresses only
// where this device has IPv6 on its interface.
func (n *Node) setNames(v *view) {
	m := v.names()
	if !n.v6 {
		for name, addrs := range m {
			var keep []netip.Addr
			for _, a := range addrs {
				if a.Is4() {
					keep = append(keep, a)
				}
			}
			m[name] = keep
		}
	}
	n.names.set(m)
}
