package web

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Listener lifecycle.
//
// A desktop gateway's listener lives as long as the process. The phone apps
// run the gateway inside the app, and iOS takes the resources of a
// suspended app's sockets away (Apple's TN2277, "Networking and
// Multitasking"): when the app is resumed its listening socket is
// "defunct". Connections to it are refused, while accept fails with
// ECONNABORTED, which Go's accept loop treats as a harmless race and
// retries, so http.Server.Serve never returns and nothing in the process
// notices. The page in the app's web view then fails every request
// ("Load failed").
//
// So the listener can be checked and replaced without touching anything
// else: the browser sessions, the host sessions, the token. Healthy makes a
// real HTTP request to the listener from inside the process; Rebind closes
// it and listens again, on the same port when possible so the page's URL
// stays valid; PauseListening closes it before the app is suspended (what
// TN2277 recommends), and Rebind reopens it on return. Serve outlives all
// of these.
//
// If the same port cannot be had again, Rebind listens on a new one and,
// in loopback mode, picks a new token as well: whoever holds the old port
// may have received the page's requests, which carry the token. The page
// must then be loaded again with URL (on a new origin it starts a new
// browser session; the old one is reaped after PageIdle).

// binding is the current listener and what depends on its address. It is
// replaced as a whole.
type binding struct {
	ln    net.Listener // nil while paused or after a failed Rebind
	addr  *net.TCPAddr
	token string // loopback mode: the URL's path prefix
	hosts *hostMatcher
}

var errPaused = errors.New("web: the listener is paused")

func (s *Server) newBinding(ln net.Listener, addr *net.TCPAddr, token string) *binding {
	return &binding{ln: &guardedListener{Listener: ln, done: make(chan struct{})}, addr: addr, token: token,
		hosts: hostsFor(addr, s.useTLS, s.leaf)}
}

func hostsFor(addr *net.TCPAddr, tls bool, leaf *x509.Certificate) *hostMatcher {
	m := newHostMatcher(addr.Port, tls, "localhost", "127.0.0.1", "::1")
	if !addr.IP.IsUnspecified() {
		m.add(addr.IP.String())
	}
	if leaf != nil {
		m.addCert(leaf)
	}
	return m
}

// replace installs b and wakes Serve. lnMu must be held.
func (s *Server) replace(b *binding, err error) {
	s.bind.Store(b)
	s.lnErr = err
	close(s.lnChanged)
	s.lnChanged = make(chan struct{})
}

// Listening reports whether the server has a listener that has not failed
// as far as the server can tell. It does no I/O; Healthy checks for real.
func (s *Server) Listening() bool {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	return s.bind.Load().ln != nil && s.lnErr == nil && s.ctx.Err() == nil
}

// Healthy makes an HTTP request to the listener from inside the process and
// returns nil if any HTTP answer came back before ctx ended (the request
// carries no token, so the answer is a 404). Without a deadline on ctx it
// gives up after two seconds.
func (s *Server) Healthy(ctx context.Context) error {
	s.lnMu.Lock()
	b, lerr := s.bind.Load(), s.lnErr
	s.lnMu.Unlock()
	switch {
	case s.ctx.Err() != nil:
		return net.ErrClosed
	case lerr != nil:
		return lerr
	case b.ln == nil:
		return errPaused
	}
	return probe(ctx, b.addr)
}

