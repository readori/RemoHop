package mesh

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// PolicyFile holds the adopted signed policy in the config directory.
const PolicyFile = "net-policy.json"

// peerInfo is one device of the mesh as this device sees it.
type peerInfo struct {
	Assignment
	FP     string
	ID     string // its 9-digit ID, if it ever registered as a host
	Online bool
	Self   bool
}

func (p *peerInfo) device() device { return device{label: p.Label, fp: p.FP} }

// view is the mesh as this device sees it at one moment: who is in it, at
// which addresses, under which policy. It is rebuilt whenever the account
// or the policy changes, and never modified once built.
type view struct {
	account []byte
	prefix4 netip.Prefix
	prefix6 netip.Prefix
	self    *peerInfo
	peers   []*peerInfo // without self, in account order
	byKey   map[string]*peerInfo
	byAddr  map[netip.Addr]*peerInfo
	members map[string]bool // current members' keys

	signed  *SignedPolicy
	policy  *Policy
	rules   *compiled
	version int64
}

// errNoAccount explains why there is no mesh.
var errNoAccount = errors.New("this device is not a member of an account: the mesh is made of the devices of one account " +
	"(`remohop login EMAIL` on each, see `remohop help login`)")

// accountMembers replays the account's log: every device ever added, in
// order (for address allocation), and the current members.
func accountMembers(st *account.State) (all []Member, current *ledger.State, err error) {
	ls, err := st.Ledger()
	if err != nil {
		return nil, nil, err
	}
	for _, e := range st.Log {
		if e.Op == ledger.OpCreate || e.Op == ledger.OpAdd {
			all = append(all, Member{Key: ed25519.PublicKey(e.Device), Name: e.Name})
		}
	}
	return all, ls, nil
}

// buildView makes the view of a member of an account. devices (may be nil)
// adds IDs and presence; sp is the policy to apply if it is valid for the
// account.
func buildView(self ed25519.PublicKey, st *account.State, devices []account.Device, sp *SignedPolicy) (*view, error) {
	if st == nil || st.Status != account.StatusMember {
		return nil, errNoAccount
	}
	if time.Since(st.Verified) > account.MaxStale {
		return nil, fmt.Errorf("the account's device list was last verified %s ago; the mesh trusts it for at most %s "+
			"(the rendezvous server could not be reached; `remohop account sync` retries)", time.Since(st.Verified).Round(time.Hour), account.MaxStale)
	}
	all, ls, err := accountMembers(st)
	if err != nil {
		return nil, err
	}
	if !ls.IsMember(self) {
		return nil, errNoAccount
	}
	v := &view{account: st.AccountID, byKey: map[string]*peerInfo{}, byAddr: map[netip.Addr]*peerInfo{}, members: map[string]bool{}}
	for _, m := range ls.Members {
		v.members[string(m.Key)] = true
	}
	v.setPolicy(sp)
	v.prefix4 = DefaultPrefix4
	if v.policy != nil {
		if v.prefix4, err = ParsePrefix4(v.policy.Prefix4); err != nil {
			return nil, err
		}
	}
	v.prefix6 = Prefix6(st.AccountID)
	byKey := map[string]account.Device{}
	for _, d := range devices {
		byKey[d.Key] = d
	}
	for _, a := range Assign(v.prefix4, v.prefix6, all) {
		if !v.members[string(a.Key)] {
			continue // removed: keeps its address, but is no peer
		}
		p := &peerInfo{Assignment: a, FP: auth.Fingerprint(a.Key), Self: bytes.Equal(a.Key, self)}
		if d, ok := byKey[auth.EncodeKey(a.Key)]; ok {
			p.ID, p.Online = d.ID, d.Online
		}
		v.byKey[string(a.Key)] = p
		v.byAddr[a.Addr4] = p
		if a.Addr6.IsValid() {
			v.byAddr[a.Addr6] = p
		}
		if p.Self {
			v.self = p
		} else {
			v.peers = append(v.peers, p)
		}
	}
	if v.self == nil {
		return nil, fmt.Errorf("no address left for this device in %s", v.prefix4)
	}
	return v, nil
}

// setPolicy applies sp if it verifies for the view's account; otherwise the
// open default.
func (v *view) setPolicy(sp *SignedPolicy) {
	v.signed, v.policy, v.version = nil, nil, 0
	v.rules, _ = compile(nil)
	if sp == nil {
		return
	}
	p, err := sp.Verify(v.account, func(k ed25519.PublicKey) bool { return v.members[string(k)] })
	if err != nil {
		return
	}
	c, err := compile(p)
	if err != nil {
		return
	}
	v.signed, v.policy, v.rules, v.version = sp, p, c, p.Version
}

// verifyPolicy checks a policy received from a peer against this view.
func (v *view) verifyPolicy(sp *SignedPolicy) (*Policy, error) {
	return sp.Verify(v.account, func(k ed25519.PublicKey) bool { return v.members[string(k)] })
}

// names is the DNS table of the view.
func (v *view) names() map[string][]netip.Addr {
	m := map[string][]netip.Addr{}
	add := func(p *peerInfo) {
		var addrs []netip.Addr
		addrs = append(addrs, p.Addr4)
		if p.Addr6.IsValid() {
			addrs = append(addrs, p.Addr6)
		}
		m[p.FQDN()+"."] = addrs
	}
	add(v.self)
	for _, p := range v.peers {
		add(p)
	}
	return m
}

// byName finds a peer by mesh name (with or without .remohop), fingerprint
// or address.
func (v *view) byName(s string) (*peerInfo, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		p, ok := v.byAddr[a]
		return p, ok
	}
	name := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(s), "."), "."+Domain)
	for _, p := range append([]*peerInfo{v.self}, v.peers...) {
		if p.Label == name || p.FP == strings.ToLower(s) {
			return p, true
		}
	}
	return nil, false
}

// loadPolicy reads the adopted policy, nil if there is none.
func loadPolicy(dir string) (*SignedPolicy, error) {
	b, err := config.ReadFile(filepath.Join(dir, PolicyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sp SignedPolicy
	if err := json.Unmarshal(b, &sp); err != nil {
		return nil, fmt.Errorf("%s: %w", PolicyFile, err)
	}
	return &sp, nil
}

func savePolicy(dir string, sp *SignedPolicy) error {
	b, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(dir, PolicyFile), append(b, '\n'), 0o600)
}

func policyMtime(dir string) time.Time {
	fi, err := os.Stat(filepath.Join(dir, PolicyFile))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}
