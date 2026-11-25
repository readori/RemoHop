package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("web: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func ctEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// isLoopbackAddr reports whether every address the listen address can bind
// to is a loopback address. A bare port, an empty host and the unspecified
// addresses listen on every interface and are not loopback.
func isLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("bad listen address %q: %v", addr, err)
	}
	if host == "" {
		return false, nil
	}
	if ip, err := netip.ParseAddr(strings.TrimSuffix(strings.SplitN(host, "%", 2)[0], "]")); err == nil {
		return ip.IsLoopback(), nil
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false, fmt.Errorf("cannot resolve listen host %q", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false, nil
		}
	}
	return true, nil
}

// loadPassword reads the gateway password file: the whole content minus
// trailing line breaks.
func loadPassword(path string) ([]byte, error) { return loadSecret(path, "the gateway password") }

// loadSecret reads what (a password or token) from a private file.
func loadSecret(path, what string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %04o); run chmod 600 on it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimRight(string(b), "\r\n"))
	if len(b) < minPasswordLen {
		return nil, fmt.Errorf("%s in %s must be at least %d characters", what, path, minPasswordLen)
	}
	return b, nil
}

const minPasswordLen = 8

// passwordMatches compares in constant time (hashing first hides the length).
func passwordMatches(want [sha256.Size]byte, got string) bool {
	h := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(want[:], h[:]) == 1
}

// hostMatcher decides which Host headers are acceptable. Checking the Host
// header defeats DNS rebinding: a hostile name that resolves to the gateway
// arrives with that name in Host.
type hostMatcher struct {
	names map[string]bool // lower-case host names and IP literals
	wild  []string        // suffixes of "*.example.com" entries
	port  string
	tls   bool
}

func newHostMatcher(port int, tls bool, names ...string) *hostMatcher {
	m := &hostMatcher{names: map[string]bool{}, port: strconv.Itoa(port), tls: tls}
	for _, n := range names {
		m.add(n)
	}
	return m
}

func (m *hostMatcher) add(n string) {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" {
		return
	}
	if rest, ok := strings.CutPrefix(n, "*."); ok {
		m.wild = append(m.wild, "."+rest)
		return
	}
	if ip, err := netip.ParseAddr(n); err == nil {
		n = ip.String()
	}
	m.names[n] = true
}

func (m *hostMatcher) addCert(c *x509.Certificate) {
	for _, n := range c.DNSNames {
		m.add(n)
	}
	for _, ip := range c.IPAddresses {
		m.add(ip.String())
	}
	if c.Subject.CommonName != "" && !strings.ContainsAny(c.Subject.CommonName, " ") {
		m.add(c.Subject.CommonName)
	}
}

func (m *hostMatcher) ok(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = strings.Trim(hostport, "[]"), "80"
		if m.tls {
			port = "443"
		}
	}
	if port != m.port {
		return false
	}
	host = strings.ToLower(host)
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	}
	if m.names[host] {
		return true
	}
	for _, suf := range m.wild {
		if strings.HasSuffix(host, suf) && len(host) > len(suf) && !strings.Contains(host[:len(host)-len(suf)], ".") {
			return true
		}
	}
	return false
}

// sameOrigin reports whether the Origin header names this very server: same
// scheme and the (already validated) Host.
// crossSite reports whether a browser says r comes from another site: an
// Origin header that is not the gateway's own, or a Sec-Fetch-Site other
// than same-origin (or none: the user typed the address). A request with
// neither header says nothing, and is not refused here.
func crossSite(r *http.Request, tls bool) bool {
	if r.Header.Get("Origin") != "" && !sameOrigin(r, tls) {
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return false
	}
	return true
}

// fromBrowser reports whether r carries what browsers send and plain
// programs do not: Origin, Sec-Fetch-* or cookies. A gateway that accepts
// Authorization: Bearer cannot tell who added that header (a reverse
// proxy may inject it into every request), so a bearer request that looks
// like a browser's gets the browser checks as well.
func fromBrowser(r *http.Request) bool {
	h := r.Header
	return h.Get("Origin") != "" || h.Get("Sec-Fetch-Site") != "" || h.Get("Sec-Fetch-Mode") != "" || h.Get("Cookie") != ""
}

