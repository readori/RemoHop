// Package portmap asks the local router to forward one UDP port to this
// machine, so that a host behind a NAT has a public address clients can
// reach whatever their own NAT does.
//
// Hole punching needs at least one side whose NAT keeps the same public
// port for every destination. Between two symmetric NATs, or a symmetric
// one and a strict one, it fails and the session goes through the relay.
// Most home routers, though, let a program on the LAN open a port: through
// PCP (RFC 6887), its predecessor NAT-PMP (RFC 6886), or UPnP's Internet
// Gateway Device profile. A host that maps its punching socket's port has
// an address (the router's public IP and the mapped port) that answers
// unsolicited packets, which any client can probe.
//
// A Mapper keeps one such mapping for the lifetime of a host: it tries PCP,
// then NAT-PMP, then UPnP, in the background and within a few seconds;
// renews the lease at half its lifetime; looks again, with backoff, when
// the router stops answering; and deletes the mapping when it is closed.
// Everything it reads from the network is treated as hostile: replies are
// accepted only from the gateway it asked, UPnP descriptions only from LAN
// addresses (see lanURL), and every read is bounded in size and time.
//
// All three protocols are implemented here with the standard library;
// RemoHop links no UPnP or NAT-PMP library.
package portmap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// Setting is the config key that turns router port mapping off for this
// machine's host. REMOHOP_PORTMAP=0 overrides it to off.
const Setting = "portmap"

func init() {
	config.RegisterSetting(config.Setting{Key: Setting, Default: "on", Validate: config.ValidateOnOff,
		Help: "ask the router (PCP, NAT-PMP or UPnP) to forward the host's UDP port, so clients behind any NAT can reach it directly"})
}

// Allowed reports whether a host keeping its config in dir may map a port
// on the router: unless REMOHOP_PORTMAP says off, or the portmap setting
// does.
func Allowed(dir string) bool {
	switch strings.ToLower(os.Getenv("REMOHOP_PORTMAP")) {
	case "0", "off", "no", "false":
		return false
	}
	var values map[string]string
	if dir == "" {
		values, _ = config.All()
	} else {
		values, _ = config.AllIn(dir)
	}
	return config.Bool(values[Setting], true)
}

// Protocol names, as Mapping.Protocol reports them.
const (
	ProtoPCP    = "PCP"
	ProtoNATPMP = "NAT-PMP"
	ProtoUPnP   = "UPnP"
)

const (
	// requestLifetime is the lease asked for. Routers may grant less.
	requestLifetime = 2 * time.Hour
	// attemptTimeout bounds one round of discovery and mapping across all
	// three protocols.
	attemptTimeout = 3 * time.Second
	// minRenew keeps a router that grants a lease of a few seconds from
	// turning renewal into a busy loop; minRetry and maxRetry bound the
	// backoff after a round that found nothing.
	minRenew = 30 * time.Second
	minRetry = time.Minute
	maxRetry = 30 * time.Minute
	// closeTimeout bounds the deletion of the mapping when the Mapper is
	// closed.
	closeTimeout = time.Second
	// description is how the mapping is labelled in a UPnP router's list.
	description = "RemoHop"
)

// Mapping is a port forwarding the router agreed to.
type Mapping struct {
	Protocol string         // ProtoPCP, ProtoNATPMP or ProtoUPnP
	External netip.AddrPort // the router's public address and the port it forwards
	Internal netip.AddrPort // the LAN address and port it forwards to
	Lifetime time.Duration  // as granted; 0 for a UPnP mapping without expiry
}

func (m Mapping) String() string {
	return fmt.Sprintf("%s %s -> :%d", m.Protocol, m.External, m.Internal.Port())
}

// lease is a mapping in force, with what it takes to renew or delete it.
type lease interface {
	renew(ctx context.Context) (Mapping, error)
	remove(ctx context.Context) error
}

