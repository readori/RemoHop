package serial

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/wire"
)

// Service is the wire service name.
const Service = "serial"

// Frame types after an open.
const (
	frameData   byte = 1
	frameConfig byte = 2
	frameModem  byte = 3
	frameBreak  byte = 4
	frameNotice byte = 5
)

const (
	maxFramePayload = 64 << 10
	chunkSize       = 32 << 10
	maxPorts        = 256
	maxStr          = 256
	minBaud         = 50
	maxBaud         = 4_000_000
	maxBreakMS      = 2000
)

// ErrUnsupported is returned where a platform has no implementation.
var ErrUnsupported = errors.New("serial: not supported on this platform")

// Config is the line setting of a port: Baud, 5-8 data bits, parity 'N',
// 'E' or 'O', 1 or 2 stop bits, optional RTS/CTS hardware flow control.
type Config struct {
	Baud   int  `json:"baud"`
	Data   int  `json:"data"`
	Parity byte `json:"-"`
	Stop   int  `json:"stop"`
	RTSCTS bool `json:"rtscts,omitempty"`
}

// DefaultConfig is 115200 8N1.
func DefaultConfig() Config { return Config{Baud: 115200, Data: 8, Parity: 'N', Stop: 1} }

func (c Config) String() string {
	return fmt.Sprintf("%d %d%c%d", c.Baud, c.Data, c.Parity, c.Stop)
}

// Validate bounds every field; peers are untrusted.
func (c Config) Validate() error {
	switch {
	case c.Baud < minBaud || c.Baud > maxBaud:
		return fmt.Errorf("baud rate %d out of range %d-%d", c.Baud, minBaud, maxBaud)
	case c.Data < 5 || c.Data > 8:
		return fmt.Errorf("data bits %d: want 5-8", c.Data)
	case c.Parity != 'N' && c.Parity != 'E' && c.Parity != 'O':
		return fmt.Errorf("parity %q: want N, E or O", string(rune(c.Parity)))
	case c.Stop != 1 && c.Stop != 2:
		return fmt.Errorf("stop bits %d: want 1 or 2", c.Stop)
	}
	return nil
}

// ParseFormat parses "8N1" style frame formats into c's Data/Parity/Stop.
func (c *Config) ParseFormat(s string) error {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 3 || s[0] < '5' || s[0] > '8' || (s[1] != 'N' && s[1] != 'E' && s[1] != 'O') || (s[2] != '1' && s[2] != '2') {
		return fmt.Errorf("bad format %q: want like 8N1, 7E1, 8N2", s)
	}
	c.Data, c.Parity, c.Stop = int(s[0]-'0'), s[1], int(s[2]-'0')
	return nil
}

// wireConfig is Config as it travels (parity as a string).
type wireConfig struct {
	Baud   int    `json:"baud"`
	Data   int    `json:"data"`
	Parity string `json:"parity"`
	Stop   int    `json:"stop"`
	RTSCTS bool   `json:"rtscts,omitempty"`
}

func (c Config) wire() wireConfig {
	return wireConfig{c.Baud, c.Data, string(rune(c.Parity)), c.Stop, c.RTSCTS}
}

func (w wireConfig) config() (Config, error) {
	if len(w.Parity) != 1 {
		return Config{}, errors.New("bad parity")
	}
	c := Config{w.Baud, w.Data, w.Parity[0], w.Stop, w.RTSCTS}
	return c, c.Validate()
}

// Modem is the state of the input modem status lines.
type Modem struct {
	CTS bool `json:"cts"`
	DSR bool `json:"dsr"`
	DCD bool `json:"dcd"`
	RI  bool `json:"ri"`
}

type request struct {
	Op     string `json:"op"`
	Port   string `json:"port,omitempty"`
	Baud   int    `json:"baud,omitempty"`
	Data   int    `json:"data,omitempty"`
	Parity string `json:"parity,omitempty"`
	Stop   int    `json:"stop,omitempty"`
	RTSCTS bool   `json:"rtscts,omitempty"`
	DTR    *bool  `json:"dtr,omitempty"`
	RTS    *bool  `json:"rts,omitempty"`
}

type reply struct {
	OK    bool       `json:"ok"`
	Error string     `json:"error,omitempty"`
	Ports []PortInfo `json:"ports,omitempty"`
	Port  string     `json:"port,omitempty"`
	Baud  int        `json:"baud,omitempty"`
}

type modemSet struct {
	DTR *bool `json:"dtr,omitempty"`
	RTS *bool `json:"rts,omitempty"`
}

// PortInfo describes one serial port of a machine.
type PortInfo struct {
	Path        string   `json:"path"`                  // /dev/ttyUSB0, COM3
	Name        string   `json:"name"`                  // ttyUSB0, COM3
	ByID        []string `json:"by_id,omitempty"`       // stable /dev/serial/by-id names
	Driver      string   `json:"driver,omitempty"`      // kernel driver
	Description string   `json:"description,omitempty"` // manufacturer product
	VID         string   `json:"vid,omitempty"`
	PID         string   `json:"pid,omitempty"`
	Serial      string   `json:"serial,omitempty"`
}

// clean bounds and strips a peer-supplied string.
func clean(s string) string {
	if len(s) > maxStr {
		s = s[:maxStr]
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	return s
}

func (p *PortInfo) clean() {
	p.Path, p.Name, p.Driver, p.Description = clean(p.Path), clean(p.Name), clean(p.Driver), clean(p.Description)
	p.VID, p.PID, p.Serial = clean(p.VID), clean(p.PID), clean(p.Serial)
	if len(p.ByID) > 8 {
		p.ByID = p.ByID[:8]
	}
	for i := range p.ByID {
		p.ByID[i] = clean(p.ByID[i])
	}
}

// frameWriter serialises frame writes from several goroutines.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) write(typ byte, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return wire.WriteFrame(f.w, typ, payload)
}

// readFrame reads one frame, refusing payloads above maxFramePayload before
// allocating anything.
func readFrame(r io.Reader, buf []byte) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint32(hdr[:4]))
	if n > maxFramePayload {
		return 0, nil, wire.ErrTooLarge
	}
	if cap(buf) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[4], buf, nil
}
