package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// AttachRequest selects the interactive session to attach to.
type AttachRequest struct {
	// Name is the session; "" means "main". It is created if missing.
	Name string
	// New always starts a fresh session, called Name or, if that is taken,
	// Name-2, Name-3, ...
	New bool
	// Term is the client's TERM; Rows and Cols its window size.
	Term       string
	Rows, Cols int
}

// AttachIO is the local side of an attached terminal.
type AttachIO struct {
	// Stdin carries keystrokes. Its EOF detaches (the session keeps
	// running). Attach does not wait for a Read still blocked when it
	// returns; whatever that Read yields afterwards is discarded.
	Stdin io.Reader
	// Stdout receives the terminal output, starting with the replay of
	// recent output when re-attaching.
	Stdout io.Writer
	// Resize delivers local window size changes.
	Resize <-chan WinSize
	// Escape is the escape character, recognised only at the start of a
	// line as in ssh: Escape followed by '.' detaches, by '?' prints help,
	// and doubled sends one literal Escape. 0 disables escapes.
	Escape byte
	// OnAttach, if set, runs once the host has attached the session,
	// before any output is written.
	OnAttach func(*AttachResult)
}

// DetachReason says why an attach ended with the session still running.
type DetachReason string

const (
	DetachEscape      DetachReason = "escape"       // the user typed the escape sequence
	DetachTakeover    DetachReason = "takeover"     // another client attached
	DetachInputClosed DetachReason = "input-closed" // Stdin reached EOF
	DetachCanceled    DetachReason = "canceled"     // ctx was cancelled
)

