package lan

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// DefaultWait is how long Browse listens for answers when not told.
const DefaultWait = 2 * time.Second

const (
	// maxHosts is how many hosts one browse keeps.
	maxHosts = 64
	// maxRecords is how many resource records of one answer are looked
	// at, and maxAddrs how many addresses of one name.
	maxRecords = 64
	maxAddrs   = 8
)

// resend is when, after the first, the question is asked again: a
// datagram can be lost, and a host can be busy.
var resend = []time.Duration{250 * time.Millisecond, time.Second}

// Host is a RemoHop host that answered on the local network. Everything in
// it is what that host said about itself, checked for form only: connecting
// to Addr still checks the host's key in the session handshake.
type Host struct {
	// Name is the host's device name, cleaned of control characters
	// (still print it with cli.Sanitize, like any text from another
	// machine).
	Name string `json:"name"`
	// Addr is where the host listens: an address it gave on this
	// machine's subnet, or else the one its answer came from.
	Addr netip.AddrPort `json:"addr"`
	// ID is its 9-digit ID, if it has one.
	ID string `json:"id,omitempty"`
	// Fingerprint is the fingerprint of its identity key, as
	// auth.Fingerprint prints it.
	Fingerprint string `json:"fingerprint"`
}

// Browse asks the local network for RemoHop hosts and returns those that
// answered within wait (DefaultWait if 0), sorted by name. It returns early,
// with what it found, when ctx ends.
func Browse(ctx context.Context, wait time.Duration) ([]Host, error) {
	c, err := listenBrowser()
	if err != nil {
		return nil, err
	}
	defer c.close()
	return browse(ctx, c, localPrefixes(), wait)
}

// browse is Browse on c, with the machine's subnets given.
func browse(ctx context.Context, c packetConn, locals []netip.Prefix, wait time.Duration) ([]Host, error) {
	if wait <= 0 {
		wait = DefaultWait
	}
	id, q, err := question()
	if err != nil {
		return nil, err
	}
	if err := c.writeTo(q, group); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.setReadDeadline(time.Unix(1, 0)) })
	defer stop()
	start := time.Now()
	end := start.Add(wait)
	next := 0 // in resend
	var out []Host
	buf := make([]byte, 9000) // the largest mDNS message (RFC 6762, section 17)
	for ctx.Err() == nil {
		now := time.Now()
		if !now.Before(end) {
			break
		}
		if next < len(resend) && now.Sub(start) >= resend[next] {
			c.writeTo(q, group) // the first one went out; a lost repeat is no matter
			next++
			continue
		}
		deadline := end
		if next < len(resend) {
			if t := start.Add(resend[next]); t.Before(deadline) {
				deadline = t
			}
		}
		if err := c.setReadDeadline(deadline); err != nil {
			return sortHosts(out), err
		}
		n, src, _, err := c.readFrom(buf)
		if err != nil {
			if isTimeout(err) || ctx.Err() != nil {
				continue
			}
			return sortHosts(out), err
		}
		out = merge(out, parseAnswer(buf[:n], src, id, locals))
	}
	return sortHosts(out), ctx.Err()
}

// question is the browser's query: PTR records of Service, with a random ID
// that answers must repeat.
func question() (uint16, []byte, error) {
	var r [2]byte
	if _, err := rand.Read(r[:]); err != nil {
		return 0, nil, err
	}
	id := binary.BigEndian.Uint16(r[:])
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id})
	if err := b.StartQuestions(); err != nil {
		return 0, nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(Service), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}); err != nil {
		return 0, nil, err
	}
	q, err := b.Finish()
	return id, q, err
}

