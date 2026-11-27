package signal

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// The relay policy is what makes a relay safe to offer to the public. The
// rendezvous service stays open to everybody; relaying, the one thing
// that costs the operator bandwidth, is controlled:
//
//   - a per-source cap on concurrent relayed sessions (both peers' sources,
//     the WebSocket client's address for bridged peers);
//   - two tiers: sessions where either peer is a verified member of an
//     account on this server get AccountRate, others Rate. Membership comes
//     from the server's own account ledger; a host's key is proven at
//     registration, a client's by a signature in the relay request bound
//     to its TLS connection and the introduction's nonce;
//   - optionally relaying for account sessions only;
//   - a deny list of addresses, networks and identity keys;
//   - a monthly byte budget, persisted in the data directory.

// Relay refusal reasons, for the hourly summary.
const (
	refusedPerSource = "per_source"
	refusedMode      = "mode"
	refusedDeny      = "deny"
	refusedBudget    = "budget"
	refusedFull      = "full"
)

const (
	relayProofLabel    = "remohop-signal-relay/1"
	relayExporterLabel = "EXPORTER-remohop-signal-relay"
	usageFile          = "relay-usage.json"
	usageFlushEvery    = time.Minute
	denyPollEvery      = 10 * time.Second
)

// relayProofMessage is what a client signs to show the relay request comes
// from the holder of its identity key: fresh per introduction (the nonce)
// and bound to this rendezvous connection (the TLS exporter).
func relayProofMessage(nonce, exporter []byte) []byte {
	msg := make([]byte, 0, len(relayProofLabel)+len(nonce)+len(exporter))
	msg = append(msg, relayProofLabel...)
	msg = append(msg, nonce...)
	return append(msg, exporter...)
}

// RelayProof signs a relay request for nonce on a connection with the given
// exporter; see Client.RelayAs.
func relayProof(id *auth.Identity, nonce, exporter []byte) []byte {
	return id.Sign(relayProofMessage(nonce, exporter))
}

// ---- deny list --------------------------------------------------------

// denyList is the parsed --relay-deny file.
type denyList struct {
	prefixes []netip.Prefix
	keys     map[[32]byte]bool
}