func probe(ctx context.Context, addr *net.TCPAddr) error {
	ip := addr.IP
	if ip.IsUnspecified() {
		ip = net.IPv4(127, 0, 0, 1)
		if addr.IP.To4() == nil {
			ip = net.IPv6loopback
		}
	}
	hostport := net.JoinHostPort(ip.String(), strconv.Itoa(addr.Port))
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return err
	}
	defer c.Close()
	dl, _ := ctx.Deadline()
	c.SetDeadline(dl)
	stop := context.AfterFunc(ctx, func() { c.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	if _, err := io.WriteString(c, "GET /.health HTTP/1.1\r\nHost: "+hostport+"\r\nConnection: close\r\n\r\n"); err != nil {
		return err
	}
	var head [7]byte
	if _, err := io.ReadFull(c, head[:]); err != nil {
		return fmt.Errorf("web: no answer from the listener: %w", err)
	}
	if string(head[:]) != "HTTP/1." {
		return errors.New("web: the listener answered something other than HTTP")
	}
	return nil
}

// Rebind closes the listener (if any) and listens again on the same
// address. If that port cannot be had it listens on a new one, with a new
// token in loopback mode; URL says where the page must go. Browser and host
// sessions are untouched either way.
func (s *Server) Rebind() error {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ctx.Err() != nil {
		return net.ErrClosed
	}
	old := s.bind.Load()
	if old.ln != nil {
		old.ln.Close()
	}
	var ln net.Listener
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(30 * time.Millisecond)
		}
		if ln, err = net.Listen("tcp", old.addr.String()); err == nil {
			break
		}
	}
	token := old.token
	if err != nil {
		s.log.Warn("web: cannot listen on the same port again; using a new one", "err", err)
		if ln, err = net.Listen("tcp", net.JoinHostPort(old.addr.IP.String(), "0")); err != nil {
			s.replace(&binding{addr: old.addr, token: old.token, hosts: old.hosts}, err)
			return err
		}
		if token != "" {
			token = randToken(16)
		}
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		err := errors.New("web: not a TCP listener")
		s.replace(&binding{addr: old.addr, token: old.token, hosts: old.hosts}, err)
		return err
	}
	s.replace(s.newBinding(ln, tcp, token), nil)
	return nil
}

// PauseListening closes the listener and keeps everything else; Rebind
// opens it again. The iOS app calls it when it goes to the background, so
// that the system has no listening socket to take away and the port is
// free to listen on again when the app returns.
func (s *Server) PauseListening() {
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	old := s.bind.Load()
	if old.ln == nil || s.ctx.Err() != nil {
		return
	}
	old.ln.Close()
	s.replace(&binding{addr: old.addr, token: old.token, hosts: old.hosts}, errPaused)
	s.closeIdle()
}

// closeIdle closes the browser's idle keep-alive connections: they would
// not survive the app's suspension either, and a browser that finds one
// closed opens a new connection instead of failing a request on it.
func (s *Server) closeIdle() {
	s.srv.SetKeepAlivesEnabled(false) // closes the idle connections
	s.srv.SetKeepAlivesEnabled(true)
}

// BreakListenerForTest breaks the listener the way a phone's OS can, for
// tests of the recovery: the socket is closed underneath the server, and
// the idle keep-alive connections go too (iOS takes those as well). With
// stall, Serve does not notice (as with iOS's defunct sockets: connections
// are refused but Accept does not return and Listening stays true);
// without, Accept fails and Serve records the failure.
func (s *Server) BreakListenerForTest(stall bool) {
	s.lnMu.Lock()
	b := s.bind.Load()
	s.lnMu.Unlock()
	gl, ok := b.ln.(*guardedListener)
	if !ok {
		return
	}
	if stall {
		gl.stalled.Store(true)
	}
	closeSoon(gl.Listener)
	s.closeIdle()
}

// guardedListener is the listener Serve uses. Its Close does not wait
// forever for the socket (closing a Go socket waits for an Accept in
// progress to return, which a defunct socket may never let happen), and it
// implements BreakListenerForTest's stall.
type guardedListener struct {
	net.Listener
	stalled atomic.Bool
	done    chan struct{}
	once    sync.Once
}

func (l *guardedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil && l.stalled.Load() {
		<-l.done
		return nil, net.ErrClosed
	}
	return c, err
}

func (l *guardedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return closeSoon(l.Listener)
}

// closeSoon closes c, waiting at most half a second for that to finish.
func closeSoon(c io.Closer) error {
	ch := make(chan error, 1)
	go func() { ch <- c.Close() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(500 * time.Millisecond):
		return nil
	}
}
