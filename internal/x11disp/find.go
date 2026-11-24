package x11disp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"

	"github.com/readori/RemoHop/internal/config"
)

func init() {
	// xgb logs "could not get authority info" on every cookie-less
	// connection, which is an expected probe here, not a problem.
	xgb.Logger = log.New(io.Discard, "", 0)
}

// cacheTTL (a variable for tests) is how long a found display is trusted without being checked
// again. Displays come and go under a long-lived daemon, so beyond this the
// cached display must answer a fresh connection before it is reused.
var cacheTTL = 5 * time.Second

// Display is a reachable X display and the credentials that unlock it.
type Display struct {
	// Name is the display string for DISPLAY and ffmpeg's -i, e.g. ":97".
	Name string
	// Screen is the screen number from the display string (usually 0).
	Screen int
	// XAuthority is the Xauthority file child processes must be given as
	// XAUTHORITY, or "" when none is needed (no cookie required). It is the
	// file the working cookie came from, unless that file would not be
	// found by a standard client lookup (the entry is for another hostname,
	// as happens in containers); then it is a private temporary file written
	// for this purpose and XAuthorityTemp is true.
	XAuthority     string
	XAuthorityTemp bool

	network, addr string
	cookie        string // hex; "" connects without a cookie
	finder        *Finder
}

// Open connects to the display with the credentials Find discovered. If it
// fails the cached result is dropped, so the next Find probes again.
func (d *Display) Open(ctx context.Context) (*xgb.Conn, error) {
	c, err := connect(ctx, d.network, d.addr, d.cookie)
	if err != nil {
		if d.finder != nil {
			d.finder.invalidate(d)
		}
		return nil, fmt.Errorf("X display %s: %w", d.Name, err)
	}
	c.DefaultScreen = d.Screen
	return c, nil
}

// Env returns the DISPLAY and XAUTHORITY entries (KEY=value) a child
// process needs; XAUTHORITY is omitted when none is needed.
func (d *Display) Env() []string {
	env := []string{"DISPLAY=" + d.Name}
	if d.XAuthority != "" {
		env = append(env, "XAUTHORITY="+d.XAuthority)
	}
	return env
}

// Merge returns base (nil means os.Environ()) with this display's entries
// replacing any existing DISPLAY and XAUTHORITY.
func (d *Display) Merge(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	over := d.Env()
	out := make([]string, 0, len(base)+len(over))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if k == "DISPLAY" || (k == "XAUTHORITY" && d.XAuthority != "") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, over...)
}

// Error is returned by Find when no display could be used. Notes lists what
// was tried, one entry per probe.
type Error struct{ Notes []string }

func (e *Error) Error() string {
	return "no X display available: " + strings.Join(e.Notes, "; ")
}

// Finder discovers displays and caches the result.
type Finder struct {
	env func() *Env

	mu     sync.Mutex
	cached *Display
	key    string
	at     time.Time
}

// NewFinder returns a Finder over a fixed environment (for tests).
func NewFinder(e *Env) *Finder { return &Finder{env: func() *Env { return e }} }

var defaultFinder = &Finder{env: RealEnv}

// Find resolves the display of the running process with the real
// environment; see Finder.Find.
func Find(ctx context.Context) (*Display, error) { return defaultFinder.Find(ctx) }

// Default is the process-wide Finder behind Find.
func Default() *Finder { return defaultFinder }

func (f *Finder) invalidate(d *Display) {
	f.mu.Lock()
	if f.cached == d {
		f.cached = nil
	}
	f.mu.Unlock()
}

// Find returns a display this process can connect to. An explicit DISPLAY
// (and XAUTHORITY) is tried first, then every local X socket in numeric
// order, each with every cookie from the usual Xauthority locations and
// finally with none. Sockets that refuse connections are skipped, and the
// first display that accepts a connection wins. A previous result is reused
// while the inputs are unchanged and it still answers.
func (f *Finder) Find(ctx context.Context) (*Display, error) {
	e := f.env()
	key := e.key()
	f.mu.Lock()
	c, ck, at := f.cached, f.key, f.at
	f.mu.Unlock()
	if c != nil && ck == key {
		if time.Since(at) < cacheTTL {
			return c, nil
		}
		if x, err := c.Open(ctx); err == nil {
			x.Close()
			f.mu.Lock()
			f.at = time.Now()
			f.mu.Unlock()
			return c, nil
		}
	}
	d, err := probe(ctx, e)
	if err != nil {
		return nil, err
	}
	d.finder = f
	f.mu.Lock()
	f.cached, f.key, f.at = d, key, time.Now()
	f.mu.Unlock()
	return d, nil
}

