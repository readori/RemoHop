package mesh

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/readori/RemoHop/internal/auth"
)

// A policy says which devices may reach which ports of which others. It is
// enforced by the receiving device on every packet that arrives over the
// mesh: what a device lets in is decided where it is, not where the
// traffic comes from.
//
//	{
//	  "version": 3,
//	  "rules": [
//	    {"from": ["laptop", "phone"], "to": ["nas"], "ports": ["22/tcp", "445/tcp", "icmp"]},
//	    {"from": ["*"], "to": ["printer"], "ports": ["631/tcp"]},
//	    {"from": ["laptop"], "to": ["192.168.1.0/24"]}
//	  ]
//	}
//
// A selector is "*", a device's mesh name (as `remohop net peers` shows it,
// without ".remohop"), or a device's identity fingerprint. In "to", a CIDR
// selects destinations behind a subnet router or exit node; those match
// nothing else (and "*" matches them too). Ports: "*" or absent for all,
// "22", "22/tcp", "8000-8100/udp", "icmp" (ping). Replies to connections a
// device opened itself always pass, as do ICMP errors.
//
// Without a policy every device of the mesh may reach every port of every
// other, as on a LAN. A policy with an empty rule list lets nothing in.
//
// The policy is shared by the account's devices: it is signed by the
// device that set it (`remohop net policy set`), which must be a member of the
// account, and devices pass it on to each other when they connect. A
// device adopts a policy with a higher version than its own. A device
// outside every account keeps a policy signed by itself.
type Policy struct {
	Version int64 `json:"version"`
	// Prefix4 overrides DefaultPrefix4 for the whole account.
	Prefix4 string `json:"prefix4,omitempty"`
	Rules   []Rule `json:"rules"`
}

// Rule lets the devices in From reach Ports on the destinations in To.
type Rule struct {
	From  []string `json:"from"`
	To    []string `json:"to"`
	Ports []string `json:"ports,omitempty"`
}

const (
	maxRules     = 256
	maxSelectors = 64
	// maxPolicy bounds a signed policy, which travels in one stream frame.
	maxPolicy = 48 << 10
)

// ParsePolicy reads and checks a policy document.
func ParsePolicy(b []byte) (*Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if _, err := compile(&p); err != nil {
		return nil, err
	}
	if _, err := ParsePrefix4(p.Prefix4); err != nil {
		return nil, fmt.Errorf("policy: prefix4: %w", err)
	}
	return &p, nil
}

type selector struct {
	all    bool
	name   string
	fp     string
	prefix netip.Prefix
}

type portRange struct {
	proto  byte // 0: TCP and UDP
	lo, hi uint16
	icmp   bool
}

type rule struct {
	from, to []selector
	ports    []portRange // nil: everything
}

type compiled struct {
	rules []rule
	open  bool // no policy at all
}

func parseSelector(s string, cidr bool) (selector, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "*":
		return selector{all: true}, nil
	case cidr && strings.Contains(s, "/"):
		p, err := netip.ParsePrefix(s)
		if err != nil || p != p.Masked() {
			return selector{}, fmt.Errorf("%q is not a network like 192.168.1.0/24", s)
		}
		return selector{prefix: p}, nil
	case strings.Count(s, ":") == 4 && len(s) == 24:
		return selector{fp: strings.ToLower(s)}, nil
	}
	if s == "" || Label(s) != strings.ToLower(strings.TrimSuffix(s, "."+Domain)) {
		return selector{}, fmt.Errorf("%q is not a device name, fingerprint or \"*\"", s)
	}
	return selector{name: Label(s)}, nil
}

func parsePorts(s string) (portRange, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "icmp" || s == "ping" {
		return portRange{icmp: true}, nil
	}
	if s == "*" {
		return portRange{lo: 0, hi: 65535}, nil
	}
	var pr portRange
	num, proto, hasProto := strings.Cut(s, "/")
	if hasProto {
		switch proto {
		case "tcp":
			pr.proto = protoTCP
		case "udp":
			pr.proto = protoUDP
		default:
			return pr, fmt.Errorf("port %q: protocol must be tcp or udp", s)
		}
	}
	lo, hi, isRange := strings.Cut(num, "-")
	a, err1 := strconv.ParseUint(lo, 10, 16)
	b := a
	var err2 error
	if isRange {
		b, err2 = strconv.ParseUint(hi, 10, 16)
	}
	if err1 != nil || err2 != nil || a == 0 || b < a {
		return pr, fmt.Errorf("%q is not a port, a range like 8000-8100, \"icmp\" or \"*\" (with /tcp or /udp if wanted)", s)
	}
	pr.lo, pr.hi = uint16(a), uint16(b)
	return pr, nil
}

