package mesh

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The mesh's own DNS responder answers for names under .remohop, and only
// for those: A and AAAA records of the mesh's devices. Everything else is
// refused, so it can never stand in for the system's resolver. It listens
// on this device's mesh address; the system is pointed at it only for the
// .remohop domain, where that can be done per interface (see net-dns).

type names struct {
	mu sync.RWMutex
	m  map[string][]netip.Addr // "laptop.remohop." → addresses
}

func (n *names) set(m map[string][]netip.Addr) {
	n.mu.Lock()
	n.m = m
	n.mu.Unlock()
}

func (n *names) lookup(fqdn string) ([]netip.Addr, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	a, ok := n.m[strings.ToLower(fqdn)]
	return a, ok
}

const dnsTTL = 60

// answer builds the response to one query, or reports that there is
// nothing to send (not a query at all).
func (n *names) answer(req []byte) ([]byte, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(req)
	if err != nil || h.Response {
		return nil, false
	}
	q, err := p.Question()
	resp := dnsmessage.Header{ID: h.ID, Response: true, OpCode: h.OpCode, RecursionDesired: h.RecursionDesired}
	if err != nil || h.OpCode != 0 {
		resp.RCode = dnsmessage.RCodeFormatError
		if h.OpCode != 0 {
			resp.RCode = dnsmessage.RCodeNotImplemented
		}
		b := dnsmessage.NewBuilder(nil, resp)
		out, err := b.Finish()
		return out, err == nil
	}
	name := strings.ToLower(q.Name.String())
	suffix := "." + Domain + "."
	inZone := strings.HasSuffix(name, suffix) || name == Domain+"."
	addrs, known := n.lookup(name)
	switch {
	case !inZone || q.Class != dnsmessage.ClassINET:
		resp.RCode = dnsmessage.RCodeRefused
	case !known:
		resp.RCode = dnsmessage.RCodeNameError
		resp.Authoritative = true
	default:
		resp.Authoritative = true
	}
	b := dnsmessage.NewBuilder(nil, resp)
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, false
	}
	if err := b.Question(q); err != nil {
		return nil, false
	}
	if resp.RCode == dnsmessage.RCodeSuccess {
		if err := b.StartAnswers(); err != nil {
			return nil, false
		}
		rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: dnsTTL}
		for _, a := range addrs {
			switch {
			case a.Is4() && (q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeALL):
				b.AResource(rh, dnsmessage.AResource{A: a.As4()})
			case a.Is6() && (q.Type == dnsmessage.TypeAAAA || q.Type == dnsmessage.TypeALL):
				b.AAAAResource(rh, dnsmessage.AAAAResource{AAAA: a.As16()})
			}
		}
	}
	out, err := b.Finish()
	return out, err == nil
}

// serveDNS answers queries on pc until it is closed or ctx ends.
func (n *names) serveDNS(ctx context.Context, pc net.PacketConn) {
	stop := context.AfterFunc(ctx, func() { pc.Close() })
	defer stop()
	buf := make([]byte, 1500)
	for {
		pc.SetReadDeadline(time.Now().Add(time.Minute))
		k, from, err := pc.ReadFrom(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && ctx.Err() == nil {
				continue
			}
			return
		}
		if out, ok := n.answer(buf[:k]); ok {
			pc.WriteTo(out, from)
		}
	}
}
