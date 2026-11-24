package web

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

// Persistent sessions (Config.Persistent, the phone apps).
//
// On a phone a connection to a host drops for reasons that have nothing to
// do with the user: iOS suspends the app (its sockets go with it), the
// phone moves from Wi-Fi to cellular, a lift has no signal for a minute.
// The user expects the session to be there when they look again, until
// they press Disconnect. So in persistent mode:
//
//   - nothing is closed for being idle;
//   - when a session's connection ends and the user did not end it, the
//     session stays in the page's list with state "reconnecting" and the
//     gateway logs in to the host again (Redialer), with backoff from one
//     second up to 30 seconds, and at once when the app says the network
//     or the app's state changed (ReconnectNow, CheckSessions);
//   - while the host asks its owner to approve the new login the state is
//     "waiting_approval";
//   - a failure that another attempt cannot fix (the credential is
//     refused, the owner denied, the host's key changed) ends the attempts:
//     state "failed" with the reason, until the user disconnects or
//     connects again;
//   - the page re-opens its tab when the state is "connected" again: the
//     terminal re-attaches to the same shell session (the host keeps a
//     detached terminal), the file list reloads, the desktop restarts.
//
// How to log in again is the Redialer's business; see RedialFunc and
// internal/mobile. The default re-runs Dial with the password typed for
// this session, held in memory only.

const (
	stateConnected = "connected"
	stateReconnect = "reconnecting"
	stateApproval  = "waiting_approval"
	stateFailed    = "failed"

	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// Redialer logs in to the host of a persistent session again. notify gets
// the dial's progress notices, as DialRequest.Notify does.
type Redialer func(ctx context.Context, notify func(string)) (*peer.Conn, error)

// RedialFunc is asked, right after a connect, how to log in to the host
// again later without the user: conn is the new session, req the request
// that made it (with the password the user typed, if any). It may use conn
// (for example to ask the host for a pairing token to keep in memory). A
// nil Redialer or an error leaves the session without reconnection.
type RedialFunc func(ctx context.Context, conn *peer.Conn, req DialRequest) (Redialer, error)

// redialer returns how to reconnect a session that was just established.
func (s *Server) redialer(ctx context.Context, conn *peer.Conn, req DialRequest, paired bool) Redialer {
	if s.cfg.Redial != nil {
		r, err := s.cfg.Redial(ctx, conn, req)
		if err != nil {
			s.log.Warn("web: this session will not reconnect by itself", "err", err)
			return nil
		}
		return r
	}
	if paired {
		req.Password = "" // the pairing saved just now logs in from here on
	}
	return func(ctx context.Context, notify func(string)) (*peer.Conn, error) {
		r := req
		r.Notify = notify
		return s.cfg.Dial(ctx, r)
	}
}

// permanent reports whether a failed reconnection is not worth repeating:
// the host refused the credential or the owner said no, rather than the
// network being in the way.
func permanent(err error, ae *apiError) bool {
	switch ae.Code {
	case "bad_password", "denied", "pairing_rejected", "key_rejected", "password_login_disabled",
		"host_key_changed", "password_required", "host_refused", "feature_off", "unknown_id",
		"signal_is_host", "signal_wrong_protocol", "host_locked_out":
		return true
	}
	return errors.Is(err, dial.ErrNoCredential)
}

// keep watches a session until it ends for good, reconnecting it in
// persistent mode. release frees its slot at the very end.
func (s *Server) keep(ws *webSession, p *pooled, release func()) {
	defer func() {
		release()
		s.forget(p)
		p.close()
	}()
	for {
		conn := p.current()
		select {
		case <-conn.Done():
		case <-p.stopped:
			return
		case <-s.ctx.Done():
			return
		}
		if p.redial == nil || s.ctx.Err() != nil {
			return
		}
		select {
		case <-p.stopped:
			return
		default:
		}
		if !s.reconnect(p) {
			// Failed for good: listed as failed until the user acts.
			select {
			case <-p.stopped:
			case <-s.ctx.Done():
			}
			return
		}
	}
}

// reconnect establishes p's connection again; false when that cannot work
// or the session was ended meanwhile.
func (s *Server) reconnect(p *pooled) bool {
	p.setState(stateReconnect, nil, 0)
	s.log.Info("web: a host session dropped; reconnecting", "target", p.key)
	wait := reconnectMin
	for try := 1; ; try++ {
		ctx, cancel := context.WithTimeout(s.ctx, s.cfg.DialTimeout)
		go func() { // the user ending the session cancels the attempt
			select {
			case <-p.stopped:
				cancel()
			case <-ctx.Done():
			}
		}()
		conn, err := p.redial(ctx, func(msg string) {
			if strings.Contains(msg, "approval") {
				p.setState(stateApproval, nil, try)
			}
		})
		cancel()
		select {
		case <-p.stopped:
			if conn != nil {
				conn.Close()
			}
			return false
		default:
		}
		if s.ctx.Err() != nil {
			if conn != nil {
				conn.Close()
			}
			return false
		}
		if err == nil {
			ci := describe(conn, p.key)
			p.cmu.Lock()
			old := p.info
			ci.Key, ci.Target = old.Key, old.Target // the page's handle stays
			ci.Paired = old.Paired
			ci.NewHost = false
			ci.Reconnects = old.Reconnects + 1
			p.conn, p.info = conn, ci
			p.cmu.Unlock()
			p.fsMu.Lock()
			if p.fsc != nil {
				p.fsc.Close()
				p.fsc = nil
			}
			p.fsMu.Unlock()
			s.log.Info("web: host session re-established", "target", p.key, "attempts", try)
			return true
		}
		ae := classify(err)
		if permanent(err, ae) {
			p.setState(stateFailed, ae, try)
			s.log.Info("web: host session cannot be re-established", "target", p.key, "code", ae.Code)
			return false
		}
		p.setState(stateReconnect, ae, try)
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-p.kick:
			t.Stop()
		case <-p.stopped:
			t.Stop()
			return false
		case <-s.ctx.Done():
			t.Stop()
			return false
		}
		wait = min(wait*2, reconnectMax)
	}
}