// Config says what to map and how.
type Config struct {
	// Port is the local UDP port to forward.
	Port uint16
	// Logger gets one Info line per new mapping, Debug for the rest; nil
	// discards.
	Logger *slog.Logger
	// OnChange, if set, is called (from the Mapper's goroutine) whenever
	// the mapping appears, changes or goes away.
	OnChange func()
}

// Mapper keeps one UDP port mapping on the local router; see the package
// comment.
type Mapper struct {
	cfg Config
	log *slog.Logger
	env env

	kick   chan struct{}
	cancel context.CancelFunc
	done   chan struct{}
	closed sync.Once

	mu    sync.Mutex
	cur   Mapping // valid External while a mapping is in force
	lease lease   // the run loop's lease, for Close
}

// env is what the Mapper takes from its surroundings; tests replace it.
type env struct {
	gateway  func() (netip.Addr, error)
	ssdp     netip.AddrPort // where M-SEARCH goes
	pmpPort  uint16         // the gateway's PCP and NAT-PMP port
	lanOK    func(netip.Addr) bool
	lifetime time.Duration
	minRenew time.Duration
	minRetry time.Duration
	maxRetry time.Duration
	timeout  time.Duration // attemptTimeout
	pmpWait  time.Duration // how long PCP and NAT-PMP each wait for an answer
	ssdpWait time.Duration // how long SSDP collects answers
}

func defaultEnv() env {
	return env{
		gateway:  defaultGateway,
		ssdp:     netip.MustParseAddrPort("239.255.255.250:1900"),
		pmpPort:  5351,
		lanOK:    lanAddr,
		lifetime: requestLifetime,
		minRenew: minRenew,
		minRetry: minRetry,
		maxRetry: maxRetry,
		timeout:  attemptTimeout,
		pmpWait:  600 * time.Millisecond,
		ssdpWait: time.Second,
	}
}

// Start begins mapping cfg.Port in the background. It never blocks: the
// first attempt runs in its own goroutine.
func Start(cfg Config) *Mapper { return start(cfg, defaultEnv()) }

func start(cfg Config, e env) *Mapper {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Mapper{
		cfg:    cfg,
		log:    cfg.Logger,
		env:    e,
		kick:   make(chan struct{}, 1),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	go m.run(ctx)
	return m
}

// Mapping returns the mapping in force, if any.
func (m *Mapper) Mapping() (Mapping, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur, m.cur.External.IsValid()
}

// Refresh makes the Mapper check its mapping now instead of at the next
// renewal: the machine moved to another network, or woke from sleep. A
// lease the router no longer honours is replaced by a fresh discovery.
func (m *Mapper) Refresh() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Close stops the Mapper and deletes the mapping, giving the router about
// a second to confirm. OnChange is not called for it.
func (m *Mapper) Close() error {
	m.closed.Do(func() {
		m.cancel()
		<-m.done
		m.mu.Lock()
		l := m.lease
		m.lease = nil
		m.mu.Unlock()
		if l != nil {
			ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			defer cancel()
			if err := l.remove(ctx); err != nil {
				m.log.Debug("router port mapping: could not delete it", "err", err)
			}
		}
		m.mu.Lock()
		m.cur = Mapping{}
		m.mu.Unlock()
	})
	return nil
}

func (m *Mapper) run(ctx context.Context) {
	defer close(m.done)
	retry := m.env.minRetry
	var l lease
	for {
		var cur Mapping
		var err error
		if l != nil {
			rctx, cancel := context.WithTimeout(ctx, m.env.timeout)
			cur, err = l.renew(rctx)
			cancel()
			if err != nil && ctx.Err() == nil {
				m.log.Debug("router port mapping: renewal failed; looking again", "err", err)
				l = nil
			}
		}
		if l == nil && ctx.Err() == nil {
			cur, l, err = m.attempt(ctx)
		}
		m.mu.Lock()
		m.lease = l
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}

		var wait time.Duration
		if err != nil {
			if _, had := m.Mapping(); had {
				m.log.Debug("router port mapping lost", "err", err)
			} else {
				m.log.Debug("no router port mapping", "err", err, "retry_in", retry)
			}
			m.set(Mapping{})
			wait = retry
			retry = min(retry*2, m.env.maxRetry)
		} else {
			m.set(cur)
			retry = m.env.minRetry
			wait = m.renewAfter(cur.Lifetime)
		}

		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-m.kick:
			t.Stop()
			retry = m.env.minRetry
		}
	}
}