func candidates(e *Env) (cands []string, notes []string) {
	seen := map[string]bool{}
	add := func(d string) {
		if !seen[d] {
			seen[d] = true
			cands = append(cands, d)
		}
	}
	if d := e.Getenv("DISPLAY"); d != "" {
		add(d)
	} else {
		notes = append(notes, "DISPLAY is not set")
	}
	dir := e.socketDir()
	socks := e.Glob(filepath.Join(dir, "X*"))
	num := func(p string) int { n, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(p), "X")); return n }
	sort.Slice(socks, func(i, j int) bool { return num(socks[i]) < num(socks[j]) })
	for _, s := range socks {
		if n := strings.TrimPrefix(filepath.Base(s), "X"); n != "" && strings.Trim(n, "0123456789") == "" {
			add(":" + n)
		}
	}
	if len(socks) == 0 {
		notes = append(notes, "no X11 sockets in "+dir)
	}
	return cands, notes
}

// located is a cookie plus the file it came from.
type located struct {
	cookie
	file string
}

func probe(ctx context.Context, e *Env) (*Display, error) {
	cands, notes := candidates(e)
	for _, name := range cands {
		host, n, screen, err := parseDisplay(name)
		if err != nil {
			notes = append(notes, fmt.Sprintf("X display %s: %v", name, err))
			continue
		}
		network, addr := "unix", filepath.Join(e.socketDir(), "X"+strconv.Itoa(n))
		if host != "" && host != "unix" {
			network, addr = "tcp", net.JoinHostPort(host, strconv.Itoa(6000+n))
		}

		var cookies []located
		seen := map[string]bool{}
		for _, f := range e.xauthFiles(n) {
			b, err := e.ReadFile(f)
			if err != nil {
				continue
			}
			for _, c := range cookiesFor(parseXauthority(bytes.NewReader(b)), n, e.Hostname) {
				if !seen[c.hex] {
					seen[c.hex] = true
					cookies = append(cookies, located{c, f})
				}
			}
		}
		cookies = append(cookies, located{}) // no cookie: servers started with -ac

		var lastErr error
		for _, ck := range cookies {
			x, err := connect(ctx, network, addr, ck.hex)
			if err != nil {
				lastErr = err
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				// An unreachable socket will not improve with other cookies.
				var oe *net.OpError
				if errors.As(err, &oe) && oe.Op == "dial" {
					break
				}
				continue
			}
			x.Close()
			d := &Display{Name: name, Screen: screen, network: network, addr: addr, cookie: ck.hex}
			if ck.hex != "" {
				d.XAuthority = ck.file
				if !ck.exact {
					if p, err := writeTempXauthority(e.Hostname, n, ck.hex); err == nil {
						d.XAuthority, d.XAuthorityTemp = p, true
					}
				}
			}
			return d, nil
		}
		notes = append(notes, fmt.Sprintf("X display %s: %v", name, lastErr))
	}
	return nil, &Error{Notes: notes}
}

// connect makes one authenticated connection. Every attempt uses its own
// socket because a server drops the connection on refusing authorization.
func connect(ctx context.Context, network, addr, cookie string) (*xgb.Conn, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	nc, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	// A wedged server must not hang the handshake forever.
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	var c *xgb.Conn
	if cookie == "" {
		c, err = xgb.NewConnNet(nc)
	} else {
		c, err = xgb.NewConnNetWithCookieHex(nc, cookie)
	}
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(time.Time{})
	return c, nil
}

// writeTempXauthority writes a private Xauthority file with one entry for
// this hostname, so clients that look cookies up by hostname find a cookie
// that was stored under a different one. It lives in the 0700 remohop state
// directory and is overwritten on each probe.
func writeTempXauthority(hostname string, n int, cookieHex string) (string, error) {
	dir, err := config.SubDir("run")
	if err != nil {
		return "", err
	}
	data := make([]byte, 16)
	for i := range data {
		v, err := strconv.ParseUint(cookieHex[i*2:i*2+2], 16, 8)
		if err != nil {
			return "", err
		}
		data[i] = byte(v)
	}
	b := marshalXauthority([]xauthEntry{{family: 256, addr: hostname, display: strconv.Itoa(n),
		name: "MIT-MAGIC-COOKIE-1", data: data}})
	p := filepath.Join(dir, fmt.Sprintf("xauth-%d-%d", os.Getpid(), n))
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return "", err
	}
	return p, nil
}