func compile(p *Policy) (*compiled, error) {
	if p == nil {
		return &compiled{open: true}, nil
	}
	if len(p.Rules) > maxRules {
		return nil, fmt.Errorf("policy: at most %d rules", maxRules)
	}
	c := &compiled{}
	for i, r := range p.Rules {
		if len(r.From) == 0 || len(r.To) == 0 {
			return nil, fmt.Errorf("policy: rule %d needs \"from\" and \"to\"", i+1)
		}
		if len(r.From) > maxSelectors || len(r.To) > maxSelectors || len(r.Ports) > maxSelectors {
			return nil, fmt.Errorf("policy: rule %d is too long", i+1)
		}
		var cr rule
		for _, s := range r.From {
			sel, err := parseSelector(s, false)
			if err != nil {
				return nil, fmt.Errorf("policy: rule %d: from: %w", i+1, err)
			}
			cr.from = append(cr.from, sel)
		}
		for _, s := range r.To {
			sel, err := parseSelector(s, true)
			if err != nil {
				return nil, fmt.Errorf("policy: rule %d: to: %w", i+1, err)
			}
			cr.to = append(cr.to, sel)
		}
		for _, s := range r.Ports {
			pr, err := parsePorts(s)
			if err != nil {
				return nil, fmt.Errorf("policy: rule %d: %w", i+1, err)
			}
			if pr.lo == 0 && pr.hi == 65535 && !pr.icmp {
				cr.ports = nil
				break
			}
			cr.ports = append(cr.ports, pr)
		}
		c.rules = append(c.rules, cr)
	}
	return c, nil
}

// device is who a selector is matched against.
type device struct {
	label string
	fp    string
}

func (s selector) matchDevice(d device) bool {
	return s.all || (s.name != "" && s.name == d.label) || (s.fp != "" && s.fp == d.fp)
}

func (s selector) matchDest(self device, dst netip.Addr, routed bool) bool {
	if s.all {
		return true
	}
	if routed {
		return s.prefix.IsValid() && s.prefix.Contains(dst)
	}
	return s.matchDevice(self)
}

func (r portRange) match(k packet) bool {
	if r.icmp {
		return k.isEchoRequest()
	}
	if !k.ports || (r.proto != 0 && r.proto != k.proto) || (k.proto != protoTCP && k.proto != protoUDP) {
		return false
	}
	return k.dport >= r.lo && k.dport <= r.hi
}

// allows decides about a packet from device from to this device (self),
// whose destination is dst: one of self's addresses, or (routed) a
// destination self forwards to as a subnet router or exit node. A later
// fragment, which carries no ports, passes if from may reach dst at all.
func (c *compiled) allows(from, self device, dst netip.Addr, routed bool, k packet) bool {
	if c.open {
		return true
	}
	for _, r := range c.rules {
		if !anyMatch(r.from, func(s selector) bool { return s.matchDevice(from) }) ||
			!anyMatch(r.to, func(s selector) bool { return s.matchDest(self, dst, routed) }) {
			continue
		}
		if r.ports == nil || !k.first {
			return true
		}
		for _, pr := range r.ports {
			if pr.match(k) {
				return true
			}
		}
	}
	return false
}

func anyMatch(sels []selector, f func(selector) bool) bool {
	for _, s := range sels {
		if f(s) {
			return true
		}
	}
	return false
}

// SignedPolicy is a policy as it is stored and passed between devices.
type SignedPolicy struct {
	// Policy is the document exactly as signed (base64 in JSON, so that
	// no reformatting can change the signed bytes).
	Policy []byte `json:"policy"`
	// Account is the account ID it belongs to; empty for a device outside
	// every account.
	Account []byte `json:"account,omitempty"`
	Signer  []byte `json:"signer"`
	Sig     []byte `json:"sig"`
}

const policyLabel = "remohop-net-policy/1"

func policySignedBytes(account, doc []byte) []byte {
	var b bytes.Buffer
	b.WriteString(policyLabel)
	b.WriteByte(0)
	b.WriteByte(byte(len(account)))
	b.Write(account)
	b.Write(doc)
	return b.Bytes()
}

// SignPolicy signs a policy for account with id.
func SignPolicy(id *auth.Identity, account []byte, p *Policy) (*SignedPolicy, error) {
	doc, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if len(doc) > maxPolicy {
		return nil, fmt.Errorf("policy too large (%d bytes, at most %d)", len(doc), maxPolicy)
	}
	return &SignedPolicy{Policy: doc, Account: account, Signer: id.Public(), Sig: id.Sign(policySignedBytes(account, doc))}, nil
}

// Verify checks the signature and that the signer may set the policy
// (member reports whether a key is a member of the account; for a device
// outside every account, whether it is that device itself), and returns the
// policy.
func (sp *SignedPolicy) Verify(account []byte, member func(ed25519.PublicKey) bool) (*Policy, error) {
	if sp == nil {
		return nil, errors.New("no policy")
	}
	if len(sp.Policy) > maxPolicy {
		return nil, errors.New("policy too large")
	}
	if !bytes.Equal(sp.Account, account) {
		return nil, errors.New("the policy belongs to another account")
	}
	if len(sp.Signer) != ed25519.PublicKeySize || !ed25519.Verify(sp.Signer, policySignedBytes(sp.Account, sp.Policy), sp.Sig) {
		return nil, errors.New("the policy's signature is invalid")
	}
	if !member(ed25519.PublicKey(sp.Signer)) {
		return nil, errors.New("the policy is signed by a device that is not a member of the account")
	}
	return ParsePolicy(sp.Policy)
}

// newer reports whether a (version va) should replace b (version vb):
// higher version, or for equal versions (two devices set one at the same
// time) the larger hash, so that every device settles on the same one.
func newer(a *SignedPolicy, va int64, b *SignedPolicy, vb int64) bool {
	if b == nil {
		return true
	}
	if va != vb {
		return va > vb
	}
	ha, hb := sha256.Sum256(a.Policy), sha256.Sum256(b.Policy)
	return bytes.Compare(ha[:], hb[:]) > 0
}
