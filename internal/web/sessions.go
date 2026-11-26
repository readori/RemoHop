package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs"
)

// webSession is one logged-in browser: its CSRF secret, its P2P sessions and
// its dials in progress. On loopback it is created by the page (and kept in
// the tab's sessionStorage across reloads); in password mode it is created
// by a login and carried by a cookie.
type webSession struct {
	id      string
	csrf    string
	created time.Time

	mu       sync.Mutex
	last     time.Time
	conns    map[string]*pooled
	attempts map[string]*attempt
	closed   bool
}

// pooled is one P2P session to a host, shared by every feature tab of the
// browser session that opened it. In persistent mode (see keep.go) its
// connection is replaced when it drops and is re-established.
type pooled struct {
	key string
	// redial logs in to the host again (persistent mode); nil: the
	// session ends with its connection.
	redial  Redialer
	stopped chan struct{} // closed when the user (or the gateway) ends the session
	stop1   sync.Once
	kick    chan struct{} // try to reconnect now

	cmu  sync.Mutex
	conn *peer.Conn // the current connection
	info connInfo   // with State and Error

	// guarded by the web session's mu
	users int
	last  time.Time

	fsMu sync.Mutex
	fsc  *fs.Client
}

func newPooled(key string, conn *peer.Conn, info connInfo) *pooled {
	if info.State == "" {
		info.State = stateConnected
	}
	return &pooled{key: key, conn: conn, info: info, stopped: make(chan struct{}), kick: make(chan struct{}, 1)}
}

// current is the session's connection now.
func (p *pooled) current() *peer.Conn {
	p.cmu.Lock()
	defer p.cmu.Unlock()
	return p.conn
}

// snapshot is the session as the page sees it.
func (p *pooled) snapshot() connInfo {
	p.cmu.Lock()
	defer p.cmu.Unlock()
	return p.info
}

func (p *pooled) state() string {
	p.cmu.Lock()
	defer p.cmu.Unlock()
	return p.info.State
}

// stop ends the session for good: no more reconnecting.
func (p *pooled) stop() { p.stop1.Do(func() { close(p.stopped) }) }

// fsClient returns the file client of the session, opening it on first use
// and again after it broke. A host that switched the file feature off
// surfaces its refusal here.
func (p *pooled) fsClient(ctx context.Context) (*fs.Client, error) {
	p.fsMu.Lock()
	defer p.fsMu.Unlock()
	if p.fsc != nil {
		select {
		case <-p.fsc.Done():
			p.fsc = nil
		default:
			return p.fsc, nil
		}
	}
	c, err := fs.NewClient(ctx, p.current())
	if err != nil {
		return nil, err
	}
	p.fsc = c
	return c, nil
}

func (p *pooled) close() {
	p.stop()
	p.fsMu.Lock()
	if p.fsc != nil {
		p.fsc.Close()
		p.fsc = nil
	}
	p.fsMu.Unlock()
	p.current().Close()
}

func dead(c *peer.Conn) bool {
	select {
	case <-c.Done():
		return true
	default:
		return false
	}
}

var errTooManySessions = &apiError{Status: http.StatusServiceUnavailable, Code: "too_many_sessions",
	Message: "this gateway is at its limit of concurrent host sessions; disconnect one first or try later"}

// reserve takes one of the gateway's host-session slots (held for a dial in
// progress and for each live session) and returns its release.
func (s *Server) reserve() (release func(), err *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots >= s.cfg.MaxSessions {
		return nil, errTooManySessions
	}
	s.slots++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.slots--
			s.mu.Unlock()
		})
	}, nil
}

// newSession registers a browser session.
func (s *Server) newSession() (*webSession, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= s.cfg.MaxBrowserSessions {
		return nil, &apiError{Status: http.StatusServiceUnavailable, Code: "too_many_browsers",
			Message: "too many browser sessions on this gateway"}
	}
	now := s.now()
	ws := &webSession{id: randToken(16), csrf: randToken(16), created: now, last: now,
		conns: map[string]*pooled{}, attempts: map[string]*attempt{}}
	if s.cfg.Persistent {
		s.takeOver(ws)
	}
	s.sessions[ws.id] = ws
	return ws, nil
}

