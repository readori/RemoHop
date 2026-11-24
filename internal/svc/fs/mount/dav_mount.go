package mount

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/webdav"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// WebDAV is a remote directory served to this machine's own WebDAV client
// from a loopback HTTP server inside the process, and (unless
// Options.ServeOnly) mounted with that client: mount_webdav on macOS, the
// WebClient redirector on Windows, davfs2 on Linux. No kernel driver has
// to be installed.
//
// The server listens on 127.0.0.1 on a random port and serves only below
// /<secret>/, a 128-bit random path segment; anything else is 404. It
// refuses requests whose Host is not loopback (DNS rebinding) and requests
// that come from a web page (an Origin header, or cross-site fetch
// metadata), so a browser cannot be made to drive the mount. What remains:
// the URL is visible to other local users in the mount table (`mount`,
// `net use`), and anyone on this machine who has it can use the mount,
// with the remote user's rights, for as long as it exists.
type WebDAV struct {
	// Target is where it is mounted: a directory, or a drive ("Z:") on
	// Windows. Empty with ServeOnly.
	Target string
	// Remote is the absolute remote directory.
	Remote string
	// URL is the root of the served tree, secret included.
	URL string

	o      Options
	sess   *session
	ln     net.Listener
	srv    *http.Server
	secret string
	log    reqLog

	once     sync.Once
	done     chan struct{}
	errMu    sync.Mutex
	err      error
	unmounts sync.Once
	uerr     error
}

// NewWebDAV serves the remote directory remote over loopback WebDAV and
// mounts it on target.
func NewWebDAV(ctx context.Context, conn *peer.Conn, remote, target string, o Options) (*WebDAV, error) {
	if o.ReconnectFor == 0 {
		o.ReconnectFor = DefaultReconnect
	}
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*WebDAV, error) {
		c.Close()
		return nil, err
	}
	root, err := c.Realpath(ctx, remote)
	if err != nil {
		return fail(err)
	}
	if rfi, err := c.Stat(ctx, root); err != nil {
		return fail(err)
	} else if !rfi.IsDir() {
		return fail(fmt.Errorf("remote %s is not a directory", root))
	}
	var sb [16]byte
	if _, err := rand.Read(sb[:]); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	d := &WebDAV{Remote: root, o: o, ln: ln, secret: hex.EncodeToString(sb[:]), done: make(chan struct{})}
	port := ln.Addr().(*net.TCPAddr).Port
	d.URL = fmt.Sprintf("http://127.0.0.1:%d/%s/", port, d.secret)
	d.sess = newSession(c, conn, o, func(err error) {
		d.errMu.Lock()
		d.err = err
		d.errMu.Unlock()
		go d.Unmount()
	})
	dfs := newDavFS(root, d.sess.c, o.ReadOnly)
	h := &webdav.Handler{Prefix: "/" + d.secret, FileSystem: dfs, LockSystem: webdav.NewMemLS()}
	d.srv = &http.Server{Handler: d.guard(h, port), ReadHeaderTimeout: 30 * time.Second}
	go d.srv.Serve(ln)

	if o.ServeOnly {
		return d, nil
	}
	t, err := davMount(d.URL, port, d.secret, target, o)
	if err != nil {
		d.shutdown()
		return nil, fmt.Errorf("%w\n%s", err, d.RequestLog())
	}
	d.Target = t
	go d.watch()
	return d, nil
}

// guard is the access check in front of the WebDAV handler.
func (d *WebDAV) guard(h http.Handler, port int) http.Handler {
	prefix := "/" + d.secret
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec.status == 0 {
				rec.status = http.StatusOK // an implicit 200 with no body
			}
			d.log.add(r, rec.status, d.secret, d.o.Logf, d.o.Debug)
		}()
		w = rec
		// Only this machine, addressed as loopback: a page that makes a
		// browser resolve its own name to 127.0.0.1 still sends its name.
		host, hport, err := net.SplitHostPort(r.Host)
		if err != nil || hport != strconv.Itoa(port) || (host != "127.0.0.1" && !strings.EqualFold(host, "localhost")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Never a web page: WebDAV clients send neither header.
		if r.Header.Get("Origin") != "" || (r.Header.Get("Sec-Fetch-Site") != "" && r.Header.Get("Sec-Fetch-Site") != "none") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		p := r.URL.Path
		seg, rest, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
		ok := subtle.ConstantTimeCompare([]byte(seg), []byte(d.secret)) == 1
		if !ok {
			// Clients probe the server root before the share (Windows:
			// OPTIONS and PROPFIND on "/"). They learn that this is a
			// WebDAV server with an empty root, and nothing else.
			switch {
			case r.Method == http.MethodOptions:
				w.Header().Set("DAV", "1, 2")
				w.Header().Set("MS-Author-Via", "DAV")
				w.Header().Set("Allow", "OPTIONS, PROPFIND")
				w.WriteHeader(http.StatusOK)
			case r.Method == "PROPFIND" && p == "/":
				w.Header().Set("Content-Type", "text/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)
			default:
				http.NotFound(w, r)
			}
			return
		}
		_ = rest
		if d.o.ReadOnly {
			switch r.Method {
			case "PUT", "DELETE", "MKCOL", "MOVE", "COPY", "PROPPATCH", "POST", "PATCH":
				http.Error(w, "read-only mount", http.StatusForbidden)
				return
			}
		}
		if p == prefix {
			r.URL.Path = prefix + "/"
		}
		w.Header().Set("MS-Author-Via", "DAV")
		h.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// reqLog keeps the last requests, for diagnosing a client that will not
// mount. The secret is not recorded.
type reqLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *reqLog) add(r *http.Request, status int, secret string, logf func(string, ...any), debug bool) {
	p := strings.Replace(r.URL.Path, secret, "<secret>", 1)
	line := fmt.Sprintf("%s %s %s -> %d", time.Now().Format("15:04:05.000"), r.Method, p, status)
	if dst := r.Header.Get("Destination"); dst != "" {
		line += " dest=" + strings.Replace(dst, secret, "<secret>", 1)
	}
	if ua := r.UserAgent(); ua != "" {
		line += " (" + ua + ")"
	}
	l.mu.Lock()
	if len(l.lines) >= 400 {
		l.lines = append(l.lines[:0], l.lines[200:]...)
	}
	l.lines = append(l.lines, line)
	l.mu.Unlock()
	if debug && logf != nil {
		logf("webdav: %s", line)
	}
}

// RequestLog returns the most recent requests the server handled, one per
// line, without the secret.
func (d *WebDAV) RequestLog() string {
	d.log.mu.Lock()
	defer d.log.mu.Unlock()
	return strings.Join(d.log.lines, "\n")
}

// watch notices a mount removed from outside (umount, `net use /delete`).
func (d *WebDAV) watch() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-t.C:
			if !davMounted(d.Target) {
				d.shutdown()
				return
			}
		}
	}
}

