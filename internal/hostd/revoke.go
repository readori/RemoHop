package hostd

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
)

// Withdrawing a feature from sessions that are already connected.
//
// The session layer's Authorize hook only decides whether a stream may
// open. A feature switched off later (`remohop config feature X off`, a
// front end's Overrides.Features, a grant that no longer allows it) must
// also end what is already open with it: a terminal, a file upload, a
// forwarded connection. Every service stream therefore runs under
// guardStream, which watches the stream's feature for as long as the
// handler runs and, when the feature goes away, cancels the handler's
// context (with a perm.WithdrawnError as its cause, so the service can say
// why) and, shortly after, resets the stream for services that do not
// react to that. Services that do more than their own feature covers watch
// the rest themselves with WatchFeature: the desktop stream (feature
// screen) stops taking keyboard and mouse input when input goes.

const (
	// watchEvery is how often the host re-reads what it allows for the
	// streams that are open. Front ends that change Overrides.Features
	// call Host.PolicyChanged to apply a change at once.
	watchEvery = 250 * time.Millisecond
	// withdrawGrace is how long a stream whose feature was withdrawn has
	// to end by itself, telling its client why, before it is reset.
	withdrawGrace = 500 * time.Millisecond
)

// featureWatch is one registration: check says whether what is watched
// is allowed under a policy.
type featureWatch struct {
	check   func(p policy) bool
	fn      func(allowed bool)
	allowed bool // last state reported; guarded by sessionWatches.eval
	stopped atomic.Bool
}

// sessionWatches holds a session's watches. It lives on the session's
// peer.Conn (watchesKey).
type sessionWatches struct {
	s    *Session
	eval sync.Mutex // serialises evaluations, and so the callbacks

	mu      sync.Mutex
	m       map[*featureWatch]struct{}
	polling bool
}

type watchesKey struct{}

func (s *Session) watches() *sessionWatches {
	v, _ := s.conn.LoadOrStoreValue(watchesKey{}, &sessionWatches{s: s, m: map[*featureWatch]struct{}{}})
	return v.(*sessionWatches)
}

// allows is Allowed's answer for s under the policy p.
func (s *Session) allows(p policy, f perm.Feature) bool {
	if s.grant != "" && !s.grantAllowsFeature(f) {
		return false
	}
	return !p.off[f] && !s.limited(f)
}

// allowsService is whether s may still use service, of feature f, under
// p: what authorize checked when the stream opened, minus the audit.
// Grants allow single services of a feature (shell:run-only is exec but
// not shell), so a granted session is checked by service.
func (s *Session) allowsService(p policy, service string, f perm.Feature) bool {
	if p.off[f] || s.limited(f) {
		return false
	}
	if s.grant == "" {
		return true
	}
	// activeGrant also checks the session's credential against the grant's
	// current one, so a reissued grant's rules never apply to this session.
	e, err := s.activeGrant()
	return err == nil && e.rules.AllowsService(service)
}

// WatchFeature calls fn(false) as soon as the host's owner no longer
// allows feature f for the session behind c (Allowed(c, f) turns false)
// and fn(true) when it is allowed again. If f is not allowed when
// WatchFeature is called, fn(false) runs before it returns. Changes made
// in the config file are noticed within a fraction of a second, and a
// front end's own switches at once (Host.PolicyChanged).
//
// Calls to fn for one session never overlap. fn runs on the host's
// watcher, so it must be quick and must not call WatchFeature or
// PolicyChanged itself. stop ends the watch; a call to fn that was already
// under way may still finish after stop returns. For a connection that is
// not a session of a Host, fn is never called.
func WatchFeature(c *peer.Conn, f perm.Feature, fn func(allowed bool)) (stop func()) {
	s := SessionOf(c)
	if s == nil || f == perm.Always {
		return func() {}
	}
	return s.watch(func(p policy) bool { return s.allows(p, f) }, fn)
}

// watch is WatchFeature for any check.
func (s *Session) watch(check func(policy) bool, fn func(allowed bool)) (stop func()) {
	ws := s.watches()
	w := &featureWatch{check: check, fn: fn}
	ws.eval.Lock()
	w.allowed = check(s.host.policy())
	if !w.allowed {
		fn(false)
	}
	ws.mu.Lock()
	ws.m[w] = struct{}{}
	if !ws.polling {
		ws.polling = true
		go ws.poll()
	}
	ws.mu.Unlock()
	ws.eval.Unlock()
	return func() {
		w.stopped.Store(true)
		ws.mu.Lock()
		delete(ws.m, w)
		ws.mu.Unlock()
	}
}

// poll re-evaluates the session's watches until it has none left or the
// session ends.
func (ws *sessionWatches) poll() {
	t := time.NewTicker(watchEvery)
	defer t.Stop()
	for {
		select {
		case <-ws.s.conn.Done():
			ws.mu.Lock()
			ws.polling = false
			ws.mu.Unlock()
			return
		case <-t.C:
		}
		ws.mu.Lock()
		if len(ws.m) == 0 {
			ws.polling = false
			ws.mu.Unlock()
			return
		}
		ws.mu.Unlock()
		ws.evaluate()
	}
}

// evaluate reports every watch whose feature changed since it was last
// reported.
func (ws *sessionWatches) evaluate() {
	ws.eval.Lock()
	defer ws.eval.Unlock()
	ws.mu.Lock()
	list := make([]*featureWatch, 0, len(ws.m))
	for w := range ws.m {
		list = append(list, w)
	}
	ws.mu.Unlock()
	if len(list) == 0 {
		return
	}
	p := ws.s.host.policy()
	for _, w := range list {
		a := w.check(p)
		if a != w.allowed && !w.stopped.Load() {
			w.allowed = a
			w.fn(a)
		}
	}
}

// PolicyChanged applies a change of what the host allows to the sessions
// already connected at once, rather than at the next periodic check:
// streams of a feature that is now off end, and a desktop stream stops
// taking input when input is off. It returns once that has been set in
// motion (streams that do not end by themselves are reset shortly after).
// A front end calls it after changing what its Overrides.Features returns.
func (h *Host) PolicyChanged() {
	for _, s := range h.Sessions() {
		if s.conn == nil {
			continue
		}
		if v, ok := s.conn.Value(watchesKey{}); ok {
			v.(*sessionWatches).evaluate()
		}
	}
}

// guardStream wraps the handler of service name (see peer.Registry.Wrapped)
// so that its stream ends when the service is withdrawn from the session
// (its feature switched off, or no longer in the session's grant): the
// handler's context is cancelled with a perm.WithdrawnError
// as the cause, and the stream is reset withdrawGrace later unless the
// handler has returned by then.
func guardStream(name string, next peer.Handler) peer.Handler {
	f, ok := perm.Of(name)
	if !ok || f == perm.Always {
		return next
	}
	return func(ctx context.Context, c *peer.Conn, st peer.Stream, req json.RawMessage) error {
		s := SessionOf(c)
		if s == nil {
			return next(ctx, c, st, req)
		}
		ctx, cancel := context.WithCancelCause(ctx)
		defer cancel(nil)
		done := make(chan struct{})
		defer close(done)
		var once sync.Once
		stop := s.watch(func(p policy) bool { return s.allowsService(p, name, f) }, func(allowed bool) {
			if allowed {
				return
			}
			once.Do(func() {
				cancel(&perm.WithdrawnError{Feature: f})
				time.AfterFunc(withdrawGrace, func() {
					select {
					case <-done:
					default:
						st.Reset()
					}
				})
			})
		})
		defer stop()
		return next(ctx, c, st, req)
	}
}