func (s *Server) lookupSession(id string) *webSession {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	ws := s.sessions[id]
	s.mu.Unlock()
	if ws == nil {
		return nil
	}
	ws.mu.Lock()
	ok := !ws.closed
	if ok {
		ws.last = s.now()
	}
	ws.mu.Unlock()
	if !ok {
		return nil
	}
	return ws
}

// dropSession closes a browser session and everything it holds.
func (s *Server) dropSession(ws *webSession) {
	s.mu.Lock()
	delete(s.sessions, ws.id)
	for t, tk := range s.tickets {
		if tk.sid == ws.id {
			delete(s.tickets, t)
		}
	}
	s.mu.Unlock()
	ws.mu.Lock()
	ws.closed = true
	conns := ws.conns
	ws.conns = map[string]*pooled{}
	atts := ws.attempts
	ws.attempts = map[string]*attempt{}
	ws.mu.Unlock()
	for _, a := range atts {
		a.cancel()
	}
	for _, p := range conns {
		p.close()
	}
}

var errNotConnected = &apiError{Status: http.StatusGone, Code: "disconnected",
	Message: "not connected to that host (the session ended or was closed for being idle); connect again"}

// errReconnecting: the session dropped and the gateway is establishing it
// again (persistent mode). The page retries once the connection list says
// "connected".
var errReconnecting = &apiError{Status: http.StatusServiceUnavailable, Code: "reconnecting",
	Message: "the connection to the host dropped; reconnecting"}

// acquire returns the live session to key and a release function. While
// acquired, the session is not closed for idleness.
func (ws *webSession) acquire(key string, now func() time.Time) (*pooled, func(), *apiError) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	p := ws.conns[key]
	if p == nil || ws.closed {
		return nil, nil, errNotConnected
	}
	switch info := p.snapshot(); {
	case info.State == stateFailed:
		e := *errNotConnected
		if info.Error != nil && info.Error.Code == "host_disconnected" {
			e.Message = info.Error.Message
		} else if info.Error != nil {
			e.Message = "the connection to the host was lost and could not be re-established: " + info.Error.Message
		}
		return nil, nil, &e
	case info.State != stateConnected || dead(p.current()):
		if p.redial == nil {
			return nil, nil, errNotConnected
		}
		return nil, nil, errReconnecting
	}
	p.users++
	p.last = now()
	var once sync.Once
	return p, func() {
		once.Do(func() {
			ws.mu.Lock()
			p.users--
			p.last = now()
			ws.mu.Unlock()
		})
	}, nil
}

// adopt adds a freshly dialled session to the pool. It reports false (and
// closes nothing) when the browser session was dropped meanwhile.
//
// The goroutine that watches (and in persistent mode reconnects) the session
// is counted in s.wg, so Close returns only once no reconnection is running
// any more; a server that is closing adopts nothing.
func (s *Server) adopt(ws *webSession, p *pooled, release func()) (*pooled, bool) {
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, false
	}
	// Under s.mu, after the check: Close cancels s.ctx before it takes s.mu,
	// so this Add happens before its Wait or not at all.
	s.wg.Add(1)
	s.mu.Unlock()
	ws.mu.Lock()
	if ws.closed {
		ws.mu.Unlock()
		s.wg.Done()
		return nil, false
	}
	old := ws.conns[p.key]
	if old != nil && old.state() == stateConnected && !dead(old.current()) {
		ws.mu.Unlock()
		s.wg.Done()
		return old, false
	}
	p.last = s.now()
	ws.conns[p.key] = p
	ws.mu.Unlock()
	if old != nil {
		old.close() // a session that was reconnecting or had failed: the new one replaces it
	}
	go func() {
		defer s.wg.Done()
		s.keep(ws, p, release)
	}()
	return p, true
}