// renewAfter is when to renew a lease granted for lifetime: at half of
// it, but not sooner than minRenew. A mapping without expiry is refreshed
// as if it had the lifetime asked for, which notices a router that
// rebooted and forgot it.
func (m *Mapper) renewAfter(lifetime time.Duration) time.Duration {
	if lifetime <= 0 {
		lifetime = m.env.lifetime
	}
	return max(lifetime/2, m.env.minRenew)
}

// set records the mapping in force (none if cur.External is invalid),
// logs a new one and tells OnChange.
func (m *Mapper) set(cur Mapping) {
	m.mu.Lock()
	prev := m.cur
	m.cur = cur
	m.mu.Unlock()
	if prev.External == cur.External && prev.Internal == cur.Internal && prev.Protocol == cur.Protocol {
		return
	}
	if cur.External.IsValid() {
		m.log.Info("router port mapping: "+cur.String(), "lifetime", cur.Lifetime)
	}
	if m.cfg.OnChange != nil {
		m.cfg.OnChange()
	}
}

// attempt runs one round of discovery: PCP, then NAT-PMP, at the default
// gateway, then UPnP at whatever device answered SSDP. Discovery for UPnP
// starts at once, alongside the other two, so a router that speaks only
// UPnP costs no more time than one that speaks PCP. Without a known
// gateway, the UPnP device's address stands in for it.
func (m *Mapper) attempt(parent context.Context) (Mapping, lease, error) {
	ctx, cancel := context.WithTimeout(parent, m.env.timeout)
	defer cancel()

	type found struct {
		dev *igd
		err error
	}
	upnpCh := make(chan found, 1)
	go func() {
		dev, err := m.discoverIGD(ctx)
		upnpCh <- found{dev, err}
	}()
	var upnp *found
	waitUPnP := func() found {
		if upnp == nil {
			f := <-upnpCh
			upnp = &f
		}
		return *upnp
	}

	var errs []error
	gw, err := m.env.gateway()
	if err != nil || !gw.Is4() {
		if f := waitUPnP(); f.dev != nil {
			gw = f.dev.ip
		} else {
			gw = netip.Addr{}
			errs = append(errs, fmt.Errorf("default gateway: %v", err))
		}
	}
	if gw.IsValid() && !m.env.lanOK(gw) {
		errs = append(errs, fmt.Errorf("default gateway %s is not a LAN address; not asking it", gw))
	} else if gw.IsValid() {
		server := netip.AddrPortFrom(gw, m.env.pmpPort)
		cur, l, err := m.mapPCP(ctx, server)
		if err == nil {
			return cur, l, nil
		}
		errs = append(errs, fmt.Errorf("PCP: %w", err))
		if cur, l, err = m.mapNATPMP(ctx, server); err == nil {
			return cur, l, nil
		}
		errs = append(errs, fmt.Errorf("NAT-PMP: %w", err))
	}
	f := waitUPnP()
	if f.err != nil {
		errs = append(errs, fmt.Errorf("UPnP: %w", f.err))
		return Mapping{}, nil, errors.Join(errs...)
	}
	cur, l, err := m.mapUPnP(ctx, f.dev)
	if err != nil {
		errs = append(errs, fmt.Errorf("UPnP: %w", err))
		return Mapping{}, nil, errors.Join(errs...)
	}
	return cur, l, nil
}