// parseDenyList reads one entry per line: an IP address, a CIDR prefix or
// an identity key as printed by remohop; "#" starts a comment.
func parseDenyList(b []byte) (*denyList, error) {
	d := &denyList{keys: map[[32]byte]bool{}}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		line, _, _ := strings.Cut(sc.Text(), "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if p, err := netip.ParsePrefix(line); err == nil {
			d.prefixes = append(d.prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(line); err == nil {
			a = a.Unmap().WithZone("")
			d.prefixes = append(d.prefixes, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		if k, err := auth.DecodeKey(line); err == nil {
			d.keys[[32]byte(k)] = true
			continue
		}
		return nil, fmt.Errorf("line %d: %q is not an address, a network or an identity key", n, line)
	}
	return d, sc.Err()
}

func (d *denyList) addr(a netip.Addr) bool {
	if d == nil || !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, p := range d.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (d *denyList) key(k []byte) bool {
	return d != nil && len(k) == ed25519.PublicKeySize && d.keys[[32]byte(k)]
}

// denyFile keeps a deny list current with its file.
type denyFile struct {
	path string
	log  *slog.Logger
	cur  atomic.Pointer[denyList]

	mu    sync.Mutex
	mtime time.Time
	size  int64
}

// reload re-reads the file if it changed (or if force). A file that cannot
// be read or parsed leaves the previous list in force.
func (f *denyFile) reload(force bool) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fi, err := os.Stat(f.path)
	if err != nil {
		return fmt.Errorf("--relay-deny: %w", err)
	}
	if !force && fi.ModTime().Equal(f.mtime) && fi.Size() == f.size {
		return nil
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		return fmt.Errorf("--relay-deny: %w", err)
	}
	d, err := parseDenyList(b)
	if err != nil {
		return fmt.Errorf("--relay-deny %s: %w", f.path, err)
	}
	f.mtime, f.size = fi.ModTime(), fi.Size()
	f.cur.Store(d)
	if f.log != nil {
		f.log.Info("relay deny list loaded", "entries", len(d.prefixes)+len(d.keys))
	}
	return nil
}

func (f *denyFile) list() *denyList {
	if f == nil {
		return nil
	}
	return f.cur.Load()
}

// ---- monthly usage ----------------------------------------------------

// relayUsage counts relayed bytes per calendar month (UTC) and persists
// the count in the data directory.
type relayUsage struct {
	path   string
	budget int64 // bytes per month; 0: unlimited
	now    func() time.Time
	log    *slog.Logger

	bytes atomic.Int64

	mu     sync.Mutex
	month  string
	saved  int64
	logged int // highest threshold (percent) logged this month
}

type usageRecord struct {
	Month string `json:"month"`
	Bytes int64  `json:"bytes"`
}

func monthOf(t time.Time) string { return t.UTC().Format("2006-01") }

// readUsage reads the usage file in dir; a missing file is no usage.
func readUsage(dir string) (usageRecord, error) {
	var r usageRecord
	b, err := os.ReadFile(filepath.Join(dir, usageFile))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", usageFile, err)
	}
	return r, nil
}

func openUsage(dir string, budget int64, now func() time.Time, log *slog.Logger) (*relayUsage, error) {
	u := &relayUsage{path: filepath.Join(dir, usageFile), budget: budget, now: now, log: log}
	r, err := readUsage(dir)
	if err != nil {
		return nil, err
	}
	u.month = monthOf(now())
	if r.Month == u.month && r.Bytes > 0 {
		u.bytes.Store(r.Bytes)
		u.saved = r.Bytes
	}
	u.logged = u.level(u.bytes.Load())
	return u, nil
}

// add counts n relayed bytes.
func (u *relayUsage) add(n int) { u.bytes.Add(int64(n)) }

// level is the highest of 50, 80, 100 and 110 percent of the budget that
// used has reached, or 0.
func (u *relayUsage) level(used int64) int {
	if u.budget <= 0 {
		return 0
	}
	pct := used * 100 / u.budget
	for _, l := range []int{110, 100, 80, 50} {
		if pct >= int64(l) {
			return l
		}
	}
	return 0
}

// current returns this month's usage, starting a new month if the clock
// says so.
func (u *relayUsage) current() int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	return u.bytes.Load()
}

func (u *relayUsage) rollLocked() {
	if m := monthOf(u.now()); m != u.month {
		if u.log != nil {
			u.log.Info("relay budget: a new month begins", "month", m, "previous_month", u.month, "previous_bytes", u.bytes.Load())
		}
		u.month, u.logged = m, 0
		u.bytes.Store(0)
		u.saved = -1 // write the new month out
	}
}

// flush writes the count out if it changed and logs thresholds crossed.
func (u *relayUsage) flush() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rollLocked()
	used := u.bytes.Load()
	if l := u.level(used); l > u.logged {
		u.logged = l
		if u.log != nil {
			msg := map[int]string{
				50:  "relay budget: half of this month's budget is used",
				80:  "relay budget: 80% used; anonymous sessions now get half their rate",
				100: "relay budget: used up; anonymous sessions are refused until the month ends",
				110: "relay budget: 110% used; all new relayed sessions are refused until the month ends",
			}[l]
			u.log.Warn(msg, "month", u.month, "gb", gb(used), "budget_gb", gb(u.budget))
		}
	}
	if used == u.saved {
		return nil
	}
	b, _ := json.Marshal(usageRecord{Month: u.month, Bytes: used})
	if err := config.WriteFileAtomic(u.path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	u.saved = used
	return nil
}

func gb(b int64) string { return fmt.Sprintf("%.2f", float64(b)/1e9) }

// ---- admission --------------------------------------------------------

// relayCounters feed the hourly summary.
type relayCounters struct {
	sessions atomic.Int64
	refused  sync.Map // reason → *atomic.Int64
	lastUsed atomic.Int64
}

func (c *relayCounters) refuse(reason string) {
	v, _ := c.refused.LoadOrStore(reason, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

// summary returns the counts since the last summary and resets them.
func (c *relayCounters) summary() (sessions int64, refused map[string]int64) {
	refused = map[string]int64{}
	c.refused.Range(func(k, v any) bool {
		if n := v.(*atomic.Int64).Swap(0); n > 0 {
			refused[k.(string)] = n
		}
		return true
	})
	return c.sessions.Swap(0), refused
}

// relayPeer is one side of a session to be relayed.
type relayPeer struct {
	source netip.Addr // IPv4 address or IPv6 /64, for the per-source cap
	addr   netip.Addr // the real address, for the deny list
	key    []byte     // proven identity key, or nil
}

// isMember reports whether key is a verified member of an account on this
// server.
func (s *Server) isMember(key []byte) bool {
	if s.accounts == nil || len(key) != ed25519.PublicKeySize {
		return false
	}
	st := s.accounts.store
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, r := range st.accountsOf(key) {
		if r.isMember(key) {
			return true
		}
	}
	return false
}

// relayDecision is what admitRelay grants.
type relayDecision struct {
	rate    int
	account bool
	release func()
}

// admitRelay decides whether a session between client and host may be
// relayed and at what rate. On refusal it returns the reason and a
// message for the client.
func (s *Server) admitRelay(client, host relayPeer) (relayDecision, string, string) {
	deny := s.relayDeny.list()
	if deny.addr(client.addr) || deny.addr(host.addr) || deny.key(client.key) || deny.key(host.key) {
		return relayDecision{}, refusedDeny, "the relay of this server is not available for this connection"
	}
	member := s.isMember(client.key) || s.isMember(host.key) || extMember(s, client.key) || extMember(s, host.key)
	return s.decideRelay(client, host, member)
}

// decideRelay is admitRelay once the deny list has passed and the account
// tier is known.
func (s *Server) decideRelay(client, host relayPeer, account bool) (relayDecision, string, string) {
	rc := s.cfg.Relay
	if rc.ForAccounts && !account {
		return relayDecision{}, refusedMode, "this server relays only for devices of an account on it: log in with `remohop login`, " +
			"or run your own rendezvous server with `remohop signal --relay`"
	}
	rate := rc.Rate
	if account {
		rate = rc.AccountRate
	}
	if u := s.relayUsage; u != nil && u.budget > 0 {
		used := u.current()
		switch {
		case used >= u.budget*11/10, used >= u.budget && !account:
			return relayDecision{}, refusedBudget, "this server's relay budget for the month is used up; it resets on the 1st (UTC). " +
				"Run your own rendezvous server with `remohop signal --relay`"
		case used >= u.budget*8/10 && !account:
			rate = max(rate/2, 1500)
		}
	}
	// PerSource is charged to the client that asks, and only to it: were it
	// charged to the host's address too, anyone who knows a host's ID could
	// take the host's whole share with two requests. The host's address has
	// its own, wider cap (hostRelayCap), against one client tying up many
	// of a host's sessions from many addresses.
	hostCap := hostRelayCap(rc.PerSource)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.relaySources == nil {
		s.relaySources = map[netip.Addr]int{}
	}
	if s.relayHosts == nil {
		s.relayHosts = map[netip.Addr]int{}
	}
	if s.relaySources[client.source] >= rc.PerSource {
		return relayDecision{}, refusedPerSource, fmt.Sprintf("this network already has %d relayed sessions on this server "+
			"(the most it allows per address); close one and try again", rc.PerSource)
	}
	if s.relayHosts[host.source] >= hostCap {
		return relayDecision{}, refusedPerSource, fmt.Sprintf("the host's network already has %d relayed sessions on this server; try again later", hostCap)
	}
	s.relaySources[client.source]++
	s.relayHosts[host.source]++
	var once sync.Once
	return relayDecision{rate: rate, account: account, release: func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.relaySources[client.source]--; s.relaySources[client.source] <= 0 {
				delete(s.relaySources, client.source)
			}
			if s.relayHosts[host.source]--; s.relayHosts[host.source] <= 0 {
				delete(s.relayHosts, host.source)
			}
		})
	}}, "", ""
}

// hostRelayCap is how many relayed sessions one host address may be part of:
// eight times what one client address may ask for.
func hostRelayCap(perSource int) int {
	if perSource > 1<<20 {
		return perSource
	}
	return max(perSource*8, 16)
}

// logRelaySummary writes the hourly summary: counts only, no addresses.
func (s *Server) logRelaySummary() {
	sessions, refused := s.relayCounters.summary()
	attrs := []any{"sessions", sessions}
	if u := s.relayUsage; u != nil {
		used := u.current()
		delta := used - s.relayCounters.lastUsed.Swap(used)
		if delta < 0 { // a new month began
			delta = used
		}
		attrs = append(attrs, "bytes", delta, "month_gb", gb(used))
		if u.budget > 0 {
			attrs = append(attrs, "budget_gb", gb(u.budget))
		}
	}
	s.mu.Lock()
	attrs = append(attrs, "active", s.relays)
	s.mu.Unlock()
	for _, r := range []string{refusedPerSource, refusedMode, refusedDeny, refusedBudget, refusedFull} {
		attrs = append(attrs, "refused_"+r, refused[r])
	}
	s.log.Info("relay summary (last hour)", attrs...)
}
