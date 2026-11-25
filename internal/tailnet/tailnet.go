// Package tailnet detects a running Tailscale on this machine, without
// depending on it: no Tailscale code is linked in, and nothing here needs
// Tailscale to be installed.
//
// RemoHop uses what it finds in two places:
//
//   - When both ends of a session are on the same tailnet, the peer's
//     tailnet address is a direct path that works through any NAT (Tailscale
//     carries it, directly or through its own relays). internal/dial prefers
//     it over a hole-punched path and tries it before the rendezvous
//     server's relay. It is only an additional candidate address: the
//     RemoHop session over it is the same end-to-end encrypted, authenticated
//     QUIC session as over any other path, and nothing Tailscale says is
//     taken as proof of who the peer is.
//   - `remohop doctor` explains the network situation, Tailscale included.
//
// Sources, in order: $REMOHOP_TAILSCALE_STATUS (a file in the format of
// `tailscale status --json`, for tests and odd installations), the
// tailscaled local API socket, the `tailscale` command, and finally the
// network interfaces alone (an interface named like Tailscale's with an
// address in 100.64.0.0/10 or fd7a:115c:a1e0::/48). REMOHOP_TAILSCALE=off turns
// all of it off.
package tailnet

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// The address ranges Tailscale assigns from: carrier-grade NAT space for
// IPv4 and its own unique-local prefix for IPv6.
var (
	CGNAT = netip.MustParsePrefix("100.64.0.0/10")
	ULA   = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// InRange reports whether a is in one of the ranges Tailscale assigns. An
// address in range is not necessarily a tailnet address: carriers use
// 100.64.0.0/10 too.
func InRange(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && (CGNAT.Contains(a) || ULA.Contains(a))
}

// IsInterface reports whether an interface looks like Tailscale's: named
// tailscale0 (Linux, BSD) or "Tailscale" (Windows), or a macOS utun, and
// holding an address in Tailscale's ranges. The name alone is not enough
// (any VPN may create a utun), nor is the address (a carrier may hand out
// 100.64.0.0/10 addresses).
func IsInterface(name string, addrs []netip.Addr) bool {
	lower := strings.ToLower(name)
	if !strings.Contains(lower, "tailscale") && !strings.HasPrefix(lower, "utun") {
		return false
	}
	for _, a := range addrs {
		if InRange(a) {
			return true
		}
	}
	return false
}

// Node is a machine on the tailnet.
type Node struct {
	Name    string       `json:"name"`
	DNSName string       `json:"dns_name,omitempty"`
	OS      string       `json:"os,omitempty"`
	IPs     []netip.Addr `json:"ips"`
	Online  bool         `json:"online"`
	// ExitNode: this node is the exit node in use; ExitNodeOption: it
	// offers to be one.
	ExitNode       bool `json:"exit_node,omitempty"`
	ExitNodeOption bool `json:"exit_node_option,omitempty"`
	// CurAddr is the address Tailscale talks to it at directly, Relay the
	// Tailscale relay (DERP region) it goes through otherwise.
	CurAddr string `json:"cur_addr,omitempty"`
	Relay   string `json:"relay,omitempty"`
}

// Sources of a Status.
const (
	SourceDisabled  = "disabled"  // REMOHOP_TAILSCALE=off
	SourceEnv       = "env"       // $REMOHOP_TAILSCALE_STATUS
	SourceLocalAPI  = "localapi"  // tailscaled's local API socket
	SourceCLI       = "cli"       // `tailscale status --json`
	SourceInterface = "interface" // only an interface was found
	SourceNone      = ""          // nothing found
)

// Status is what was found about Tailscale on this machine.
type Status struct {
	// Running: Tailscale is up and this machine is on a tailnet.
	Running bool   `json:"running"`
	Source  string `json:"source"`
	// BackendState is Tailscale's own word ("Running", "Stopped",
	// "NeedsLogin", ...), empty when only an interface was found.
	BackendState   string `json:"backend_state,omitempty"`
	Version        string `json:"version,omitempty"`
	Tailnet        string `json:"tailnet,omitempty"`
	MagicDNSSuffix string `json:"magic_dns_suffix,omitempty"`
	MagicDNS       bool   `json:"magic_dns,omitempty"`
	Self           Node   `json:"self"`
	Peers          []Node `json:"peers,omitempty"`
	// ExitNode names the exit node this machine sends its traffic
	// through, if any.
	ExitNode string `json:"exit_node,omitempty"`
	// Interface is the name of the tailnet interface, if one was found.
	Interface string `json:"interface,omitempty"`
	// Note explains a source that failed, for `remohop doctor`.
	Note string `json:"note,omitempty"`
}

// HasPeers reports whether the status lists the tailnet's other machines
// (it came from Tailscale itself, not only from an interface).
func (s *Status) HasPeers() bool {
	return s.Source == SourceEnv || s.Source == SourceLocalAPI || s.Source == SourceCLI
}

// Peer returns the tailnet peer that holds a, if the status lists it.
func (s *Status) Peer(a netip.Addr) (*Node, bool) {
	a = a.Unmap()
	for i := range s.Peers {
		for _, ip := range s.Peers[i].IPs {
			if ip == a {
				return &s.Peers[i], true
			}
		}
	}
	return nil, false
}

// IsSelf reports whether a is one of this machine's tailnet addresses.
func (s *Status) IsSelf(a netip.Addr) bool {
	a = a.Unmap()
	for _, ip := range s.Self.IPs {
		if ip == a {
			return true
		}
	}
	return false
}

// Disabled reports whether REMOHOP_TAILSCALE=off.
func Disabled() bool {
	v := strings.ToLower(os.Getenv("REMOHOP_TAILSCALE"))
	return v == "off" || v == "0" || v == "false" || v == "no"
}

// cache keeps one detection for a few seconds: a dial may ask more than
// once, and the CLI is not free.
var cache struct {
	mu sync.Mutex
	at time.Time
	st *Status
}

const cacheFor = 5 * time.Second

// Detect finds out whether Tailscale runs here. It never fails: what could
// not be found is reported in the Status. ctx bounds the local API and CLI
// queries.
func Detect(ctx context.Context) *Status {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.st != nil && time.Since(cache.at) < cacheFor {
		c := *cache.st
		return &c
	}
	st := detect(ctx)
	cache.st, cache.at = st, time.Now()
	c := *st
	return &c
}

// ResetCache forgets the cached detection (tests, `remohop doctor`).
func ResetCache() {
	cache.mu.Lock()
	cache.st = nil
	cache.mu.Unlock()
}

func detect(ctx context.Context) *Status {
	if Disabled() {
		return &Status{Source: SourceDisabled}
	}
	ifname, ifaddrs := findInterface()
	var notes []string
	if p := os.Getenv("REMOHOP_TAILSCALE_STATUS"); p != "" {
		b, err := os.ReadFile(p)
		if err == nil {
			var st *Status
			if st, err = Parse(b); err == nil {
				st.Source, st.Interface = SourceEnv, ifname
				return st
			}
		}
		notes = append(notes, fmt.Sprintf("REMOHOP_TAILSCALE_STATUS: %v", err))
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if b, err := localAPI(ctx); err == nil {
		if st, err := Parse(b); err == nil {
			st.Source, st.Interface = SourceLocalAPI, ifname
			return st
		}
	} else if !errors.Is(err, errNoSocket) {
		notes = append(notes, "local API: "+err.Error())
	}
	if b, err := cliStatus(ctx); err == nil {
		if st, err := Parse(b); err == nil {
			st.Source, st.Interface = SourceCLI, ifname
			return st
		} else {
			notes = append(notes, "tailscale status --json: "+err.Error())
		}
	} else if !errors.Is(err, exec.ErrNotFound) {
		notes = append(notes, "tailscale status --json: "+err.Error())
	}
	st := &Status{Note: strings.Join(notes, "; ")}
	if ifname != "" {
		st.Running, st.Source, st.Interface = true, SourceInterface, ifname
		st.Self = Node{Name: hostname(), IPs: ifaddrs, Online: true}
	}
	return st
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// findInterface returns the first interface that looks like Tailscale's
// and its addresses in Tailscale's ranges.
func findInterface() (string, []netip.Addr) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs := interfaceAddrs(ifc)
		if IsInterface(ifc.Name, addrs) {
			var in []netip.Addr
			for _, a := range addrs {
				if InRange(a) {
					in = append(in, a)
				}
			}
			return ifc.Name, in
		}
	}
	return "", nil
}

func interfaceAddrs(ifc net.Interface) []netip.Addr {
	as, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range as {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

// LocalAddrs returns this machine's tailnet addresses as seen on its
// interfaces, without asking Tailscale. Cheap enough for every dial.
func LocalAddrs() []netip.Addr {
	if Disabled() {
		return nil
	}
	_, addrs := findInterface()
	return addrs
}

// raw is the part of `tailscale status --json` (ipnstate.Status) read here.
type raw struct {
	Version        string             `json:"Version"`
	BackendState   string             `json:"BackendState"`
	TailscaleIPs   []string           `json:"TailscaleIPs"`
	Self           *rawPeer           `json:"Self"`
	MagicDNSSuffix string             `json:"MagicDNSSuffix"`
	CurrentTailnet *rawTailnet        `json:"CurrentTailnet"`
	Peer           map[string]rawPeer `json:"Peer"`
	ExitNodeStatus *struct {
		TailscaleIPs []string `json:"TailscaleIPs"`
		Online       bool     `json:"Online"`
	} `json:"ExitNodeStatus"`
}

type rawTailnet struct {
	Name            string `json:"Name"`
	MagicDNSSuffix  string `json:"MagicDNSSuffix"`
	MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
}

type rawPeer struct {
	HostName       string   `json:"HostName"`
	DNSName        string   `json:"DNSName"`
	OS             string   `json:"OS"`
	TailscaleIPs   []string `json:"TailscaleIPs"`
	Online         bool     `json:"Online"`
	ExitNode       bool     `json:"ExitNode"`
	ExitNodeOption bool     `json:"ExitNodeOption"`
	CurAddr        string   `json:"CurAddr"`
	Relay          string   `json:"Relay"`
}

// maxPeers bounds what is kept of a status: a tailnet can be large, and
// only addresses are looked up.
const maxPeers = 4096

func parseIPs(in []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range in {
		// ExitNodeStatus lists prefixes ("100.1.2.3/32").
		if p, err := netip.ParsePrefix(s); err == nil {
			s = p.Addr().String()
		}
		if a, err := netip.ParseAddr(s); err == nil && InRange(a) {
			out = append(out, a.Unmap())
		}
	}
	return out
}

func (p rawPeer) node() Node {
	return Node{Name: clean(p.HostName), DNSName: clean(strings.TrimSuffix(p.DNSName, ".")), OS: clean(p.OS), IPs: parseIPs(p.TailscaleIPs),
		Online: p.Online, ExitNode: p.ExitNode, ExitNodeOption: p.ExitNodeOption, CurAddr: clean(p.CurAddr), Relay: clean(p.Relay)}
}

// clean keeps text from Tailscale printable and short.
func clean(s string) string {
	if len(s) > 128 {
		s = s[:128]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// Parse reads the output of `tailscale status --json` (or the local API's
// status). Fields RemoHop does not use are ignored.
func Parse(b []byte) (*Status, error) {
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("not a Tailscale status: %w", err)
	}
	if r.BackendState == "" && r.Self == nil {
		return nil, errors.New("not a Tailscale status (no BackendState)")
	}
	st := &Status{BackendState: clean(r.BackendState), Version: clean(r.Version), MagicDNSSuffix: clean(r.MagicDNSSuffix)}
	st.Running = r.BackendState == "Running"
	if r.CurrentTailnet != nil {
		st.Tailnet = clean(r.CurrentTailnet.Name)
		st.MagicDNS = r.CurrentTailnet.MagicDNSEnabled
		if st.MagicDNSSuffix == "" {
			st.MagicDNSSuffix = clean(r.CurrentTailnet.MagicDNSSuffix)
		}
	}
	if r.Self != nil {
		st.Self = r.Self.node()
	}
	if len(st.Self.IPs) == 0 {
		st.Self.IPs = parseIPs(r.TailscaleIPs)
	}
	keys := make([]string, 0, len(r.Peer))
	for k := range r.Peer {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(st.Peers) == maxPeers {
			break
		}
		n := r.Peer[k].node()
		st.Peers = append(st.Peers, n)
		if n.ExitNode {
			st.ExitNode = n.Name
		}
	}
	if st.ExitNode == "" && r.ExitNodeStatus != nil {
		if ips := parseIPs(r.ExitNodeStatus.TailscaleIPs); len(ips) > 0 {
			st.ExitNode = ips[0].String()
		}
	}
	return st, nil
}

var errNoSocket = errors.New("no tailscaled socket")

// socketPaths are where tailscaled's local API listens on Unix systems.
func socketPaths() []string {
	if p := os.Getenv("REMOHOP_TAILSCALE_SOCKET"); p != "" {
		return []string{p}
	}
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd":
		return []string{"/var/run/tailscale/tailscaled.sock", "/run/tailscale/tailscaled.sock"}
	case "darwin":
		// The open-source tailscaled; the App Store app has no socket
		// here and is reached through its CLI instead.
		return []string{"/var/run/tailscaled.socket", "/var/run/tailscale/tailscaled.sock"}
	}
	return nil
}

// localAPI asks tailscaled's local API for its status.
func localAPI(ctx context.Context) ([]byte, error) {
	var path string
	for _, p := range socketPaths() {
		if fi, err := os.Stat(p); err == nil && fi.Mode()&os.ModeSocket != 0 {
			path = p
			break
		}
	}
	if path == "" {
		return nil, errNoSocket
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", path)
	}}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr}
	// The local API checks the Host header against this name.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(bufio.NewReader(resp.Body), 16<<20))
}

// cliPath finds the tailscale command.
func cliPath() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	var extra []string
	switch runtime.GOOS {
	case "darwin":
		extra = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"}
	case "windows":
		extra = []string{os.Getenv("ProgramFiles") + `\Tailscale\tailscale.exe`}
	}
	for _, p := range extra {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func cliStatus(ctx context.Context) ([]byte, error) {
	p, err := cliPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, p, "status", "--json")
	out, err := cmd.Output()
	if err != nil {
		// `tailscale status` exits non-zero when stopped but may still
		// print a status.
		if len(out) > 0 && json.Valid(out) {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

// Warm starts a detection in the background when this machine has a
// tailnet interface, so that the Prefer of a dial in progress finds the
// answer cached (or being fetched) instead of starting `tailscale status`
// (up to 2 s on Windows and the macOS App Store build) between the
// introduction and the first probe, while the host's priming hold runs.
func Warm() {
	if Disabled() || (os.Getenv("REMOHOP_TAILSCALE_STATUS") == "" && len(LocalAddrs()) == 0) {
		return
	}
	go Detect(context.Background())
}

// Prefer returns a predicate for the candidates of a peer that are worth
// preferring because they are on this machine's tailnet, or nil when there
// are none: the peer offers no address in Tailscale's ranges, or this
// machine is not on a tailnet. When Tailscale itself could be asked, only
// addresses of machines it lists as peers qualify; with nothing but an
// interface to go on, any address in its ranges does.
func Prefer(ctx context.Context, remote []netip.AddrPort) func(netip.AddrPort) bool {
	if Disabled() {
		return nil
	}
	any := false
	for _, r := range remote {
		any = any || InRange(r.Addr())
	}
	if !any {
		return nil
	}
	// Without a tailnet interface here, a tailnet address cannot be
	// reached from our sockets (Tailscale in userspace-networking mode, or
	// no Tailscale at all and only a carrier-grade NAT address on offer):
	// do not even ask Tailscale.
	if os.Getenv("REMOHOP_TAILSCALE_STATUS") == "" && len(LocalAddrs()) == 0 {
		return nil
	}
	st := Detect(ctx)
	if !st.Running {
		return nil
	}
	return Predicate(st)
}

// Predicate is Prefer's decision for a given status.
func Predicate(st *Status) func(netip.AddrPort) bool {
	if st == nil || !st.Running {
		return nil
	}
	if !st.HasPeers() {
		return func(ap netip.AddrPort) bool { return InRange(ap.Addr()) && !st.IsSelf(ap.Addr()) }
	}
	set := map[netip.Addr]bool{}
	for _, p := range st.Peers {
		for _, ip := range p.IPs {
			set[ip] = true
		}
	}
	return func(ap netip.AddrPort) bool { return set[ap.Addr().Unmap()] }
}
