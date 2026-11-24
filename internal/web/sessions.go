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
// browser session that opened it.
type pooled struct {
	key  string
	conn *peer.Conn
	info connInfo

	// guarded by the web session's mu
	users int
	last  time.Time

	fsMu sync.Mutex
	fsc  *fs.Client
}

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
	c, err := fs.NewClient(ctx, p.conn)
	if err != nil {
		return nil, err
	}
	p.fsc = c
	return c, nil
}

func (p *pooled) close() {
	p.fsMu.Lock()
	if p.fsc != nil {
		p.fsc.Close()
		p.fsc = nil
	}
	p.fsMu.Unlock()
	p.conn.Close()
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

// acquire returns the live session to key and a release function. While
// acquired, the session is not closed for idleness.
func (ws *webSession) acquire(key string, now func() time.Time) (*pooled, func(), *apiError) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	p := ws.conns[key]
	if p == nil || ws.closed || dead(p.conn) {
		return nil, nil, errNotConnected
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
func (s *Server) adopt(ws *webSession, p *pooled, release func()) (*pooled, bool) {
	ws.mu.Lock()
	if ws.closed {
		ws.mu.Unlock()
		return nil, false
	}
	if old := ws.conns[p.key]; old != nil && !dead(old.conn) {
		ws.mu.Unlock()
		return old, false
	}
	p.last = s.now()
	ws.conns[p.key] = p
	ws.mu.Unlock()
	// Free the slot when the session ends, whoever ends it.
	go func() {
		<-p.conn.Done()
		release()
		ws.mu.Lock()
		if ws.conns[p.key] == p {
			delete(ws.conns, p.key)
		}
		ws.mu.Unlock()
		p.close()
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
		if !dead(p.conn) {
			out = append(out, p.info)
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
			if p.users == 0 && now.Sub(p.last) > s.cfg.ConnIdle {
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
