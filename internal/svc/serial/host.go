package serial

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

func init() {
	hk = hooks{resolvePort: resolveListed, openPort: openDevice, hostList: ListPorts}
	// Serial access is its own feature, as powerful as a shell where the
	// port is a console (a login prompt).
	perm.Register(Service, perm.Serial)
	peer.Handle(Service, hostServe)
}

// Port is an open serial device on the host.
type Port interface {
	io.ReadWriteCloser
	Apply(Config) error
	// SetModem sets DTR and/or RTS (nil = leave alone).
	SetModem(dtr, rts *bool) error
	// Modem reads the input status lines.
	Modem() (Modem, error)
	// Break holds the line in break condition for d.
	Break(d time.Duration) error
}

// hooks are what the host side is built on; tests replace them to serve a
// pty instead of a real UART.
type hooks struct {
	// resolvePort maps what the client asked for to a device path, or fails.
	resolvePort func(string) (string, error)
	// openPort opens path exclusively with cfg.
	openPort func(string, Config) (Port, error)
	// hostList enumerates the host's ports.
	hostList func() ([]PortInfo, error)
}

var (
	hookMu sync.RWMutex
	hk     hooks
)

func getHooks() hooks {
	hookMu.RLock()
	defer hookMu.RUnlock()
	return hk
}

// resolveListed accepts only ports the host enumerates: by path, short name or
// /dev/serial/by-id name. A client cannot make the host open arbitrary files.
func resolveListed(name string) (string, error) {
	ports, err := getHooks().hostList()
	if err != nil {
		return "", err
	}
	for _, p := range ports {
		if name == p.Path || name == p.Name {
			return p.Path, nil
		}
		for _, id := range p.ByID {
			if name == id || name == filepath.Base(id) {
				return p.Path, nil
			}
		}
	}
	return "", fmt.Errorf("no such serial port %q", clean(name))
}

var (
	busyMu sync.Mutex
	busy   = map[string]bool{}
)

func claim(path string) bool {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	busyMu.Lock()
	defer busyMu.Unlock()
	if busy[path] {
		return false
	}
	busy[path] = true
	return true
}

func release(path string) {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	busyMu.Lock()
	delete(busy, path)
	busyMu.Unlock()
}

func hostServe(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, reply{Error: "bad request"})
		return err
	}
	switch req.Op {
	case "list":
		ports, err := getHooks().hostList()
		if err != nil {
			wire.WriteJSON(s, reply{Error: err.Error()})
			return err
		}
		if len(ports) > maxPorts {
			ports = ports[:maxPorts]
		}
		return wire.WriteJSON(s, reply{OK: true, Ports: ports})
	case "open":
		return hostOpen(ctx, s, req)
	}
	wire.WriteJSON(s, reply{Error: "unknown op"})
	return errors.New("serial: unknown op")
}

func hostOpen(ctx context.Context, s peer.Stream, req request) error {
	fail := func(err error) error {
		wire.WriteJSON(s, reply{Error: err.Error()})
		return err
	}
	cfg := Config{Baud: req.Baud, Data: req.Data, Stop: req.Stop, RTSCTS: req.RTSCTS}
	if len(req.Parity) == 1 {
		cfg.Parity = req.Parity[0]
	}
	if err := cfg.Validate(); err != nil {
		return fail(err)
	}
	if len(req.Port) == 0 || len(req.Port) > 512 {
		return fail(errors.New("bad port name"))
	}
	path, err := getHooks().resolvePort(req.Port)
	if err != nil {
		return fail(err)
	}
	if !claim(path) {
		return fail(fmt.Errorf("%s is busy (open by another session)", filepath.Base(path)))
	}
	defer release(path)
	p, err := getHooks().openPort(path, cfg)
	if err != nil {
		return fail(err)
	}
	defer p.Close()
	if req.DTR != nil || req.RTS != nil {
		if err := p.SetModem(req.DTR, req.RTS); err != nil {
			slog.Debug("serial: initial modem lines", "err", err)
		}
	}
	if err := wire.WriteJSON(s, reply{OK: true, Port: path, Baud: cfg.Baud}); err != nil {
		return err
	}
	return relayHost(ctx, s, p)
}

// relayHost moves bytes between the port and the stream until either ends.
func relayHost(ctx context.Context, s peer.Stream, p Port) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { p.Close(); s.Reset() })
	defer stop()
	fw := &frameWriter{w: s}
	errc := make(chan error, 3)

	go func() { // port -> client
		buf := make([]byte, 16<<10)
		for {
			n, err := p.Read(buf)
			if n > 0 {
				if werr := fw.write(frameData, buf[:n]); werr != nil {
					errc <- werr
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() { // modem status -> client, only when it changes
		last, err := p.Modem()
		if err != nil {
			return // not a modem-capable device
		}
		fw.write(frameModem, mustJSON(last))
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			m, err := p.Modem()
			if err != nil {
				return
			}
			if m != last {
				last = m
				if fw.write(frameModem, mustJSON(m)) != nil {
					return
				}
			}
		}
	}()

	go func() { // client -> port
		buf := make([]byte, 0, 16<<10)
		for {
			typ, payload, err := readFrame(s, buf)
			if err != nil {
				errc <- err
				return
			}
			if err := hostFrame(p, fw, typ, payload); err != nil {
				errc <- err
				return
			}
		}
	}()

	err := <-errc
	cancel()
	p.Close()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return err
}

func hostFrame(p Port, fw *frameWriter, typ byte, payload []byte) error {
	notice := func(format string, a ...any) { fw.write(frameNotice, []byte(fmt.Sprintf(format, a...))) }
	switch typ {
	case frameData:
		_, err := p.Write(payload)
		return err
	case frameConfig:
		var wc wireConfig
		if err := json.Unmarshal(payload, &wc); err != nil {
			notice("bad config frame")
			return nil
		}
		cfg, err := wc.config()
		if err == nil {
			err = p.Apply(cfg)
		}
		if err != nil {
			notice("config %d %d%s%d refused: %v", wc.Baud, wc.Data, wc.Parity, wc.Stop, err)
		}
	case frameModem:
		var m modemSet
		if err := json.Unmarshal(payload, &m); err != nil {
			notice("bad modem frame")
			return nil
		}
		if err := p.SetModem(m.DTR, m.RTS); err != nil {
			notice("modem control lines: %v", err)
		}
	case frameBreak:
		if len(payload) != 2 {
			return nil
		}
		ms := int(binary.BigEndian.Uint16(payload))
		if ms < 1 {
			ms = 1
		}
		if ms > maxBreakMS {
			ms = maxBreakMS
		}
		if err := p.Break(time.Duration(ms) * time.Millisecond); err != nil {
			notice("break: %v", err)
		}
	}
	// Unknown frame types are ignored for forward compatibility.
	return nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
