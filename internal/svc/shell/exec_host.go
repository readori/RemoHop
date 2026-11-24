package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
)

// execReq is the request of an "exec" stream.
type execReq struct {
	Command   string   `json:"command"`
	Cwd       string   `json:"cwd,omitempty"`
	Env       []string `json:"env,omitempty"`
	TTY       bool     `json:"tty,omitempty"`
	Term      string   `json:"term,omitempty"`
	Rows      int      `json:"rows,omitempty"`
	Cols      int      `json:"cols,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

// outputGrace is how long the host keeps reading a command's output after
// the command itself exited. Output still flowing after that comes from
// background children holding the pipes; group members are killed and the
// output is closed so the stream ends instead of hanging. Processes that
// left the group (setsid) keep running but lose their output.
var outputGrace = 2 * time.Second

func init() {
	peer.Handle(svcExec, handleExec)
}

// execProc is a started command with the host's ends of its stdio.
type execProc struct {
	proc  *child
	stdin io.WriteCloser
	tty   *termDev // nil without -t
	outs  []execOut
}

type execOut struct {
	r   io.ReadCloser
	typ byte
}

func startExec(req *execReq) (*execProc, error) {
	cmd := commandCmd(req.Command)
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	cmd.Dir = resolveDir(req.Cwd)
	if st, err := os.Stat(cmd.Dir); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, errors.New(cmd.Dir + ": not a directory")
	}
	if req.TTY {
		term := req.Term
		if term == "" {
			term = "xterm-256color"
		}
		cmd.Env = buildEnv(validTerm(term), cmd.Dir, req.Env)
		proc, t, err := startTerm(cmd, req.Rows, req.Cols)
		if err != nil {
			return nil, err
		}
		return &execProc{proc: proc, stdin: t, tty: t, outs: []execOut{{t, frameStdout}}}, nil
	}
	cmd.Env = buildEnv("", cmd.Dir, req.Env)
	proc, stdin, stdout, stderr, err := startPiped(cmd)
	if err != nil {
		return nil, err
	}
	return &execProc{proc: proc, stdin: stdin, outs: []execOut{{stdout, frameStdout}, {stderr, frameStderr}}}, nil
}

// handleExec runs one command. Protocol after the stream header:
//
//	client -> host: frameInput*, frameInputEOF, frameResize (any time)
//	host -> client: frameStdout/frameStderr*, then exactly one frameExit
//
// The client must keep its write side open until it has read frameExit: the
// host treats the end of the client's direction (or a reset) as the client
// going away and kills the command's process group.
func handleExec(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	fw := &frameWriter{w: s}
	var req execReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		return fw.json(frameExit, exitMsg{Code: -1, Error: "bad request: " + err.Error()})
	}
	if req.Command == "" {
		return fw.json(frameExit, exitMsg{Code: -1, Error: "empty command"})
	}
	if err := validEnv(req.Env); err != nil {
		return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
	}
	// A session under a grant asks it first (and may wait for the host's
	// owner to approve); the grant may also record the output.
	var rec grant.Recorder
	if g := grant.Of(c); g != nil {
		if err := g.Command(ctx, "exec", req.Command, req.TTY, req.Env); err != nil {
			return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
		}
		rec = g.Record("exec", req.Command, req.Cols, req.Rows)
	}
	recEnd := "client went away"
	if rec != nil {
		rec.Input([]byte("$ " + req.Command + "\r\n"))
		defer func() { rec.Close(recEnd) }()
	}
	p, err := startExec(&req)
	if err != nil {
		recEnd = "failed to start: " + err.Error()
		return fw.json(frameExit, exitMsg{Code: -1, Error: err.Error()})
	}
	gone := make(chan struct{})
	var goneOnce sync.Once
	abandon := func() {
		goneOnce.Do(func() { close(gone) })
	}

	// Client -> host: stdin, EOF, resize. Any read error means the client
	// went away (see the protocol comment above).
	go func() {
		defer abandon()
		buf := make([]byte, ioChunk)
		stdinOpen := true
		for {
			typ, data, err := readFrame(s, buf)
			if err != nil {
				return
			}
			switch typ {
			case frameInput:
				if rec != nil {
					rec.Input(data)
				}
				if stdinOpen {
					if _, err := p.stdin.Write(data); err != nil {
						// The command closed its stdin; drop further input
						// but keep listening for the client going away.
						stdinOpen = false
					}
				}
			case frameInputEOF:
				if stdinOpen {
					stdinOpen = false
					if p.tty != nil {
						// A terminal has no half-close: deliver EOF the way
						// a user would type it.
						p.stdin.Write(ttyEOF)
					} else {
						p.stdin.Close()
					}
				}
			case frameResize:
				if rows, cols, ok := decodeSize(data); ok && p.tty != nil {
					p.tty.resize(rows, cols)
					if rec != nil {
						rec.Resize(cols, rows)
					}
				}
			}
		}
	}()

	var pumps sync.WaitGroup
	for _, o := range p.outs {
		pumps.Add(1)
		go func(o execOut) {
			defer pumps.Done()
			buf := make([]byte, ioChunk)
			for {
				n, err := o.r.Read(buf)
				if n > 0 {
					if rec != nil {
						rec.Output(buf[:n])
					}
					if werr := fw.frame(o.typ, buf[:n]); werr != nil {
						abandon()
						return
					}
				}
				if err != nil {
					return
				}
			}
		}(o)
	}

	// The leader is left unreaped (where the OS allows) until the output is
	// drained, so its PID, and the process group it leads, cannot be
	// recycled while the group may still be signalled below.
	exited := make(chan struct{})
	go func() {
		p.proc.awaitExit()
		close(exited)
	}()

	var timeout <-chan time.Time
	if req.TimeoutMS > 0 {
		t := time.NewTimer(time.Duration(req.TimeoutMS) * time.Millisecond)
		defer t.Stop()
		timeout = t.C
	}
	timedOut, abandoned := false, false
	select {
	case <-exited:
	case <-timeout:
		timedOut = true
	case <-gone:
		abandoned = true
	case <-streamGone(s):
		abandoned = true
	case <-ctx.Done():
		abandoned = true
	}
	if timedOut || abandoned {
		p.proc.killTree()
	}
	<-exited
	if p.tty == nil {
		// (A PTY master is both stdin and the output; it is closed once
		// the output has been drained, or the tail would be lost.)
		p.stdin.Close()
	} else {
		p.tty.processExited()
	}

	pumpsDone := make(chan struct{})
	go func() {
		pumps.Wait()
		close(pumpsDone)
	}()
	// stopOutput ends the pumps. Closing the pipes or terminal interrupts
	// their reads; the wait stays bounded regardless, and the final frame
	// guards against anything they write afterwards.
	stopOutput := func() {
		closeOuts(p)
		select {
		case <-pumpsDone:
		case <-time.After(outputGrace):
		}
	}
	if abandoned {
		// Nobody will read the rest; unblock pumps stuck writing to the
		// stream and stop.
		s.Reset()
		stopOutput()
		p.proc.reap()
		return context.Canceled
	}

	select {
	case <-pumpsDone:
	case <-time.After(outputGrace):
		// Something still holds the output open. The rest of the tree is
		// killed (a no-op once the leader can no longer pin the group ID);
		// processes that left the group (setsid) are not ours to track and
		// merely lose their output.
		p.proc.killTree()
		stopOutput()
	}
	closeOuts(p)

	code, sig, name := p.proc.reap()
	recEnd = fmt.Sprintf("exit %d", code)
	if name != "" {
		recEnd += " (" + name + ")"
	}
	if timedOut {
		recEnd += " (timed out)"
	}
	return fw.final(frameExit, exitMsg{Code: code, Signal: sig, SignalName: name, TimedOut: timedOut})
}

func closeOuts(p *execProc) {
	for _, o := range p.outs {
		o.r.Close()
	}
}