func (d *WebDAV) shutdown() {
	d.once.Do(func() {
		d.sess.close()
		d.srv.Close()
		close(d.done)
	})
}

// Done is closed once the directory is no longer served.
func (d *WebDAV) Done() <-chan struct{} { return d.done }

// Err reports why the mount ended on its own.
func (d *WebDAV) Err() error {
	d.errMu.Lock()
	defer d.errMu.Unlock()
	return d.err
}

// Unmount unmounts and stops serving. The server keeps answering while the
// OS client unmounts (it flushes through it), but not for long: after a
// few seconds the unmount is forced.
func (d *WebDAV) Unmount() error {
	d.unmounts.Do(func() {
		if d.Target != "" && davMounted(d.Target) {
			res := make(chan error, 1)
			go func() { res <- davUnmount(d.Target, false) }()
			var err error
			select {
			case err = <-res:
			case <-time.After(3 * time.Second):
				err = errors.New("timed out")
			}
			if err != nil {
				// Stop serving so nothing can block on us, then force.
				d.sess.close()
				if ferr := davUnmount(d.Target, true); ferr != nil && davMounted(d.Target) {
					d.uerr = fmt.Errorf("unmount %s: %v (forced: %v)", d.Target, err, ferr)
				}
			}
		}
		d.shutdown()
	})
	return d.uerr
}

// session holds the rfs client of a mount and replaces it when the session
// is lost, as the FUSE mount does: while it is down every operation fails
// at once; if it cannot be restored within Options.ReconnectFor, onFail is
// called (and the mount unmounts).
type session struct {
	client atomic.Pointer[rfs.Client]
	conn0  *peer.Conn
	o      Options
	cancel context.CancelFunc
	onFail func(error)
}

func newSession(c *rfs.Client, conn *peer.Conn, o Options, onFail func(error)) *session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{conn0: conn, o: o, cancel: cancel, onFail: onFail}
	s.client.Store(c)
	go s.supervise(ctx)
	return s
}

func (s *session) c() *rfs.Client { return s.client.Load() }

func (s *session) logf(format string, args ...any) {
	if s.o.Logf != nil {
		s.o.Logf(format, args...)
	}
}

// close ends reconnecting and fails everything in flight.
func (s *session) close() {
	s.cancel()
	c := s.c()
	c.Close()
	if c.Conn() != s.conn0 {
		c.Conn().Close()
	}
}

func (s *session) supervise(ctx context.Context) {
	for {
		c := s.c()
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
		}
		if ctx.Err() != nil {
			return
		}
		s.logf("session lost")
		c.Conn().Close()
		nc, err := s.reconnect(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logf("%v; unmounting", err)
				s.onFail(err)
			}
			return
		}
		s.client.Store(nc)
		if ctx.Err() != nil { // closed while redialling
			nc.Close()
			nc.Conn().Close()
			return
		}
		s.logf("reconnected")
	}
}

func (s *session) reconnect(ctx context.Context) (*rfs.Client, error) {
	if s.o.Redial == nil || s.o.ReconnectFor < 0 {
		return nil, errors.New("session lost")
	}
	deadline := time.Now().Add(s.o.ReconnectFor)
	backoff := 500 * time.Millisecond
	var lastErr error
	for time.Now().Before(deadline) {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, err := s.o.Redial(dctx)
		var c *rfs.Client
		if err == nil {
			if c, err = rfs.NewClient(dctx, conn); err != nil {
				conn.Close()
			}
		}
		cancel()
		if err == nil {
			return c, nil
		}
		lastErr = err
		s.logf("reconnect failed: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
	return nil, fmt.Errorf("session lost and reconnecting failed for %s: %v", s.o.ReconnectFor, lastErr)
}
