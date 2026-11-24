package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// KeepAlive is how long a detached interactive session keeps running on the
// host before it is killed. 0 kills a session as soon as its client goes
// away. The REMOHOP_SHELL_KEEPALIVE environment variable (a Go duration such as
// "45m", or "0") overrides the default when the package initialises; hosts
// may also set it directly before serving. It applies when a session
// detaches.
var KeepAlive = 30 * time.Minute

const (
	// replayBytes is the ring of recent terminal output replayed to a client
	// that (re)attaches, enough for a full screen of colourful output.
	replayBytes = 64 << 10
	// maxSessions bounds the interactive sessions one host keeps.
	maxSessions = 32
	// defaultSession is the session `remohop <ID> shell` attaches to.
	defaultSession = "main"
	// killGrace is how long a hung-up session gets before SIGKILL.
	killGrace = 5 * time.Second
)

func init() {
	if v := os.Getenv("REMOHOP_SHELL_KEEPALIVE"); v != "" {
		if d, err := parseKeepAlive(v); err == nil {
			KeepAlive = d
		} else {
			slog.Warn("ignoring REMOHOP_SHELL_KEEPALIVE", "value", v, "err", err)
		}
	}
	peer.Handle(svcShell, handleShell)
}

func parseKeepAlive(v string) (time.Duration, error) {
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, errors.New("negative duration")
	}
	return d, nil
}

