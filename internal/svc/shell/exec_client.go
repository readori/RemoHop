package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/readori/RemoHop/internal/peer"
)

// WinSize is a terminal size in character cells.
type WinSize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}

// ExecRequest describes one non-interactive command run on the host through
// the user's shell (`$SHELL -c`, cmd.exe /C on Windows).
type ExecRequest struct {
	Command string
	// Cwd is the working directory on the host; empty, "~" and relative
	// paths are relative to the host user's home.
	Cwd string
	// Env holds KEY=VALUE overrides on top of the host's environment.
	Env []string
	// Stdin is streamed to the command; its EOF closes the command's
	// stdin. nil gives the command an empty stdin. Exec does not wait for a
	// Read that is still blocked when the command exits.
	Stdin io.Reader
	// Stdout and Stderr receive the command's output; nil discards it.
	// With TTY both streams arrive merged on Stdout. Writes are synchronous,
	// so a slow writer slows the remote command down instead of buffering.
	Stdout io.Writer
	Stderr io.Writer
	// Timeout, if positive, kills the remote process group after that long
	// and reports ExecResult.TimedOut.
	Timeout time.Duration

	// TTY runs the command on a pseudo-terminal of Rows x Cols with TERM
	// set to Term. Resize, if set, delivers window size changes.
	TTY        bool
	Term       string
	Rows, Cols int
	Resize     <-chan WinSize
}

// ExecResult is how a command ended.
type ExecResult struct {
	// ExitCode is the command's exit status, or 128+n if it died from
	// signal n (including the kill on timeout).
	ExitCode   int           `json:"exit_code"`
	Signal     int           `json:"signal,omitempty"`
	SignalName string        `json:"signal_name,omitempty"`
	TimedOut   bool          `json:"timed_out,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
}

// StartError reports that the host could not start the command at all
// (missing working directory, bad request, ...).
type StartError struct{ Msg string }

func (e *StartError) Error() string { return "remote: " + e.Msg }

// Exec runs req.Command on the host and streams its I/O. It returns when
// the command has exited and all its output has been written. Cancelling
// ctx (or losing the session) kills the remote process group; Exec then
// returns ctx.Err() or the transport error.
func Exec(ctx context.Context, conn *peer.Conn, req ExecRequest) (*ExecResult, error) {
	if req.Command == "" {
		return nil, errors.New("shell: empty command")
	}
	if err := validEnv(req.Env); err != nil {
		return nil, err
	}
	wr := execReq{
		Command: req.Command, Cwd: req.Cwd, Env: req.Env,
		TTY: req.TTY, Term: req.Term, Rows: req.Rows, Cols: req.Cols,
	}
	if req.Timeout > 0 {
		wr.TimeoutMS = max(1, req.Timeout.Milliseconds())
	}
	start := time.Now()
	s, err := conn.Open(ctx, svcExec, wr)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()

	fw := &frameWriter{w: s}
	done := make(chan struct{})
	defer close(done)
	go sendInput(fw, req.Stdin, done)
	if req.Resize != nil {
		go sendResizes(fw, req.Resize, done)
	}

	stdout, stderr := orDiscard(req.Stdout), orDiscard(req.Stderr)
	buf := make([]byte, maxFrame)
	for {
		typ, p, err := readFrame(s, buf)
		if err != nil {
			if ce := ctx.Err(); ce != nil {
				return nil, ce
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("shell: exec: %w", err)
		}
		var w io.Writer
		switch typ {
		case frameStdout:
			w = stdout
		case frameStderr:
			w = stderr
		case frameExit:
			var m exitMsg
			if err := json.Unmarshal(p, &m); err != nil {
				return nil, fmt.Errorf("shell: exec: bad exit message: %w", err)
			}
			if m.Error != "" {
				return nil, &StartError{Msg: printable(m.Error)}
			}
			return &ExecResult{
				ExitCode: m.Code, Signal: m.Signal, SignalName: printable(m.SignalName),
				TimedOut: m.TimedOut, Duration: time.Since(start),
			}, nil
		default:
			continue
		}
		if _, err := w.Write(p); err != nil {
			// Stop the remote command rather than let it run unobserved.
			s.Reset()
			return nil, fmt.Errorf("shell: exec: writing output: %w", err)
		}
	}
}

// sendInput streams r as input frames followed by an EOF frame.
func sendInput(fw *frameWriter, r io.Reader, done <-chan struct{}) {
	if r != nil {
		buf := make([]byte, ioChunk)
		for {
			n, err := r.Read(buf)
			select {
			case <-done:
				return
			default:
			}
			if n > 0 {
				if fw.frame(frameInput, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
	}
	fw.frame(frameInputEOF, nil)
}

func sendResizes(fw *frameWriter, ch <-chan WinSize, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case ws, ok := <-ch:
			if !ok {
				return
			}
			if fw.frame(frameResize, encodeSize(ws.Rows, ws.Cols)) != nil {
				return
			}
		}
	}
}

func orDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// DefaultCaptureLimit is the per-stream cap Capture uses when given 0.
const DefaultCaptureLimit = 1 << 20

// CaptureResult is an ExecResult plus the captured output.
type CaptureResult struct {
	ExecResult
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	// StdoutBytes and StderrBytes count everything the command wrote,
	// including what was dropped.
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
}

// Capture runs a command and collects its output, keeping at most limit
// bytes of each stream (the beginning; the rest is counted and dropped).
// limit <= 0 means DefaultCaptureLimit. req.Stdout and req.Stderr are
// ignored. This is the "run it and tell me what happened" call AI agents
// use.
func Capture(ctx context.Context, conn *peer.Conn, req ExecRequest, limit int) (*CaptureResult, error) {
	if limit <= 0 {
		limit = DefaultCaptureLimit
	}
	out, errb := &capped{limit: limit}, &capped{limit: limit}
	req.Stdout, req.Stderr = out, errb
	res, err := Exec(ctx, conn, req)
	if err != nil {
		return nil, err
	}
	return &CaptureResult{
		ExecResult: *res,
		Stdout:     out.buf, Stderr: errb.buf,
		StdoutTruncated: out.total > int64(len(out.buf)),
		StderrTruncated: errb.total > int64(len(errb.buf)),
		StdoutBytes:     out.total, StderrBytes: errb.total,
	}, nil
}

// capped keeps the first limit bytes written to it and counts the rest.
type capped struct {
	buf   []byte
	limit int
	total int64
}

func (c *capped) Write(p []byte) (int, error) {
	c.total += int64(len(p))
	if room := c.limit - len(c.buf); room > 0 {
		c.buf = append(c.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
