package serial

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// List asks the host which serial ports it has. Strings are bounded but not
// sanitised for a terminal: print them through cli.Sanitize.
func List(ctx context.Context, c *peer.Conn) ([]PortInfo, error) {
	s, err := c.Open(ctx, Service, request{Op: "list"})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep reply
	if err := wire.ReadJSON(s, &rep); err != nil {
		return nil, err
	}
	if !rep.OK {
		return nil, errors.New(clean(rep.Error))
	}
	if len(rep.Ports) > maxPorts {
		rep.Ports = rep.Ports[:maxPorts]
	}
	for i := range rep.Ports {
		rep.Ports[i].clean()
	}
	return rep.Ports, nil
}

// OpenOptions are optional extras of Open.
type OpenOptions struct {
	// DTR and RTS, when non-nil, are the initial state of those lines.
	DTR, RTS *bool
	// OnNotice receives host diagnostics (a refused setting); may be nil.
	OnNotice func(string)
	// OnModem receives host input-line changes (CTS, DSR, DCD, RI); may be nil.
	OnModem func(Modem)
}

// Remote is an open remote serial port: an io.ReadWriteCloser of the bytes of
// the port plus control. It is safe for concurrent use.
type Remote struct {
	s    peer.Stream
	fw   *frameWriter
	pr   *io.PipeReader
	pw   *io.PipeWriter
	opts OpenOptions

	// Path is the device path on the host; Config what it was opened with.
	Path   string
	Config Config

	done chan struct{}
	once sync.Once
	err  error
}

// Open opens the host's port (exclusively) with cfg.
func Open(ctx context.Context, c *peer.Conn, port string, cfg Config, opts OpenOptions) (*Remote, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s, err := c.Open(ctx, Service, request{Op: "open", Port: port, Baud: cfg.Baud, Data: cfg.Data,
		Parity: string(rune(cfg.Parity)), Stop: cfg.Stop, RTSCTS: cfg.RTSCTS, DTR: opts.DTR, RTS: opts.RTS})
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, s.Reset)
	var rep reply
	err = wire.ReadJSON(s, &rep)
	if !stop() {
		s.Reset()
		return nil, ctx.Err()
	}
	if err == nil && !rep.OK {
		err = errors.New(clean(rep.Error))
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	r := &Remote{s: s, fw: &frameWriter{w: s}, opts: opts, Path: clean(rep.Port), Config: cfg, done: make(chan struct{})}
	r.pr, r.pw = io.Pipe()
	go r.readLoop()
	return r, nil
}

func (r *Remote) readLoop() {
	var err error
	defer func() {
		if errors.Is(err, io.EOF) {
			err = nil
		}
		r.finish(err)
	}()
	buf := make([]byte, 0, 16<<10)
	for {
		var typ byte
		var p []byte
		typ, p, err = readFrame(r.s, buf)
		if err != nil {
			return
		}
		switch typ {
		case frameData:
			if _, werr := r.pw.Write(p); werr != nil {
				err = werr
				return
			}
		case frameNotice:
			if r.opts.OnNotice != nil {
				if len(p) > 1024 {
					p = p[:1024]
				}
				r.opts.OnNotice(clean(string(p)))
			}
		case frameModem:
			var m Modem
			if json.Unmarshal(p, &m) == nil && r.opts.OnModem != nil {
				r.opts.OnModem(m)
			}
		}
	}
}

func (r *Remote) finish(err error) {
	r.once.Do(func() {
		r.err = err
		close(r.done)
		r.pw.CloseWithError(io.EOF)
		r.s.Close()
	})
}

// Read returns bytes from the remote port; io.EOF once the port or session is gone.
func (r *Remote) Read(b []byte) (int, error) { return r.pr.Read(b) }

// Write sends bytes to the remote port.
func (r *Remote) Write(b []byte) (int, error) {
	select {
	case <-r.done:
		return 0, io.ErrClosedPipe
	default:
	}
	for n := 0; n < len(b); {
		m := min(len(b)-n, chunkSize)
		if err := r.fw.write(frameData, b[n:n+m]); err != nil {
			return n, err
		}
		n += m
	}
	return len(b), nil
}

// SetConfig changes the line settings of the remote port.
func (r *Remote) SetConfig(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return r.fw.write(frameConfig, mustJSON(cfg.wire()))
}

// SetModem sets DTR and/or RTS on the remote port (nil leaves a line alone).
func (r *Remote) SetModem(dtr, rts *bool) error {
	return r.fw.write(frameModem, mustJSON(modemSet{dtr, rts}))
}

// Break asks the host to hold a break condition for d (at most 2 s).
func (r *Remote) Break(d time.Duration) error {
	ms := min(max(int(d/time.Millisecond), 1), maxBreakMS)
	return r.fw.write(frameBreak, binary.BigEndian.AppendUint16(nil, uint16(ms)))
}

// Done is closed when the port or the session ends.
func (r *Remote) Done() <-chan struct{} { return r.done }

// Err is why Done closed (nil for a clean end). Valid after Done.
func (r *Remote) Err() error { return r.err }

// Close closes the remote port.
func (r *Remote) Close() error {
	r.finish(nil)
	return nil
}