// shellReq is the request of a "shell" stream.
type shellReq struct {
	Op   string `json:"op"` // "attach", "list" or "kill"
	Name string `json:"name,omitempty"`
	New  bool   `json:"new,omitempty"`
	Term string `json:"term,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

// shellReply answers "list" and "kill".
type shellReply struct {
	Error    string        `json:"error,omitempty"`
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

type attachedMsg struct {
	Name        string `json:"name"`
	PID         int    `json:"pid"`
	Created     bool   `json:"created"`
	KeepAliveMS int64  `json:"keepalive_ms"`
}

type detachedMsg struct {
	Reason string `json:"reason"`
}

// SessionInfo describes one interactive session on the host. Durations are
// computed on the host so clock skew does not distort them.
type SessionInfo struct {
	Name        string    `json:"name"`
	PID         int       `json:"pid"`
	Created     time.Time `json:"created"`
	AgeSeconds  float64   `json:"age_seconds"`
	Attached    bool      `json:"attached"`
	IdleSeconds float64   `json:"idle_seconds"`
	Rows        int       `json:"rows"`
	Cols        int       `json:"cols"`
	// ExpiresInSeconds is set for a detached session: the time left before
	// the keep-alive reaps it.
	ExpiresInSeconds float64 `json:"expires_in_seconds,omitempty"`
}

func handleShell(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req shellReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, shellReply{Error: "bad request: " + err.Error()})
	}
	// A session under a grant sees, attaches to and ends only the
	// terminals it started itself.
	g := grant.Of(c)
	owner := ""
	if g != nil {
		owner = g.Name()
	}
	switch req.Op {
	case "list":
		return wire.WriteJSON(s, shellReply{Sessions: sessions.list(owner)})
	case "kill":
		if g != nil {
			g.Log("terminal-kill", "session", req.Name)
		}
		if err := sessions.kill(req.Name, owner); err != nil {
			return wire.WriteJSON(s, shellReply{Error: err.Error()})
		}
		return wire.WriteJSON(s, shellReply{})
	case "attach":
		return handleAttach(ctx, s, &req, g)
	}
	return wire.WriteJSON(s, shellReply{Error: fmt.Sprintf("unknown op %q", req.Op)})
}

// handleAttach serves an interactive client. Protocol after the stream
// header:
//
//	host -> client: frameAttached, frameStdout (replay, then live output)*,
//	                then frameExit (the shell ended) or frameDetached
//	                (another client took the session over)
//	client -> host: frameInput*, frameResize*
//
// A client detaches simply by closing or resetting the stream; the session
// keeps running for KeepAlive. A second client attaching to a session that
// is already attached takes it over: the first client receives
// frameDetached and the session follows the newcomer's terminal size. This
// matches `screen -d -r` and mosh, and avoids two terminals of different
// sizes fighting over one PTY.
func handleAttach(ctx context.Context, s peer.Stream, req *shellReq, g grant.Guard) error {
	fw := &frameWriter{w: s}
	name := req.Name
	if name == "" {
		name = defaultSession
	}
	if err := validName(name); err != nil {
		return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
	}
	term := req.Term
	if term == "" {
		term = "xterm-256color"
	}
	rows, cols := req.Rows, req.Cols
	if rows <= 0 || cols <= 0 || rows > maxDim || cols > maxDim {
		rows, cols = 24, 80
	}
	owner := ""
	var rec grant.Recorder
	if g != nil {
		owner = g.Name()
		if err := g.Terminal(ctx, name); err != nil {
			return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
		}
	}
	sess, created, err := sessions.open(name, req.New, validTerm(term), rows, cols, owner)
	if err != nil {
		return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
	}
	if g != nil {
		if rec = g.Record("terminal", sess.name, cols, rows); rec != nil {
			defer rec.Close("detached or ended")
		}
	}
	return sess.serve(ctx, s, fw, created, rows, cols, rec)
}

// sessionManager holds every interactive session of this host process.
// Sessions belong to the host, not to a connection: any authenticated
// client may attach to any of them.
type sessionManager struct {
	mu sync.Mutex
	m  map[string]*session
}

var sessions = &sessionManager{m: map[string]*session{}}

// open returns the session called name, starting it if needed. With fresh,
// a new session is always started, named name or, if taken, name-2,
// name-3, ...
func (sm *sessionManager) open(name string, fresh bool, term string, rows, cols int, owner string) (*session, bool, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if fresh {
		base := name
		for i := 2; sm.m[name] != nil; i++ {
			name = base + "-" + strconv.Itoa(i)
			if len(name) > 64 {
				return nil, false, errors.New("no free session name")
			}
		}
	} else if s := sm.m[name]; s != nil {
		if owner != "" && s.owner != owner {
			return nil, false, fmt.Errorf("session %q was not started under grant %s; attaching to it is not allowed by grant %s (start your own with a new name)", name, owner, owner)
		}
		return s, false, nil
	}
	if len(sm.m) >= maxSessions {
		return nil, false, fmt.Errorf("too many sessions (%d); end some with `shell kill NAME`", maxSessions)
	}
	s, err := startSession(name, term, rows, cols)
	if err != nil {
		return nil, false, err
	}
	s.owner = owner
	sm.m[name] = s
	return s, true, nil
}

func (sm *sessionManager) remove(s *session) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.m[s.name] == s {
		delete(sm.m, s.name)
	}
}

func (sm *sessionManager) get(name string) *session {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.m[name]
}

func (sm *sessionManager) list(owner string) []SessionInfo {
	sm.mu.Lock()
	all := make([]*session, 0, len(sm.m))
	for _, s := range sm.m {
		if owner == "" || s.owner == owner {
			all = append(all, s)
		}
	}
	sm.mu.Unlock()
	out := make([]SessionInfo, 0, len(all))
	now := time.Now()
	for _, s := range all {
		out = append(out, s.info(now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (sm *sessionManager) kill(name, owner string) error {
	s := sm.get(name)
	if s == nil || (owner != "" && s.owner != owner) {
		return fmt.Errorf("no session %q", name)
	}
	s.terminate()
	return nil
}

// session is one persistent shell on a PTY.
type session struct {
	name       string
	proc       *child
	term       *termDev
	created    time.Time
	readerDone chan struct{}

	mu           sync.Mutex
	ring         ring
	rows, cols   int
	att          *attachment // nil while detached
	gen          int         // bumped on every attach; invalidates reap timers
	lastActivity time.Time
	detachedAt   time.Time
	keepAlive    time.Duration // in force since detachedAt
	reap         *time.Timer
	exit         *exitMsg // set once the session is over
	// owner is the grant the session was started under ("" for full
	// access); set before the session is listed, then never changed.
	owner string
}

// attachment is one connected client.
type attachment struct {
	fw   *frameWriter
	s    peer.Stream
	rec  grant.Recorder // the grant's recording of this attachment, or nil
	done chan struct{}  // closed after the final frame has been queued
	once sync.Once
}

// finish sends the client its last frame and releases its handler. A stuck
// client gets a bounded time to take the frame.
func (a *attachment) finish(typ byte, msg any) {
	a.once.Do(func() {
		a.s.SetWriteDeadline(time.Now().Add(2 * time.Second))
		a.fw.final(typ, msg)
		close(a.done)
	})
}

func startSession(name, term string, rows, cols int) (*session, error) {
	cmd := loginShellCmd()
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	cmd.Dir = homeDir()
	cmd.Env = buildEnv(term, cmd.Dir, []string{"REMOHOP_SESSION=" + name})
	proc, t, err := startTerm(cmd, rows, cols)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	s := &session{
		name: name, proc: proc, term: t, created: now, readerDone: make(chan struct{}),
		ring: ring{size: replayBytes}, rows: rows, cols: cols, lastActivity: now,
	}
	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

// readLoop drains the terminal for the whole life of the session, attached
// or not, so the shell never blocks on output while nobody is watching.
func (s *session) readLoop() {
	defer close(s.readerDone)
	buf := make([]byte, ioChunk)
	for {
		n, err := s.term.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.ring.write(buf[:n])
			s.lastActivity = time.Now()
			att := s.att
			s.mu.Unlock()
			if att != nil {
				if att.rec != nil {
					att.rec.Output(buf[:n])
				}
				if werr := att.fw.frame(frameStdout, buf[:n]); werr != nil {
					s.detach(att)
					att.s.Reset()
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// waitLoop reaps the shell and ends the session.
func (s *session) waitLoop() {
	s.proc.awaitExit()
	s.term.processExited()
	// Drain what the shell wrote last, but never wait on the terminal
	// indefinitely: background jobs may hold the slave side open forever.
	// Closing the master hangs the terminal up; on unix such jobs keep
	// running (as after an ordinary logout) but lose their terminal, while
	// on Windows reaping the shell closes its job object, which ends them.
	// Either way the session ends now; the reader finishes on its own once
	// Close interrupts it.
	select {
	case <-s.readerDone:
	case <-time.After(termDrain):
	}
	s.term.Close()
	select {
	case <-s.readerDone:
	case <-time.After(termDrain):
	}

	code, sig, name := s.proc.reap()
	msg := &exitMsg{Code: code, Signal: sig, SignalName: name}
	sessions.remove(s)
	s.mu.Lock()
	s.exit = msg
	att := s.att
	s.att = nil
	if s.reap != nil {
		s.reap.Stop()
	}
	s.mu.Unlock()
	if att != nil {
		att.finish(frameExit, msg)
	}
}

// serve attaches a client and relays its input until it goes away, the
// shell exits or another client takes over.
func (s *session) serve(ctx context.Context, st peer.Stream, fw *frameWriter, created bool, rows, cols int, rec grant.Recorder) error {
	att := &attachment{fw: fw, s: st, rec: rec, done: make(chan struct{})}

	s.mu.Lock()
	if s.exit != nil {
		msg := *s.exit
		s.mu.Unlock()
		return fw.json(frameExit, msg)
	}
	old := s.att
	s.att = att
	s.gen++
	if s.reap != nil {
		s.reap.Stop()
		s.reap = nil
	}
	s.detachedAt = time.Time{}
	s.rows, s.cols = rows, cols
	replay := s.ring.snapshot()
	// Hold the client's writer until the replay is out so the read loop's
	// live output (which now targets att) queues up behind it in order.
	fw.mu.Lock()
	s.mu.Unlock()

	if old != nil {
		go old.finish(frameDetached, detachedMsg{Reason: "session attached by another client"})
	}
	s.term.resize(rows, cols)
	hello, _ := json.Marshal(attachedMsg{
		Name: s.name, PID: s.proc.pid(), Created: created, KeepAliveMS: KeepAlive.Milliseconds(),
	})
	err := wire.WriteFrame(st, frameAttached, hello)
	if err == nil && len(replay) > 0 {
		err = wire.WriteFrame(st, frameStdout, replay)
	}
	fw.mu.Unlock()
	if err != nil {
		s.detach(att)
		return err
	}
	if !created {
		s.term.redraw()
	}

	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		s.relayInput(st, att)
	}()
	select {
	case <-att.done:
		return nil
	case <-inputDone:
	case <-streamGone(st):
	case <-ctx.Done():
		if w, ok := perm.Withdrawn(context.Cause(ctx)); ok {
			s.withdraw(att, w)
			return nil
		}
	}
	s.detach(att)
	return nil
}

// withdrawWait bounds how long withdraw waits for the shell to exit before
// it tells the client the terminal ended anyway (the host resets the
// stream soon after a withdrawal; see internal/hostd).
const withdrawWait = 300 * time.Millisecond

// withdraw ends the session because the host's owner withdrew the shell
// feature from the attached client: unlike a client that goes away, the
// shell is not kept for re-attaching. The client is told why in the
// terminal and then gets the exit.
func (s *session) withdraw(att *attachment, why error) {
	att.fw.frame(frameStdout, []byte("\r\n[remohop: "+why.Error()+"; this terminal was ended]\r\n"))
	s.terminate()
	select {
	case <-att.done:
		return
	case <-time.After(withdrawWait):
	}
	s.mu.Lock()
	current := s.att == att
	if current {
		s.att = nil
	}
	s.mu.Unlock()
	if current {
		att.finish(frameExit, &exitMsg{Code: -1})
	}
}

// relayInput copies keystrokes and resizes from the client to the PTY for as
// long as att is the session's client.
func (s *session) relayInput(st peer.Stream, att *attachment) {
	buf := make([]byte, ioChunk)
	for {
		typ, p, err := readFrame(st, buf)
		if err != nil {
			return
		}
		s.mu.Lock()
		current := s.att == att
		if current {
			s.lastActivity = time.Now()
		}
		s.mu.Unlock()
		if !current {
			return
		}
		switch typ {
		case frameInput:
			if att.rec != nil {
				att.rec.Input(p)
			}
			if _, err := s.term.Write(p); err != nil {
				return
			}
		case frameResize:
			if rows, cols, ok := decodeSize(p); ok {
				s.mu.Lock()
				s.rows, s.cols = rows, cols
				s.mu.Unlock()
				s.term.resize(rows, cols)
				if att.rec != nil {
					att.rec.Resize(cols, rows)
				}
			}
		}
	}
}

// detach marks att gone and starts the keep-alive countdown.
func (s *session) detach(att *attachment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.att != att || s.exit != nil {
		return
	}
	s.att = nil
	s.detachedAt = time.Now()
	s.keepAlive = KeepAlive
	if s.keepAlive <= 0 {
		go s.terminate()
		return
	}
	gen := s.gen
	s.reap = time.AfterFunc(s.keepAlive, func() {
		s.mu.Lock()
		expired := s.att == nil && s.gen == gen && s.exit == nil
		s.mu.Unlock()
		if expired {
			s.terminate()
		}
	})
}

// terminate hangs the session up like a closed terminal would, then kills
// whatever ignores the hangup. waitLoop finishes the cleanup.
func (s *session) terminate() {
	s.proc.hangup()
	s.term.Close()
	time.AfterFunc(killGrace, s.proc.killTree)
}

func (s *session) info(now time.Time) SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := SessionInfo{
		Name: s.name, PID: s.proc.pid(), Created: s.created,
		AgeSeconds: now.Sub(s.created).Seconds(), Attached: s.att != nil,
		IdleSeconds: now.Sub(s.lastActivity).Seconds(), Rows: s.rows, Cols: s.cols,
	}
	if s.att == nil && !s.detachedAt.IsZero() {
		in.ExpiresInSeconds = max(0, s.detachedAt.Add(s.keepAlive).Sub(now).Seconds())
	}
	return in
}

// ring keeps the last size bytes written to it.
type ring struct {
	size    int
	buf     []byte
	wrapped bool
}

func (r *ring) write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.size {
		// Compact only once the buffer has doubled, keeping writes
		// amortised O(1).
		if len(r.buf) >= 2*r.size {
			r.buf = append(r.buf[:0], r.buf[len(r.buf)-r.size:]...)
		}
		r.wrapped = true
	}
}

// snapshot returns a copy of the retained output. Once older output has
// been dropped the cut can fall inside an escape sequence or a UTF-8
// character, so the replay starts at the next line instead.
func (r *ring) snapshot() []byte {
	b := r.buf
	if len(b) > r.size {
		b = b[len(b)-r.size:]
	}
	if r.wrapped {
		for i, c := range b[:min(len(b), 4096)] {
			if c == '\n' {
				b = b[i+1:]
				break
			}
		}
	}
	return append([]byte(nil), b...)
}
