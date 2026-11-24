package shell

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Service names registered on the host.
const (
	svcShell = "shell" // interactive sessions: attach, list, kill
	svcExec  = "exec"  // one non-interactive command
	svcSpawn = "spawn" // start a background job
	svcJobs  = "jobs"  // list, logs, kill, wait, remove background jobs
)

// Frame types. Both directions use wire.WriteFrame framing; the type byte
// says what the payload is. Values are shared between exec and interactive
// streams so the same reader handles both.
const (
	// client -> host
	frameInput    byte = 'i' // stdin / keystrokes
	frameInputEOF byte = 'e' // stdin reached EOF (empty payload)
	frameResize   byte = 'r' // 4 bytes: rows, cols (big-endian uint16)

	// host -> client
	frameStdout   byte = 'o' // stdout, or terminal output
	frameStderr   byte = 'E' // stderr (exec without a PTY only)
	frameExit     byte = 'x' // JSON exitMsg; last frame of the stream
	frameAttached byte = 'a' // JSON attachedMsg; first frame of an attach
	frameDetached byte = 'd' // JSON detachedMsg; host detached this client
	frameLogEnd   byte = 'z' // JSON jobsReply; last frame of a log stream
)

const (
	// maxFrame bounds a frame we accept. It is far below wire.MaxFrame so a
	// misbehaving peer cannot make us allocate 16 MiB per frame; our own
	// writers never send more than ioChunk.
	maxFrame = 256 << 10
	// ioChunk is the read size for process output and client input. Reads
	// return whatever is available, so interactive keystrokes go out as
	// single bytes immediately while bulk output naturally coalesces into
	// large frames.
	ioChunk = 32 << 10
	// maxDim bounds terminal dimensions accepted from the wire.
	maxDim = 4096
)

var errFrameTooLarge = errors.New("shell: frame too large")

// readFrame reads one wire frame, refusing payloads above maxFrame. buf is
// reused when large enough.
func readFrame(r io.Reader, buf []byte) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:4])
	if n > maxFrame {
		return 0, nil, errFrameTooLarge
	}
	p := buf
	if cap(p) < int(n) {
		p = make([]byte, n)
	}
	p = p[:n]
	if _, err := io.ReadFull(r, p); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[4], p, nil
}

// frameWriter serialises frame writes on one stream: QUIC stream writes from
// several goroutines would interleave frames.
type frameWriter struct {
	mu     sync.Mutex
	w      io.Writer
	closed bool // a final frame was written; nothing may follow it
}

var errFinished = errors.New("shell: stream finished")

func (f *frameWriter) frame(typ byte, p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errFinished
	}
	return wire.WriteFrame(f.w, typ, p)
}

// final writes v as the stream's last frame; later frames are refused, so a
// slow output writer can never slip data in after an exit message.
func (f *frameWriter) final(typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return errFinished
	}
	f.closed = true
	return wire.WriteFrame(f.w, typ, b)
}

func (f *frameWriter) json(typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return f.frame(typ, b)
}

func encodeSize(rows, cols int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint16(b[:2], uint16(clampDim(rows)))
	binary.BigEndian.PutUint16(b[2:], uint16(clampDim(cols)))
	return b[:]
}

func decodeSize(p []byte) (rows, cols int, ok bool) {
	if len(p) != 4 {
		return 0, 0, false
	}
	rows = int(binary.BigEndian.Uint16(p[:2]))
	cols = int(binary.BigEndian.Uint16(p[2:]))
	return rows, cols, rows > 0 && cols > 0 && rows <= maxDim && cols <= maxDim
}

func clampDim(n int) int {
	switch {
	case n <= 0:
		return 0
	case n > maxDim:
		return maxDim
	}
	return n
}

// exitMsg ends an exec or interactive stream.
type exitMsg struct {
	Code       int    `json:"code"`
	Signal     int    `json:"signal,omitempty"`
	SignalName string `json:"signal_name,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	// Error is set when the command could not be started at all.
	Error string `json:"error,omitempty"`
}

// streamGone returns a channel closed when the peer abandons s: the stream's
// context ends as soon as the peer resets its read side, which lets a host
// notice a cancelled client even while it is not reading from the stream
// (for example while blocked feeding a process's stdin).
func streamGone(s peer.Stream) <-chan struct{} {
	return s.Context().Done()
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validName checks session and job names: short, printable, safe to show in
// a terminal and to embed in a command line hint.
func validName(s string) error {
	if !nameRE.MatchString(s) {
		return fmt.Errorf("invalid name %q (use 1-64 of A-Z a-z 0-9 . _ -, starting with a letter or digit)", s)
	}
	return nil
}

// validEnv checks KEY=VALUE overrides coming off the wire or the command line.
func validEnv(env []string) error {
	if len(env) > 256 {
		return errors.New("too many environment variables")
	}
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.ContainsRune(kv, 0) {
			return fmt.Errorf("invalid environment entry %q (want KEY=VALUE)", kv)
		}
	}
	return nil
}

// printable replaces control characters in host-supplied strings before
// they are shown on the user's terminal, so a hostile host cannot inject
// escape sequences through a job name or an error message.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, s)
}