func sameOrigin(r *http.Request, tls bool) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Path != "" && u.Path != "/" || u.User != nil {
		return false
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

// limiter slows password guessing at the gateway login: failures are counted
// per source (an IPv6 source by its /64), the first few are free, then the
// source is locked out for exponentially longer, and a global bucket caps
// failures across all sources. A source that is locked out is refused
// without its password being looked at, so a lockout is not an oracle.
//
// The global bucket would let anyone who keeps it empty lock everyone out,
// so a source that logged in successfully within knownFor is exempt from it
// (not from its own lockout): strangers spending the global budget cannot
// keep the gateway's users out, only other strangers.
type limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	per    map[string]*srcState
	known  map[string]time.Time // source → last successful login
	tokens float64              // global failure budget
	last   time.Time
}

type srcState struct {
	fails  int
	locked time.Time
	seen   time.Time
}

const (
	freeFailures   = 3
	baseLockout    = 5 * time.Second
	maxLockout     = 15 * time.Minute
	globalBurst    = 30
	globalRefill   = 2 * time.Second
	maxTrackedSrcs = 10000
	knownFor       = 30 * 24 * time.Hour
	maxKnownSrcs   = 1000
)

func newLimiter(now func() time.Time) *limiter {
	return &limiter{now: now, per: map[string]*srcState{}, known: map[string]time.Time{}, tokens: globalBurst, last: now()}
}

func srcKey(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap()
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}

func (l *limiter) refill(now time.Time) {
	l.tokens = min(globalBurst, l.tokens+float64(now.Sub(l.last))/float64(globalRefill))
	l.last = now
}

// allow reports whether src may try a password now, and otherwise how long
// it must wait.
func (l *limiter) allow(src string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.refill(now)
	if s := l.per[src]; s != nil && now.Before(s.locked) {
		return false, s.locked.Sub(now)
	}
	if l.tokens < 1 && !l.isKnown(src, now) {
		return false, time.Duration((1 - l.tokens) * float64(globalRefill))
	}
	return true, 0
}

func (l *limiter) isKnown(src string, now time.Time) bool {
	t, ok := l.known[src]
	return ok && now.Sub(t) < knownFor
}

func (l *limiter) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.refill(now)
	if !l.isKnown(src, now) {
		l.tokens--
	}
	if len(l.per) >= maxTrackedSrcs {
		for k, s := range l.per {
			if now.Sub(s.seen) > time.Hour {
				delete(l.per, k)
			}
		}
	}
	s := l.per[src]
	if s == nil || now.Sub(s.seen) > 30*time.Minute {
		s = &srcState{}
		l.per[src] = s
	}
	s.seen = now
	s.fails++
	if s.fails > freeFailures {
		d := baseLockout << min(s.fails-freeFailures-1, 20)
		s.locked = now.Add(min(d, maxLockout))
	}
}

func (l *limiter) success(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.per, src)
	now := l.now()
	if _, ok := l.known[src]; !ok && len(l.known) >= maxKnownSrcs {
		oldest, first := "", now
		for k, t := range l.known {
			if !t.After(first) {
				oldest, first = k, t
			}
		}
		delete(l.known, oldest)
	}
	l.known[src] = now
}

// realIP takes the visitor's address from the values of the real-IP header:
// the last address of the last value, which is the one the nearest proxy
// added (X-Forwarded-For grows to the right; CF-Connecting-IP has one).
func realIP(values []string) (netip.Addr, bool) {
	if len(values) == 0 {
		return netip.Addr{}, false
	}
	v := values[len(values)-1]
	if i := strings.LastIndexByte(v, ','); i >= 0 {
		v = v[i+1:]
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(v))
	return ip, err == nil
}

// checkHeaderName validates a --real-ip-header value.
func checkHeaderName(name string) error {
	if name == "" {
		return nil
	}
	for _, r := range name {
		if r > 0x7e || r <= ' ' || strings.ContainsRune("\"(),/:;<=>?@[\\]{}", r) {
			return fmt.Errorf("--real-ip-header %q is not a header name", name)
		}
	}
	return nil
}

var errNoTLSPassword = errors.New("refusing to listen on a non-loopback address without --tls-cert/--tls-key and --password-file: " +
	"the gateway can drive every host it is connected to, so anything beyond localhost needs TLS and its own login")
