// Package lan finds RemoHop hosts on the local network without a
// rendezvous server and without typing an address. A running host answers
// multicast DNS (mDNS, RFC 6762) questions for the DNS-SD (RFC 6763)
// service type _remohop._udp; `remohop lan` and the desktop app ask that
// question and list who answers.
//
// What a host says about itself is a hint, never a credential: its device
// name, the UDP port of its QUIC listener, its 9-digit ID when it has one,
// and the fingerprint of its identity key. Connecting to the address runs
// the full session handshake, which checks the host's key like any other
// connection does. Nothing secret is announced.
//
// Only what discovery needs is implemented, with the standard library and
// golang.org/x/net (dnsmessage for the DNS messages, ipv4 for the
// multicast socket options); RemoHop links no mDNS library:
//
//   - The host only answers (see Serve). It does not probe, announce
//     itself unasked or say goodbye. It answers a question only when it
//     came from an address on one of the host's own subnets and, where the
//     system tells, was sent to the mDNS group; it answers by unicast to
//     the asker, with the addresses it has on the asker's subnet, a few
//     times a second at most.
//   - The browser (see Browse) asks from an ephemeral port, which makes
//     its questions "legacy unicast" ones (RFC 6762, section 6.7): every
//     responder answers them by unicast to that port, so the browser needs
//     neither port 5353 nor group membership.
//   - IPv4 only.
//
// Everything read from the network is bounded in size and number, and
// checked for form; names are cleaned of control characters.
package lan

import (
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// Setting is the config key that turns the host's answers off.
// REMOHOP_LAN_DISCOVERY=0 overrides it to off.
const Setting = "lan-discovery"

func init() {
	config.RegisterSetting(config.Setting{Key: Setting, Default: "on", Validate: config.ValidateOnOff,
		Help: "answer LAN discovery (`remohop lan`, the desktop app's \"On this network\") with this host's name, port, ID and key fingerprint"})
}

// Allowed reports whether a host whose config file holds values may answer
// discovery: unless REMOHOP_LAN_DISCOVERY says off, or the setting does.
func Allowed(values map[string]string) bool {
	switch strings.ToLower(os.Getenv("REMOHOP_LAN_DISCOVERY")) {
	case "0", "off", "no", "false":
		return false
	}
	return config.Bool(values[Setting], true)
}

const (
	// Service is the DNS-SD service type, in the .local domain.
	Service = "_remohop._udp.local."
	// metaService lists the service types of a network (RFC 6763,
	// section 9). Answering it lets generic browsers find RemoHop.
	metaService = "_services._dns-sd._udp.local."
	// Version is the version of the TXT record's format (its "v" key).
	// A later format that old browsers must not read gets a new number;
	// new keys alone do not need one.
	Version = 1

	mdnsPort = 5353
	// maxName is the most bytes of a device name kept, as for the name
	// a client gives itself (auth.MaxTokenName).
	maxName = auth.MaxTokenName
	// maxTXT is how many TXT strings are read.
	maxTXT = 16
)

// group is the IPv4 mDNS multicast group, 224.0.0.251:5353.
var group = netip.AddrPortFrom(netip.AddrFrom4([4]byte{224, 0, 0, 251}), mdnsPort)

// Info is what a host tells the local network about itself.
type Info struct {
	// Name is the device name.
	Name string
	// ID is the 9-digit ID, or "" when the host has none (not registered
	// yet, or no rendezvous server).
	ID string
	// Fingerprint is auth.Fingerprint of the host's identity key.
	Fingerprint string
	// Port is the UDP port of the host's QUIC listener.
	Port uint16
	// Addr is the address the host's socket is bound to. The zero value or
	// an unspecified address means every address of the machine.
	Addr netip.Addr
}

// TXT returns the host's TXT record strings: the format version, the name,
// the ID if there is one, and the key fingerprint.
func (i Info) TXT() []string {
	t := []string{"v=" + strconv.Itoa(Version)}
	if n := cleanName(i.Name); n != "" {
		t = append(t, "name="+n)
	}
	if validID(i.ID) {
		t = append(t, "id="+i.ID)
	}
	return append(t, "fp="+i.Fingerprint)
}

// txtInfo is what a TXT record says.
type txtInfo struct {
	name, id, fp string
}

// parseTXT reads a host's TXT strings. Keys are case-insensitive and only
// the first of a repeated key counts (RFC 6763, section 6.4); unknown keys
// are skipped. A record of another format version, or without a well-formed
// fingerprint, is refused; a malformed ID is dropped.
func parseTXT(txt []string) (txtInfo, error) {
	var out txtInfo
	seen := map[string]bool{}
	version := ""
	for i, s := range txt {
		if i == maxTXT {
			break
		}
		k, v, _ := strings.Cut(s, "=")
		k = strings.ToLower(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		switch k {
		case "v":
			version = v
		case "name":
			out.name = cleanName(v)
		case "id":
			if validID(v) {
				out.id = v
			}
		case "fp":
			out.fp = v
		}
	}
	if version != strconv.Itoa(Version) {
		return txtInfo{}, errors.New("unknown TXT format version")
	}
	if !validFingerprint(out.fp) {
		return txtInfo{}, errors.New("no valid key fingerprint")
	}
	return out, nil
}

// cleanName makes a device name safe to keep: one line of plain text, at
// most maxName bytes.
func cleanName(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	return auth.CleanText(s, maxName)
}

// validID accepts a 9-digit RemoHop ID (no leading zero).
func validID(s string) bool {
	if len(s) != 9 || s[0] == '0' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// validFingerprint accepts what auth.Fingerprint makes: five groups of four
// lower-case hex digits joined by colons.
func validFingerprint(s string) bool {
	if len(s) != 24 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i%5 == 4 {
			if c != ':' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// instanceName is the host's DNS-SD service instance name: its device name
// and the start of its fingerprint, so that two hosts with one name stay
// apart, under Service. A DNS label holds at most 63 bytes and the
// dnsmessage package does not escape dots, so dots become dashes and a long
// name is cut.
func instanceName(i Info) string {
	suffix := " (" + strings.ReplaceAll(i.Fingerprint, ":", "")[:8] + ")"
	name := strings.ReplaceAll(cleanName(i.Name), ".", "-")
	if name == "" {
		name = "RemoHop"
	}
	if len(name)+len(suffix) > 63 {
		cut := 63 - len(suffix)
		for cut > 0 && !utf8.RuneStart(name[cut]) {
			cut--
		}
		name = strings.TrimSpace(name[:cut])
	}
	return name + suffix + "." + Service
}

// hostName is the name the host's addresses are given under: its own, not
// the machine's NAME.local, which the system's mDNS responder may own.
func hostName(i Info) string {
	return "remohop-" + strings.ReplaceAll(i.Fingerprint, ":", "") + ".local."
}
