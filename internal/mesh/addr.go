package mesh

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Domain is the DNS suffix of mesh names: laptop.remohop.
const Domain = "remohop"

// DefaultPrefix4 is the IPv4 network mesh addresses come from unless the
// account's policy names another. It is next to the point-to-point VPN's
// 10.77.0.0/16 and well away from Tailscale's 100.64.0.0/10, so both can
// run on the same machine.
var DefaultPrefix4 = netip.MustParsePrefix("10.78.0.0/16")

// maxGen bounds how far address allocation walks a key's candidate
// sequence before giving up on a collision.
const maxGen = 64

// Member is a device in the mesh, as allocation sees it.
type Member struct {
	Key ed25519.PublicKey
	// Name is the name the device had when it joined (from the signed
	// account log), turned into a DNS label by Assign.
	Name string
}

// Assignment is what a member gets: its addresses and its name.
type Assignment struct {
	Key   ed25519.PublicKey
	Label string // DNS label, unique in the mesh
	Addr4 netip.Addr
	Addr6 netip.Addr
	// Gen is the step of the key's candidate sequence the addresses came
	// from: 0 unless an earlier member already held the first choice.
	Gen int
}

// FQDN is the member's full mesh name, without the trailing dot.
func (a Assignment) FQDN() string { return a.Label + "." + Domain }

// candidate4 returns the gen-th IPv4 candidate of key in prefix, or an
// invalid address if that step yields an address nobody should get (the
// network or broadcast address of the prefix, or one ending in .0 or
// .255, which some software mistakes for those).
func candidate4(prefix netip.Prefix, key ed25519.PublicKey, gen int) netip.Addr {
	prefix = prefix.Masked()
	hostBits := 32 - prefix.Bits()
	if !prefix.Addr().Is4() || hostBits < 2 || hostBits > 24 {
		return netip.Addr{}
	}
	h := hashOf("remohop-net-addr4", key, gen)
	n := binary.BigEndian.Uint32(h[:4]) & (1<<hostBits - 1)
	if n == 0 || n == 1<<hostBits-1 || n&0xff == 0 || n&0xff == 0xff {
		return netip.Addr{}
	}
	base := binary.BigEndian.Uint32(prefix.Addr().AsSlice())
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], base|n)
	return netip.AddrFrom4(b)
}

// candidate6 returns the gen-th IPv6 candidate of key in a /64.
func candidate6(prefix netip.Prefix, key ed25519.PublicKey, gen int) netip.Addr {
	if !prefix.Addr().Is6() || prefix.Bits() != 64 {
		return netip.Addr{}
	}
	h := hashOf("remohop-net-addr6", key, gen)
	b := prefix.Masked().Addr().As16()
	copy(b[8:], h[:8])
	if binary.BigEndian.Uint64(b[8:]) == 0 {
		return netip.Addr{}
	}
	return netip.AddrFrom16(b)
}

func hashOf(label string, key []byte, gen int) [32]byte {
	h := sha256.New()
	h.Write([]byte(label))
	h.Write([]byte{0})
	h.Write(key)
	var g [4]byte
	binary.BigEndian.PutUint32(g[:], uint32(gen))
	h.Write(g[:])
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// Prefix6 is the account's IPv6 network: a unique-local /64 (RFC 4193)
// whose 40-bit global ID is derived from the account ID, so that two
// accounts' meshes do not collide.
func Prefix6(accountID []byte) netip.Prefix {
	h := sha256.Sum256(append([]byte("remohop-net-ula\x00"), accountID...))
	var b [16]byte
	b[0] = 0xfd
	copy(b[1:6], h[:5])
	return netip.PrefixFrom(netip.AddrFrom16(b), 64)
}

// Assign gives every member its addresses and name. members must be in the
// order the account's log added them, every device that was ever added
// included (removed ones too): a later member never takes an address an
// earlier one holds, and nobody's address changes when someone joins or
// leaves. Every device computes the same assignment from the same log.
func Assign(prefix4, prefix6 netip.Prefix, members []Member) []Assignment {
	used4 := map[netip.Addr]bool{}
	used6 := map[netip.Addr]bool{}
	labels := map[string]bool{}
	seen := map[string]bool{}
	var out []Assignment
	for _, m := range members {
		if len(m.Key) != ed25519.PublicKeySize || seen[string(m.Key)] {
			continue
		}
		seen[string(m.Key)] = true
		a := Assignment{Key: m.Key}
		for gen := 0; gen < maxGen; gen++ {
			a4, a6 := candidate4(prefix4, m.Key, gen), candidate6(prefix6, m.Key, gen)
			if !a4.IsValid() || used4[a4] || (prefix6.IsValid() && (!a6.IsValid() || used6[a6])) {
				continue
			}
			a.Addr4, a.Addr6, a.Gen = a4, a6, gen
			break
		}
		if !a.Addr4.IsValid() {
			continue // the prefix is full; such a member has no address
		}
		used4[a.Addr4] = true
		if a.Addr6.IsValid() {
			used6[a.Addr6] = true
		}
		base := Label(m.Name)
		a.Label = base
		for i := 2; labels[a.Label]; i++ {
			suffix := "-" + strconv.Itoa(i)
			a.Label = strings.TrimRight(trimLabel(base, 63-len(suffix)), "-") + suffix
		}
		labels[a.Label] = true
		out = append(out, a)
	}
	return out
}

// Label turns a device name into a DNS label: lower case letters, digits
// and dashes, at most 63 characters, never empty.
func Label(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(trimLabel(b.String(), 63), "-")
	if s == "" {
		return "device"
	}
	return s
}

func trimLabel(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ParsePrefix4 validates an IPv4 mesh network: private (RFC 1918), between
// /8 and /24. Carrier-grade NAT space is refused: Tailscale lives there.
func ParsePrefix4(s string) (netip.Prefix, error) {
	if s == "" {
		return DefaultPrefix4, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return netip.Prefix{}, fmt.Errorf("%q is not an IPv4 network like 10.78.0.0/16", s)
	}
	if p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("%s has host bits set; did you mean %s?", p, p.Masked())
	}
	if p.Bits() < 8 || p.Bits() > 24 {
		return netip.Prefix{}, fmt.Errorf("%s: the mesh network must be between /8 and /24", p)
	}
	if !p.Addr().IsPrivate() {
		return netip.Prefix{}, fmt.Errorf("%s is not a private network (10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16)", p)
	}
	return p, nil
}