// AttachResult describes the attached session and how the attach ended.
type AttachResult struct {
	Session string `json:"session"`
	PID     int    `json:"pid"`
	Created bool   `json:"created"`
	// KeepAlive is how long the host keeps the session once detached.
	KeepAlive time.Duration `json:"keepalive_ns"`

	// Exited is set when the shell ended; the session is gone.
	Exited     bool   `json:"exited,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Signal     int    `json:"signal,omitempty"`
	SignalName string `json:"signal_name,omitempty"`
	// Detached is set when the client left and the session keeps running.
	Detached DetachReason `json:"detached,omitempty"`
}

// Attach connects to an interactive session on the host and relays the
// terminal until the shell exits, the user detaches, Stdin ends, another
// client takes over or ctx is cancelled. Except when the shell exited, the
// session keeps running on the host for the keep-alive period.
//
// If the connection fails after the session was attached, Attach returns
// the partial result (so callers can say how to resume) with the error.
func Attach(ctx context.Context, conn *peer.Conn, req AttachRequest, aio AttachIO) (*AttachResult, error) {
	if req.Name != "" {
		if err := validName(req.Name); err != nil {
			return nil, err
		}
	}
	s, err := conn.Open(ctx, svcShell, shellReq{
		Op: "attach", Name: req.Name, New: req.New, Term: req.Term, Rows: req.Rows, Cols: req.Cols,
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()

	buf := make([]byte, maxFrame)
	typ, p, err := readFrame(s, buf)
	if err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("shell: attach: %w", err)
	}
	if typ == frameExit {
		var m exitMsg
		json.Unmarshal(p, &m)
		return nil, &StartError{Msg: printable(m.Error)}
	}
	var hello attachedMsg
	if typ != frameAttached || json.Unmarshal(p, &hello) != nil || validName(hello.Name) != nil {
		return nil, errors.New("shell: attach: protocol error")
	}
	res := &AttachResult{
		Session: hello.Name, PID: hello.PID, Created: hello.Created,
		KeepAlive: time.Duration(hello.KeepAliveMS) * time.Millisecond,
	}
	if aio.OnAttach != nil {
		aio.OnAttach(res)
	}

	out := &lockedWriter{w: orDiscard(aio.Stdout)}
	fw := &frameWriter{w: s}
	done := make(chan struct{})
	defer close(done)
	// The input side records why it ended the attach, then closes the
	// stream, which unblocks the read loop below.
	var (
		reasonMu sync.Mutex
		reason   DetachReason
	)
	leave := func(r DetachReason) {
		reasonMu.Lock()
		if reason == "" {
			reason = r
		}
		reasonMu.Unlock()
		s.Close()
	}
	if aio.Stdin != nil {
		go relayKeys(aio.Stdin, fw, out, aio.Escape, leave, done)
	}
	if aio.Resize != nil {
		go sendResizes(fw, aio.Resize, done)
	}

	for {
		typ, p, err := readFrame(s, buf)
		if err != nil {
			reasonMu.Lock()
			r := reason
			reasonMu.Unlock()
			switch {
			case r != "":
				res.Detached = r
				return res, nil
			case ctx.Err() != nil:
				res.Detached = DetachCanceled
				return res, nil
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return res, fmt.Errorf("shell: connection lost: %w", err)
		}
		switch typ {
		case frameStdout:
			if _, err := out.Write(p); err != nil {
				return res, fmt.Errorf("shell: writing output: %w", err)
			}
		case frameExit:
			var m exitMsg
			if err := json.Unmarshal(p, &m); err != nil {
				return res, errors.New("shell: attach: bad exit message")
			}
			res.Exited, res.ExitCode, res.Signal, res.SignalName = true, m.Code, m.Signal, printable(m.SignalName)
			return res, nil
		case frameDetached:
			res.Detached = DetachTakeover
			return res, nil
		}
	}
}

// relayKeys forwards keystrokes, interpreting the escape sequences.
func relayKeys(in io.Reader, fw *frameWriter, out io.Writer, esc byte, leave func(DetachReason), done <-chan struct{}) {
	e := &escaper{esc: esc, lineStart: true}
	buf := make([]byte, ioChunk)
	for {
		n, err := in.Read(buf)
		select {
		case <-done:
			return
		default:
		}
		if n > 0 {
			fwd, act := e.feed(buf[:n])
			if len(fwd) > 0 {
				if fw.frame(frameInput, fwd) != nil {
					return
				}
			}
			switch act {
			case escDetach:
				leave(DetachEscape)
				return
			case escHelp:
				c := string(esc)
				fmt.Fprintf(out, "\r\nSupported escape sequences:\r\n  %s.  detach (the session keeps running)\r\n  %s?  this help\r\n  %s%s  send %s\r\n(escapes are recognised after a newline)\r\n", c, c, c, c, c)
			}
		}
		if err != nil {
			leave(DetachInputClosed)
			return
		}
	}
}

type escAction int

const (
	escNone escAction = iota
	escDetach
	escHelp
)

// escaper implements ssh-style escapes: the escape character is special only
// as the first character of a line.
type escaper struct {
	esc       byte // 0 disables
	lineStart bool
	pending   bool // saw esc at line start, waiting for the next byte
}

// feed returns the bytes to forward from in and the escape action found.
// On escDetach the rest of in is dropped.
func (e *escaper) feed(in []byte) ([]byte, escAction) {
	if e.esc == 0 {
		return in, escNone
	}
	out := make([]byte, 0, len(in)+1)
	act := escNone
	for _, b := range in {
		if e.pending {
			e.pending = false
			switch b {
			case '.':
				return out, escDetach
			case '?':
				act = escHelp
				continue
			case e.esc:
				out = append(out, b)
			default:
				out = append(out, e.esc, b)
			}
		} else if e.lineStart && b == e.esc {
			e.pending = true
			continue
		} else {
			out = append(out, b)
		}
		e.lineStart = b == '\r' || b == '\n'
	}
	return out, act
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Sessions lists the interactive sessions on the host.
func Sessions(ctx context.Context, conn *peer.Conn) ([]SessionInfo, error) {
	rep, err := shellCall(ctx, conn, shellReq{Op: "list"})
	if err != nil {
		return nil, err
	}
	for _, si := range rep.Sessions {
		if validName(si.Name) != nil {
			return nil, errBadReply
		}
	}
	return rep.Sessions, nil
}

// KillSession ends an interactive session (its shell is hung up).
func KillSession(ctx context.Context, conn *peer.Conn, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	_, err := shellCall(ctx, conn, shellReq{Op: "kill", Name: name})
	return err
}

func shellCall(ctx context.Context, conn *peer.Conn, req shellReq) (*shellReply, error) {
	s, err := conn.Open(ctx, svcShell, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep shellReply
	if err := wire.ReadJSON(s, &rep); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("shell: %w", err)
	}
	if rep.Error != "" {
		return nil, errors.New("remote: " + printable(rep.Error))
	}
	return &rep, nil
}
