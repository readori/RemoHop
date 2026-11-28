package lan

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	// checkEvery is how often Serve asks again whether to answer at all,
	// and looks for new interfaces and addresses.
	checkEvery = 5 * time.Second
	// retryListen is how long Serve waits after the mDNS port could not be
	// opened (another program holds it exclusively, say).
	retryListen = time.Minute
	// answerRate and answerBurst bound how many answers a second the host
	// sends, whoever asks.
	answerRate  = 10
	answerBurst = 20
	// maxQuestions is how many questions of one query are looked at.
	maxQuestions = 16
	// ttl is the lifetime given to the records: RFC 6762 caps it at 10
	// seconds in answers to legacy unicast questions; full mDNS queriers
	// get the usual two minutes.
	ttlLegacy = 10
	ttlMDNS   = 120
	// classFlush is class IN with the cache-flush bit, for the records
	// that are the host's alone (RFC 6762, section 10.2).
	classFlush = dnsmessage.ClassINET | 0x8000
)

// packetConn is the socket the responder and the browser use: a UDP socket
// in production (socket.go), an in-memory network in tests.
type packetConn interface {
	// readFrom reads one datagram. toGroup is false when the system says
	// it was not sent to the mDNS group (a unicast question); it is true
	// when it was, or when the system does not say.
	readFrom(b []byte) (n int, src netip.AddrPort, toGroup bool, err error)
	// writeTo sends a datagram; to the mDNS group it goes out on every
	// interface that does multicast.
	writeTo(b []byte, dst netip.AddrPort) error
	setReadDeadline(t time.Time) error
	close() error
}

// Serve answers discovery questions until ctx ends. info is asked at each
// question what to say and whether to answer at all; while it says not to,
// the mDNS port is not held. Errors are logged (at debug level: a host
// without discovery works as before) and retried; Serve returns when ctx
// ends.
func Serve(ctx context.Context, info func() (Info, bool), log *slog.Logger) {
	serve(ctx, info, log, listenMDNS, localPrefixes)
}

// serve is Serve with the socket and the machine's subnets supplied.
func serve(ctx context.Context, info func() (Info, bool), log *slog.Logger,
	listen func() (packetConn, error), locals func() []netip.Prefix) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	lim := &limiter{rate: answerRate, burst: answerBurst}
	var c packetConn
	defer func() {
		if c != nil {
			c.close()
		}
	}()
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	for ctx.Err() == nil {
		_, on := info()
		if !on {
			if c != nil {
				c.close()
				c = nil
				log.Debug("LAN discovery: stopped answering")
			}
			if !wait(checkEvery) {
				return
			}
			continue
		}
		if c == nil {
			var err error
			if c, err = listen(); err != nil {
				c = nil
				log.Debug("LAN discovery: cannot open the mDNS port", "err", err)
				if !wait(retryListen) {
					return
				}
				continue
			}
			log.Debug("LAN discovery: answering on the local network")
		} else if j, ok := c.(interface{ join() }); ok {
			j.join() // interfaces that came up since
		}
		if err := serveFor(ctx, c, checkEvery, info, locals(), lim, time.Now); err != nil && ctx.Err() == nil {
			log.Debug("LAN discovery: socket failed", "err", err)
			c.close()
			c = nil
			if !wait(time.Second) {
				return
			}
		}
	}
}