func (ws *webSession) disconnect(key string) bool {
	ws.mu.Lock()
	p := ws.conns[key]
	delete(ws.conns, key)
	ws.mu.Unlock()
	if p == nil {
		return false
	}
	p.close()
	return true
}

func (ws *webSession) list() []connInfo {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	out := make([]connInfo, 0, len(ws.conns))
	for _, p := range ws.conns {
		info := p.snapshot()
		if p.redial != nil || !dead(p.current()) {
			out = append(out, info) // in persistent mode also while reconnecting, or failed
		}
	}
	return out
}

// tickets are single-use, short-lived capabilities for the requests a page
// cannot decorate with headers: WebSocket upgrades and file downloads. The
// page buys one with an authenticated, CSRF-checked POST and puts it in the
// URL; it is useless to anyone who sees the URL later.
type ticket struct {
	sid     string
	purpose string
	exp     time.Time
}

const (
	ticketLife = 30 * time.Second
	maxTickets = 256
)

func (s *Server) issueTicket(ws *webSession, purpose string) (string, *apiError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, t := range s.tickets {
		if now.After(t.exp) {
			delete(s.tickets, k)
		}
	}
	if len(s.tickets) >= maxTickets {
		return "", &apiError{Status: http.StatusTooManyRequests, Code: "busy", Message: "too many pending requests"}
	}
	t := randToken(16)
	s.tickets[t] = ticket{sid: ws.id, purpose: purpose, exp: now.Add(ticketLife)}
	return t, nil
}

func (s *Server) redeemTicket(t, purpose string) *webSession {
	s.mu.Lock()
	tk, ok := s.tickets[t]
	delete(s.tickets, t)
	s.mu.Unlock()
	if !ok || tk.purpose != purpose || s.now().After(tk.exp) {
		return nil
	}
	return s.lookupSession(tk.sid)
}

// reap closes browser sessions that went quiet and pooled P2P sessions that
// nobody used for a while.
func (s *Server) reap() {
	now := s.now()
	var drop []*webSession
	s.mu.Lock()
	for _, ws := range s.sessions {
		ws.mu.Lock()
		busy := false
		for _, p := range ws.conns {
			if p.users > 0 {
				busy = true
			}
		}
		idle := now.Sub(ws.last)
		switch {
		case s.cfg.Persistent:
			// App mode: the host sessions last until the user ends them.
		case !s.passwordMode() && !busy && idle > s.cfg.PageIdle:
			drop = append(drop, ws) // the tab is gone
		case s.passwordMode() && (now.Sub(ws.created) > s.cfg.SessionTTL || !busy && idle > s.cfg.SessionIdle):
			drop = append(drop, ws)
		}
		ws.mu.Unlock()
	}
	s.mu.Unlock()
	for _, ws := range drop {
		s.dropSession(ws)
	}
	s.mu.Lock()
	sessions := make([]*webSession, 0, len(s.sessions))
	for _, ws := range s.sessions {
		sessions = append(sessions, ws)
	}
	s.mu.Unlock()
	for _, ws := range sessions {
		var stale []string
		ws.mu.Lock()
		for k, p := range ws.conns {
			if !s.cfg.Persistent && p.users == 0 && now.Sub(p.last) > s.cfg.ConnIdle {
				stale = append(stale, k)
			}
		}
		for k, a := range ws.attempts {
			if a.finished() && now.Sub(a.doneAt()) > time.Minute {
				delete(ws.attempts, k)
			}
		}
		ws.mu.Unlock()
		for _, k := range stale {
			ws.disconnect(k)
		}
	}
}

func (s *Server) reapLoop(ctx context.Context) {
	tick := min(max(s.cfg.PageIdle/4, 20*time.Millisecond), 10*time.Second)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reap()
		}
	}
}

var errClosed = errors.New("web: gateway is shutting down")