// parseAnswer reads one answer to the question with id, received from src,
// and returns the hosts it describes. An answer must come from port 5353
// and from one of this machine's subnets (locals); anything malformed, of
// another service or beyond the limits is left out.
func parseAnswer(b []byte, src netip.AddrPort, id uint16, locals []netip.Prefix) []Host {
	from := src.Addr().Unmap()
	if src.Port() != mdnsPort || !onLink(from, locals) {
		return nil
	}
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response || h.ID != id {
		return nil
	}
	if err := p.SkipAllQuestions(); err != nil {
		return nil
	}
	type service struct {
		label string // the instance's own label, a name to fall back on
		srv   dnsmessage.SRVResource
	}
	srvs := map[string]service{}
	txts := map[string][]string{}
	addrs := map[string][]netip.Addr{}
	seen := 0
	// read handles one record of a section; false ends the section.
	read := func(header func() (dnsmessage.ResourceHeader, error), skip func() error) bool {
		rh, err := header()
		if err != nil || seen == maxRecords {
			return false
		}
		seen++
		name := strings.ToLower(rh.Name.String())
		switch {
		case rh.Type == dnsmessage.TypeSRV && strings.HasSuffix(name, "."+Service) && len(srvs) < maxHosts:
			r, err := p.SRVResource()
			if err != nil {
				return false
			}
			if _, dup := srvs[name]; !dup {
				label, cut := rh.Name.String(), len(Service)+1
				if len(label) > cut && strings.EqualFold(label[len(label)-cut:], "."+Service) {
					label = label[:len(label)-cut]
				} else {
					label = ""
				}
				srvs[name] = service{label: label, srv: r}
			}
		case rh.Type == dnsmessage.TypeTXT && strings.HasSuffix(name, "."+Service) && len(txts) < maxHosts:
			r, err := p.TXTResource()
			if err != nil {
				return false
			}
			if _, dup := txts[name]; !dup {
				txts[name] = r.TXT
			}
		case rh.Type == dnsmessage.TypeA && len(addrs[name]) < maxAddrs && len(addrs) < maxHosts:
			r, err := p.AResource()
			if err != nil {
				return false
			}
			addrs[name] = append(addrs[name], netip.AddrFrom4(r.A))
		default:
			if skip() != nil {
				return false
			}
		}
		return true
	}
	for read(p.AnswerHeader, p.SkipAnswer) {
	}
	if p.SkipAllAuthorities() == nil {
		for read(p.AdditionalHeader, p.SkipAdditional) {
		}
	}
	var out []Host
	for inst, s := range srvs {
		txt, ok := txts[inst]
		if !ok || s.srv.Port == 0 {
			continue
		}
		t, err := parseTXT(txt)
		if err != nil {
			continue
		}
		addr := from
		for _, a := range addrs[strings.ToLower(s.srv.Target.String())] {
			if usable(a, from) && onLink(a, locals) {
				addr = a
				break
			}
		}
		name := t.name
		if name == "" {
			name = cleanName(s.label)
		}
		out = append(out, Host{Name: name, Addr: netip.AddrPortFrom(addr, s.srv.Port), ID: t.id, Fingerprint: t.fp})
	}
	return out
}

// usable reports whether a host may be dialled at address a, which it gave
// in an answer from from: a unicast IPv4 address, and a loopback one only
// when the answer came from this machine's loopback.
func usable(a, from netip.Addr) bool {
	if !a.Is4() || a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	return !a.IsLoopback() || from.IsLoopback()
}

// onLink reports whether a is on one of the machine's subnets.
func onLink(a netip.Addr, locals []netip.Prefix) bool {
	for _, p := range locals {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// merge adds found to hosts, one entry per key fingerprint: the first
// answer's address stays, and an ID learnt later is added.
func merge(hosts, found []Host) []Host {
	for _, f := range found {
		i := slices.IndexFunc(hosts, func(h Host) bool { return h.Fingerprint == f.Fingerprint })
		switch {
		case i >= 0:
			if hosts[i].ID == "" {
				hosts[i].ID = f.ID
			}
		case len(hosts) < maxHosts:
			hosts = append(hosts, f)
		}
	}
	return hosts
}

func sortHosts(hs []Host) []Host {
	slices.SortFunc(hs, func(a, b Host) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), a.Addr.Compare(b.Addr))
	})
	return hs
}