// serveFor answers on c for about d, then returns (nil) so that Serve can
// look at the settings and the interfaces again.
func serveFor(ctx context.Context, c packetConn, d time.Duration, info func() (Info, bool),
	locals []netip.Prefix, lim *limiter, now func() time.Time) error {
	end := now().Add(d)
	// A cancelled ctx wakes the read at once.
	stop := context.AfterFunc(ctx, func() { c.setReadDeadline(time.Unix(1, 0)) })
	defer stop()
	if ctx.Err() != nil {
		return nil
	}
	if err := c.setReadDeadline(end); err != nil {
		return err
	}
	buf := make([]byte, 1500)
	for {
		n, src, toGroup, err := c.readFrom(buf)
		if err != nil {
			if isTimeout(err) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if !toGroup || n < 12 || buf[2]&0x80 != 0 {
			continue // only questions to the group are answered, and most of what the group carries are answers
		}
		in, on := info()
		if !on {
			return nil
		}
		out := answer(buf[:n], src, in, locals)
		if out == nil || !lim.allow(now()) {
			continue
		}
		c.writeTo(out, src) // a lost answer is a lost datagram; the browser asks again
	}
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// answer builds the reply to query, received from src, for the host that
// info describes; nil when there is nothing to say. locals are the
// machine's own addresses with their subnets: the asker must be on one of
// them (RFC 6762, section 11), and the reply gives the host's addresses on
// that subnet only, those the asker can reach.
func answer(query []byte, src netip.AddrPort, info Info, locals []netip.Prefix) []byte {
	if !validFingerprint(info.Fingerprint) || info.Port == 0 {
		return nil
	}
	addrs := addrsFor(src.Addr(), info.Addr, locals)
	if len(addrs) == 0 {
		return nil
	}
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil || h.Response || h.OpCode != 0 {
		return nil
	}
	instance, host := instanceName(info), hostName(info)
	var questions []dnsmessage.Question
	var ptr, meta, srv, txt, a bool
	for range maxQuestions {
		q, err := p.Question()
		if err != nil {
			break // the end of the questions, or a malformed one
		}
		if q.Class&^0x8000 != dnsmessage.ClassINET && q.Class&^0x8000 != dnsmessage.ClassANY {
			continue
		}
		questions = append(questions, q)
		name := strings.ToLower(q.Name.String())
		any := q.Type == dnsmessage.TypeALL
		switch {
		case name == Service && (q.Type == dnsmessage.TypePTR || any):
			ptr = true
		case name == metaService && (q.Type == dnsmessage.TypePTR || any):
			meta = true
		case name == strings.ToLower(instance):
			srv = srv || q.Type == dnsmessage.TypeSRV || any
			txt = txt || q.Type == dnsmessage.TypeTXT || any
		case name == host && (q.Type == dnsmessage.TypeA || any):
			a = true
		}
	}
	if !ptr && !meta && !srv && !txt && !a {
		return nil
	}
	// Answers to a legacy unicast question (from a port other than 5353)
	// repeat its ID and question and have short lifetimes; the cache-flush
	// bit is for mDNS queriers only (RFC 6762, sections 6.7 and 10.2).
	legacy := src.Port() != mdnsPort
	ttl, unique := uint32(ttlMDNS), classFlush
	hdr := dnsmessage.Header{Response: true, Authoritative: true}
	if legacy {
		ttl, unique = ttlLegacy, dnsmessage.ClassINET
		hdr.ID = h.ID
	} else {
		questions = nil
	}
	w := &writer{b: dnsmessage.NewBuilder(make([]byte, 0, 512), hdr), ttl: ttl}
	w.b.EnableCompression()
	w.do(w.b.StartQuestions())
	for _, q := range questions {
		w.do(w.b.Question(q))
	}
	// The records asked for are answers; the ones that complete them
	// (the service's SRV, TXT and addresses after its PTR) are additional.
	ptrRec := func() {
		w.do(w.b.PTRResource(w.hdr(Service, dnsmessage.TypePTR, dnsmessage.ClassINET), dnsmessage.PTRResource{PTR: w.name(instance)}))
	}
	metaRec := func() {
		w.do(w.b.PTRResource(w.hdr(metaService, dnsmessage.TypePTR, dnsmessage.ClassINET), dnsmessage.PTRResource{PTR: w.name(Service)}))
	}
	srvRec := func() {
		w.do(w.b.SRVResource(w.hdr(instance, dnsmessage.TypeSRV, unique), dnsmessage.SRVResource{Port: info.Port, Target: w.name(host)}))
	}
	txtRec := func() {
		w.do(w.b.TXTResource(w.hdr(instance, dnsmessage.TypeTXT, unique), dnsmessage.TXTResource{TXT: info.TXT()}))
	}
	aRec := func() {
		for _, ip := range addrs {
			w.do(w.b.AResource(w.hdr(host, dnsmessage.TypeA, unique), dnsmessage.AResource{A: ip.As4()}))
		}
	}
	var answers, extra []func()
	if meta {
		answers = append(answers, metaRec)
	}
	if ptr {
		answers = append(answers, ptrRec)
		extra = append(extra, srvRec, txtRec, aRec)
	} else {
		if srv {
			answers = append(answers, srvRec)
			if !a {
				extra = append(extra, aRec)
			}
		}
		if txt {
			answers = append(answers, txtRec)
		}
		if a {
			answers = append(answers, aRec)
		}
	}
	w.do(w.b.StartAnswers())
	for _, r := range answers {
		r()
	}
	w.do(w.b.StartAdditionals())
	for _, r := range extra {
		r()
	}
	out, err := w.b.Finish()
	if w.do(err); w.err != nil {
		return nil
	}
	return out
}

// writer builds a message, keeping the first error.
type writer struct {
	b   dnsmessage.Builder
	ttl uint32
	err error
}

func (w *writer) do(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *writer) name(s string) dnsmessage.Name {
	n, err := dnsmessage.NewName(s)
	w.do(err)
	return n
}

func (w *writer) hdr(name string, t dnsmessage.Type, class dnsmessage.Class) dnsmessage.ResourceHeader {
	return dnsmessage.ResourceHeader{Name: w.name(name), Type: t, Class: class, TTL: w.ttl}
}

// addrsFor is the host's addresses on the subnet of asker: those of locals
// whose prefix holds asker, or only bound if the host's socket is bound to
// one address. None when the asker is not on any of the machine's subnets.
func addrsFor(asker, bound netip.Addr, locals []netip.Prefix) []netip.Addr {
	asker = asker.Unmap()
	if !asker.Is4() {
		return nil
	}
	bound = bound.Unmap()
	var out []netip.Addr
	for _, p := range locals {
		ip := p.Addr().Unmap()
		if !ip.Is4() || !p.Contains(asker) {
			continue
		}
		if bound.IsValid() && !bound.IsUnspecified() && bound != ip {
			continue
		}
		if len(out) < 4 {
			out = append(out, ip)
		}
	}
	return out
}

// limiter is a token bucket.
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func (l *limiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last.IsZero() {
		l.tokens = l.burst
	} else {
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	}
	l.last = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
