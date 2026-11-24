package mesh

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
)

// Config keys.
const (
	SettingNet          = "net"
	SettingExit         = "net-exit"
	SettingRoutes       = "net-routes"
	SettingUseExit      = "net-use-exit"
	SettingAcceptRoutes = "net-accept-routes"
	SettingDNS          = "net-dns"
)

// DNS modes (net-dns).
const (
	DNSAuto  = "auto"  // per-interface split DNS for .remohop where the system has it (systemd-resolved)
	DNSHosts = "hosts" // a marked block in the hosts file
	DNSOff   = "off"   // only the responder on the mesh address
)

func validateRoutes(v string) error {
	_, err := parseRoutes(v)
	return err
}

func parseRoutes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		p, err := netip.ParsePrefix(f)
		if err != nil || !p.Addr().Is4() {
			return nil, fmt.Errorf("%q is not an IPv4 network like 192.168.1.0/24", f)
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("%s has host bits set; did you mean %s?", p, p.Masked())
		}
		if p.Bits() == 0 {
			return nil, errors.New("0.0.0.0/0 is not a route; use --exit to offer this device as an exit node")
		}
		if len(out) == 32 {
			return nil, errors.New("at most 32 routes")
		}
		out = append(out, p)
	}
	return out, nil
}

func init() {
	config.RegisterSetting(config.Setting{Key: SettingNet, Default: "off", Validate: config.ValidateOnOff,
		Help: "join the mesh of this account's devices (`remohop net up`); runs inside the host daemon"})
	config.RegisterSetting(config.Setting{Key: SettingExit, Default: "off", Validate: config.ValidateOnOff,
		Help: "offer this device to the mesh as an exit node for all traffic (Linux)"})
	config.RegisterSetting(config.Setting{Key: SettingRoutes, Validate: validateRoutes,
		Help: "networks this device routes for the mesh, comma separated (subnet router, Linux)"})
	config.RegisterSetting(config.Setting{Key: SettingUseExit,
		Help: "send all traffic through this mesh device (its name), which must offer to be an exit node"})
	config.RegisterSetting(config.Setting{Key: SettingAcceptRoutes, Default: "off", Validate: config.ValidateOnOff,
		Help: "use the networks other mesh devices route for (net-routes there)"})
	config.RegisterSetting(config.Setting{Key: SettingDNS, Default: DNSAuto,
		Validate: func(v string) error {
			if v != DNSAuto && v != DNSHosts && v != DNSOff {
				return errors.New("must be auto, hosts or off")
			}
			return nil
		},
		Help: "how names like laptop.remohop resolve: auto (systemd-resolved split DNS where present), hosts (a block in the hosts file), off"})

	perm.Register(Service, perm.Always)
	peer.Handle(Service, serve)
	hostd.RegisterScope(hostd.Scope{Name: "net", Services: []string{Service, hostd.ServicePing}, Admit: admit})
	hostd.RegisterExtension(run)
}

func readSettings(dir string) (on bool, s settings, err error) {
	values, err := config.AllIn(dir)
	if err != nil {
		return false, s, err
	}
	on = config.Bool(values[SettingNet], false)
	s.exit = config.Bool(values[SettingExit], false)
	s.acceptRoutes = config.Bool(values[SettingAcceptRoutes], false)
	s.useExit = strings.TrimSpace(values[SettingUseExit])
	s.dns = values[SettingDNS]
	if s.dns == "" {
		s.dns = DNSAuto
	}
	s.routes, err = parseRoutes(values[SettingRoutes])
	return on, s, err
}

// current is the node of this process's host, if the mesh is up.
var current atomic.Pointer[Node]

// run is the host extension: it starts and stops the node as the net
// setting says, for as long as the host runs.
func run(ctx context.Context, h *hostd.Host) {
	var (
		n       *Node
		cfg     settings
		lastErr string
		retryAt time.Time
	)
	stop := func() {
		if n != nil {
			current.Store(nil)
			n.stop()
			n = nil
		}
	}
	defer func() {
		stop()
		removeStatus(h.Dir())
	}()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		on, s, err := readSettings(h.Dir())
		switch {
		case err != nil:
			// A broken setting must not take a running mesh down.
		case !on:
			if n != nil {
				stop()
			}
			if lastErr != "" || statusExists(h.Dir()) {
				lastErr = ""
				removeStatus(h.Dir())
			}
		case n != nil && (!s.equal(cfg) || n.restart.Load() || n.ctx.Err() != nil):
			h.Logger().Info("net: settings or account changed; restarting the mesh")
			stop()
			retryAt = time.Time{}
			continue
		case n == nil && time.Now().After(retryAt):
			nn, err := startNode(ctx, h, s)
			if err != nil {
				if err.Error() != lastErr {
					h.Logger().Warn("net: the mesh cannot come up", "err", err)
				}
				lastErr = err.Error()
				writeErrorStatus(h.Dir(), err)
				retryAt = time.Now().Add(15 * time.Second)
				break
			}
			lastErr = ""
			n, cfg = nn, s
			current.Store(n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshes limits the account refreshes an unknown key can cause.
var refreshes struct {
	mu   sync.Mutex
	last time.Time
}

// admit is the login scope: a key of a member of this device's account may
// log in for the mesh while the mesh is up here.
func admit(h *hostd.Host, key ed25519.PublicKey) (string, bool) {
	n := current.Load()
	if n == nil || n.ctx.Err() != nil {
		return "", false
	}
	v := n.currentView()
	if p, ok := v.byKey[string(key)]; ok && !p.Self {
		return "net:" + p.Label, true
	}
	// Perhaps a device that joined the account since our last look:
	// refresh in the background, so its next attempt finds it.
	refreshes.mu.Lock()
	if time.Since(refreshes.last) > 10*time.Second {
		refreshes.last = time.Now()
		go func() {
			ctx, cancel := context.WithTimeout(n.ctx, 15*time.Second)
			defer cancel()
			account.Refresh(ctx, account.Options{Dir: n.dir, Logger: n.log})
			n.refresh()
		}()
	}
	refreshes.mu.Unlock()
	return "", false
}

// serve is the host side of a link.
func serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	refuse := func(msg string) error {
		writeHello(s, &hello{V: protoVersion, Error: msg})
		return errors.New(msg)
	}
	sess := hostd.SessionOf(c)
	if sess == nil {
		return refuse("not available on this connection")
	}
	var h hello
	if err := peer.DecodeReq(raw, &h); err != nil {
		return refuse("malformed request")
	}
	n := current.Load()
	if n == nil {
		return refuse(errStopped.Error() + " (its owner can start it with `remohop net up`)")
	}
	return n.accept(ctx, c, s, sess.Auth.ClientKey, &h)
}