func (p *pooled) setState(state string, err *apiError, retries int) {
	p.cmu.Lock()
	p.info.State, p.info.Error, p.info.Retries = state, err, retries
	p.cmu.Unlock()
}

// forget removes p from whichever browser session holds it.
func (s *Server) forget(p *pooled) {
	s.mu.Lock()
	all := make([]*webSession, 0, len(s.sessions))
	for _, ws := range s.sessions {
		all = append(all, ws)
	}
	s.mu.Unlock()
	for _, ws := range all {
		ws.mu.Lock()
		if ws.conns[p.key] == p {
			delete(ws.conns, p.key)
		}
		ws.mu.Unlock()
	}
}

// takeOver moves the host sessions of every other browser session to ws
// and drops those sessions: in persistent mode there is one user, and a
// page that starts a new browser session (its storage is gone, or the
// gateway moved to a new address) is that user. s.mu must be held.
func (s *Server) takeOver(ws *webSession) {
	for id, old := range s.sessions {
		if old == ws {
			continue
		}
		old.mu.Lock()
		for k, p := range old.conns {
			if ws.conns[k] == nil {
				ws.conns[k] = p
			} else {
				defer p.close()
			}
		}
		old.conns = map[string]*pooled{}
		old.closed = true
		atts := old.attempts
		old.attempts = map[string]*attempt{}
		old.mu.Unlock()
		for _, a := range atts {
			a.cancel()
		}
		delete(s.sessions, id)
	}
}

// allPooled returns every pooled session of the gateway.
func (s *Server) allPooled() []*pooled {
	s.mu.Lock()
	sessions := make([]*webSession, 0, len(s.sessions))
	for _, ws := range s.sessions {
		sessions = append(sessions, ws)
	}
	s.mu.Unlock()
	var out []*pooled
	for _, ws := range sessions {
		ws.mu.Lock()
		for _, p := range ws.conns {
			out = append(out, p)
		}
		ws.mu.Unlock()
	}
	return out
}

// ReconnectNow makes every session that is waiting to reconnect try at
// once (the app is back in the foreground, or the network changed).
func (s *Server) ReconnectNow() {
	for _, p := range s.allPooled() {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

// checkTimeout bounds CheckSessions' ping of each session.
const checkTimeout = 3 * time.Second

// CheckSessions pings every connected session and closes the ones that do
// not answer within three seconds, so that they reconnect now rather than
// when QUIC's idle timeout notices (up to 45 seconds later); then it does
// what ReconnectNow does. The apps call it when they return to the
// foreground and when the network changes. It returns when the checks are
// done.
func (s *Server) CheckSessions() {
	var wg sync.WaitGroup
	for _, p := range s.allPooled() {
		if p.redial == nil || p.state() != stateConnected {
			continue
		}
		conn := p.current()
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(s.ctx, checkTimeout)
			defer cancel()
			if _, err := hostd.Ping(ctx, conn, hostd.PingOptions{Count: 1}); err != nil && s.ctx.Err() == nil && !dead(conn) {
				if _, refused := errors.AsType[*peer.RemoteError](err); refused {
					return // it answered, just not to this
				}
				s.log.Info("web: a host session does not answer; reconnecting", "target", p.key)
				conn.CloseWithReason("no answer to a liveness check")
			}
		}()
	}
	wg.Wait()
	s.ReconnectNow()
}

// DropSessionsForTest closes the connection of every host session as a
// network outage would (the sessions themselves stay and, in persistent
// mode, reconnect). For tests of the reconnection.
func (s *Server) DropSessionsForTest() {
	for _, p := range s.allPooled() {
		p.current().CloseWithReason("connection dropped by a test")
	}
}

// Sessions lists every host session of the gateway (all browser sessions),
// as the page sees them.
func (s *Server) Sessions() []connInfo {
	out := []connInfo{}
	for _, p := range s.allPooled() {
		if p.redial != nil || !dead(p.current()) {
			out = append(out, p.snapshot())
		}
	}
	return out
}

// DisconnectTarget ends the host sessions to target (a canonical target:
// 9-digit ID or host:port), as when the user forgets that host.
func (s *Server) DisconnectTarget(target string) {
	s.disconnectWhere(func(p *pooled) bool { return p.snapshot().Target == target })
}

// DisconnectAll ends every host session, as the user's Disconnect does
// (the Android app's notification offers it).
func (s *Server) DisconnectAll() { s.disconnectWhere(func(*pooled) bool { return true }) }

func (s *Server) disconnectWhere(match func(*pooled) bool) {
	s.mu.Lock()
	sessions := make([]*webSession, 0, len(s.sessions))
	for _, ws := range s.sessions {
		sessions = append(sessions, ws)
	}
	s.mu.Unlock()
	for _, ws := range sessions {
		ws.mu.Lock()
		var keys []string
		for k, p := range ws.conns {
			if match(p) {
				keys = append(keys, k)
			}
		}
		ws.mu.Unlock()
		for _, k := range keys {
			ws.disconnect(k)
		}
	}
}
