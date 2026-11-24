package grant

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxCast bounds one recording; past it the recording ends with a marker
// and the session goes on unrecorded.
const MaxCast = 64 << 20

// Cast writes an asciicast v2 recording
// (https://docs.asciinema.org/manual/asciicast/v2/): a header line, then
// one JSON array per event. Play it back with `asciinema play FILE`.
type Cast struct {
	mu      sync.Mutex
	f       *os.File
	w       *bufio.Writer
	start   time.Time
	size    int64
	closed  bool
	pending []byte // an incomplete UTF-8 sequence held back from the last output
}

type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// NewCast creates the recording file (mode 0600, directories 0700).
func NewCast(path string, cols, rows int, title string) (*Cast, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if cols <= 0 || rows <= 0 {
		cols, rows = 80, 24
	}
	c := &Cast{f: f, w: bufio.NewWriterSize(f, 32<<10), start: time.Now()}
	hdr, _ := json.Marshal(castHeader{Version: 2, Width: cols, Height: rows, Timestamp: c.start.Unix(), Title: title,
		Env: map[string]string{"TERM": "xterm-256color"}})
	c.write(append(hdr, '\n'))
	c.w.Flush()
	return c, nil
}

func (c *Cast) write(b []byte) {
	c.size += int64(len(b))
	c.w.Write(b)
}

func (c *Cast) event(kind, data string) {
	if c.closed {
		return
	}
	t := strconv.FormatFloat(time.Since(c.start).Seconds(), 'f', 6, 64)
	d, _ := json.Marshal(data)
	line := make([]byte, 0, len(d)+len(t)+16)
	line = append(line, '[')
	line = append(line, t...)
	line = append(line, `, "`...)
	line = append(line, kind...)
	line = append(line, `", `...)
	line = append(line, d...)
	line = append(line, "]\n"...)
	if c.size+int64(len(line)) > MaxCast {
		m, _ := json.Marshal("recording stopped: size limit reached")
		c.write([]byte("[" + t + `, "m", ` + string(m) + "]\n"))
		c.closeLocked()
		return
	}
	c.write(line)
	// Flush often enough that a recording is useful while it runs and
	// survives a crash, without a write per keystroke.
	if c.w.Buffered() > 16<<10 {
		c.w.Flush()
	}
}

// text turns a chunk of terminal output into a string without splitting a
// UTF-8 sequence across events; invalid bytes become U+FFFD.
func (c *Cast) text(p []byte) string {
	b := append(c.pending, p...)
	c.pending = nil
	// Hold back an incomplete sequence at the end (at most 3 bytes).
	for i := 1; i <= 3 && i <= len(b); i++ {
		r := b[len(b)-i]
		if r < 0x80 {
			break
		}
		if utf8.RuneStart(r) {
			if !utf8.FullRune(b[len(b)-i:]) {
				c.pending = append([]byte(nil), b[len(b)-i:]...)
				b = b[:len(b)-i]
			}
			break
		}
	}
	return string(b)
}

// Output records terminal output.
func (c *Cast) Output(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.text(p); s != "" {
		c.event("o", s)
	}
}

// Input records what was typed or sent to the program.
func (c *Cast) Input(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.event("i", string(p))
}

// Resize records a terminal size change.
func (c *Cast) Resize(cols, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.event("r", strconv.Itoa(cols)+"x"+strconv.Itoa(rows))
}

// Close writes a marker with how the stream ended and closes the file.
func (c *Cast) Close(exit string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if len(c.pending) > 0 {
		c.event("o", string(c.pending))
		c.pending = nil
	}
	if exit != "" {
		c.event("m", exit)
	}
	c.closeLocked()
}

func (c *Cast) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	c.w.Flush()
	c.f.Close()
}
