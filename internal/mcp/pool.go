package mcp

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
)

// DialFunc establishes an authenticated session with target (a 9-digit ID, a
// saved alias or host:port). It must not prompt: the MCP server's stdin and
// stdout belong to the protocol.
type DialFunc func(ctx context.Context, target string) (*peer.Conn, error)

var errPoolClosed = errors.New("mcp: server is shutting down")

// pool keeps one session per target. Sessions are dialled lazily, shared by
// concurrent callers (one dial no matter how many tool calls arrive at once),
// replaced when they have died, and closed after sitting unused for idle.
type pool struct {
	dial        DialFunc
	idle        time.Duration
	dialTimeout time.Duration
	log         *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	m      map[string]*entry
	closed bool
}

type entry struct {
	ready  chan struct{} // closed when the dial finished
	conn   *peer.Conn
	err    error
	cancel context.CancelFunc // releases the dial context
	users  int                // calls (and forwards) currently relying on conn
	last   time.Time
}

func newPool(dial DialFunc, idle, dialTimeout time.Duration, log *slog.Logger) *pool {
	ctx, cancel := context.WithCancel(context.Background())
	p := &pool{dial: dial, idle: idle, dialTimeout: dialTimeout, log: log, ctx: ctx, cancel: cancel, m: map[string]*entry{}}
	if idle > 0 {
		p.wg.Add(1)
		go p.reap()
	}
	return p
}

func dead(c *peer.Conn) bool {
	select {
	case <-c.Done():
		return true
	default:
		return false
	}
}

// acquire returns the live session for target, dialling if there is none,
// and a release function the caller must call when it is done with the
// session (an acquired session is never closed for idleness).
func (p *pool) acquire(ctx context.Context, target string) (*peer.Conn, func(), error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, nil, errPoolClosed
	}
	e := p.m[target]
	if e != nil {
		select {
		case <-e.ready:
			if e.err != nil || dead(e.conn) {
				p.evictLocked(target, e)
				e = nil
			}
		default: // still dialling: join that dial
		}
	}
	if e == nil {
		e = &entry{ready: make(chan struct{})}
		p.m[target] = e
		p.wg.Add(1)
		go p.dialEntry(target, e)
	}
	e.users++
	p.mu.Unlock()

	release := func() {
		p.mu.Lock()
		e.users--
		e.last = time.Now()
		p.mu.Unlock()
	}
	select {
	case <-e.ready:
	case <-ctx.Done():
		release()
		return nil, nil, ctx.Err()
	}
	if e.err != nil {
		release()
		return nil, nil, e.err
	}
	return e.conn, release, nil
}

// dialEntry runs the dial for e. It uses the pool's context, not the first
// caller's: other callers share the result and one of them giving up must
// not fail the rest.
func (p *pool) dialEntry(target string, e *entry) {
	defer p.wg.Done()
	ctx, cancel := context.WithCancel(p.ctx)
	timer := time.AfterFunc(p.dialTimeout, cancel)
	p.log.Debug("dialling", "target", target)
	conn, err := p.dial(ctx, target)
	timer.Stop()
	expired := ctx.Err() != nil

	p.mu.Lock()
	defer p.mu.Unlock()
	defer close(e.ready)
	switch {
	case err != nil:
		cancel()
		e.err = err
		if expired {
			e.err = errors.New("timed out dialling " + target + ": " + err.Error())
		}
		if p.m[target] == e {
			delete(p.m, target)
		}
	case p.closed:
		cancel()
		conn.Close()
		e.err = errPoolClosed
	default:
		e.conn, e.cancel, e.last = conn, cancel, time.Now()
	}
}

// evictLocked forgets e as the session of target and closes it.
func (p *pool) evictLocked(target string, e *entry) {
	if e == nil {
		return
	}
	if p.m[target] == e {
		delete(p.m, target)
	}
	if e.cancel != nil {
		e.cancel()
	}
	if e.conn != nil {
		e.conn.Close()
	}
}

// invalidate drops conn after it was found broken, so the next acquire dials
// afresh.
func (p *pool) invalidate(target string, conn *peer.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.m[target]; e != nil && e.conn == conn {
		p.evictLocked(target, e)
	}
}

// reap closes sessions nobody has used for the idle period.
func (p *pool) reap() {
	defer p.wg.Done()
	tick := max(p.idle/4, 10*time.Millisecond)
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
		}
		p.mu.Lock()
		for target, e := range p.m {
			select {
			case <-e.ready:
			default:
				continue
			}
			if e.err == nil && (dead(e.conn) || (e.users == 0 && time.Since(e.last) >= p.idle)) {
				p.log.Debug("closing idle session", "target", target)
				p.evictLocked(target, e)
			}
		}
		p.mu.Unlock()
	}
}

// close ends every session and stops the pool.
func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	for target, e := range p.m {
		select {
		case <-e.ready:
			p.evictLocked(target, e)
		default:
		}
	}
	p.mu.Unlock()
	p.cancel()
	p.wg.Wait()
}

// sessions reports the live sessions: how each one's packets travel, keyed
// both by the target as callers named it and by the dialer's canonical
// target, so a session opened through an alias is found under the ID too.
func (p *pool) sessions() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for target, e := range p.m {
		select {
		case <-e.ready:
		default:
			continue
		}
		if e.err != nil || dead(e.conn) {
			continue
		}
		path := connPath(e.conn)
		out[target] = path
		if info := dial.InfoOf(e.conn); info != nil && info.Target != "" {
			out[info.Target] = path
		}
	}
	return out
}
