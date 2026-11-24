package forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Service names on the wire.
const (
	svcDial   = "forward"
	svcListen = "forward.listen"
	svcAccept = "forward.accept"
)

// dialTimeout bounds how long the host (and a reverse-forward client) waits
// for a target to accept a connection.
const dialTimeout = 10 * time.Second

type dialReq struct {
	Network string `json:"network"`
	Addr    string `json:"addr"`
}

// status is the first message the dialling side of a stream answers with.
type status struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type listenReq struct {
	Network string `json:"network"`
	Bind    string `json:"bind"`
	ID      string `json:"id"`
}

type listenReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Addr  string `json:"addr,omitempty"`
}

type acceptReq struct {
	ID string `json:"id"`
}

func init() {
	peer.Handle(svcDial, hostDial)
	peer.Handle(svcListen, hostListen)
}

func checkNetwork(n string) error {
	switch n {
	case "tcp", "tcp4", "tcp6":
		return nil
	}
	return fmt.Errorf("unsupported network %q", n)
}

func checkHostPort(addr string) error {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if h == "" && p == "" {
		return errors.New("empty address")
	}
	return nil
}

// hostDial serves "forward": dial the requested address from the host and
// tunnel it over the stream.
func hostDial(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req dialReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, status{Error: "bad request"})
		return err
	}
	if req.Network == "" {
		req.Network = "tcp"
	}
	err := checkNetwork(req.Network)
	if err == nil {
		err = checkHostPort(req.Addr)
	}
	var nc net.Conn
	if err == nil {
		d := net.Dialer{Timeout: dialTimeout}
		nc, err = d.DialContext(ctx, req.Network, req.Addr)
	}
	if err != nil {
		slog.Debug("forward: dial failed", "addr", req.Addr, "err", err)
		wire.WriteJSON(s, status{Error: err.Error()})
		return err
	}
	if err := wire.WriteJSON(s, status{OK: true}); err != nil {
		nc.Close()
		return err
	}
	sc := newStreamConn(s, req.Network, req.Addr)
	stop := context.AfterFunc(ctx, func() { sc.Abort(); tcpEnd{nc}.Abort() })
	defer stop()
	pipe(tcpEnd{nc}, sc, nil, nil)
	return nil
}

// hostListen serves "forward.listen": listen on the host and carry every
// inbound connection back to the client over "forward.accept" streams. The
// request stream is the control stream; the listener lives exactly as long
// as it stays open.
func hostListen(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req listenReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		wire.WriteJSON(s, listenReply{Error: "bad request"})
		return err
	}
	if req.Network == "" {
		req.Network = "tcp"
	}
	err := checkNetwork(req.Network)
	if err == nil && (req.ID == "" || len(req.ID) > 64) {
		err = errors.New("bad listener id")
	}
	var ln net.Listener
	if err == nil {
		ln, err = net.Listen(req.Network, req.Bind)
	}
	if err != nil {
		wire.WriteJSON(s, listenReply{Error: err.Error()})
		return err
	}
	if err := wire.WriteJSON(s, listenReply{OK: true, Addr: ln.Addr().String()}); err != nil {
		ln.Close()
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	context.AfterFunc(ctx, func() { ln.Close() })
	// The client never writes on the control stream, so a read returning
	// means it closed the stream or the session died.
	go func() { io.Copy(io.Discard, s); cancel() }()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel() // a dead listener ends the forward
		acceptLoop(ctx, ln, &wg, func(nc net.Conn) { hostAccepted(ctx, c, req, nc) })
	}()
	<-ctx.Done()
	wg.Wait()
	return nil
}

// hostAccepted carries one connection accepted by a reverse listener to the
// client.
func hostAccepted(ctx context.Context, c *peer.Conn, req listenReq, nc net.Conn) {
	te := tcpEnd{nc}
	st, err := c.Open(ctx, svcAccept, acceptReq{ID: req.ID})
	if err != nil {
		slog.Debug("forward: reverse accept open failed", "err", err)
		te.Abort()
		return
	}
	sc := newStreamConn(st, req.Network, nc.LocalAddr().String())
	stop := context.AfterFunc(ctx, func() { sc.Abort(); te.Abort() })
	defer stop()
	if err := readStatus(ctx, st, &status{}); err != nil {
		slog.Debug("forward: client could not reach reverse target", "err", err)
		sc.Abort()
		te.Abort()
		return
	}
	pipe(te, sc, nil, nil)
}

// readStatus reads a status-style reply (any struct with OK and Error) and
// turns a refusal into an error. A cancelled ctx resets the stream.
func readStatus(ctx context.Context, s peer.Stream, into any) error {
	stop := context.AfterFunc(ctx, s.Reset)
	err := wire.ReadJSON(s, into)
	if !stop() {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	switch r := into.(type) {
	case *status:
		if !r.OK {
			return errors.New(r.Error)
		}
	case *listenReply:
		if !r.OK {
			return errors.New(r.Error)
		}
	}
	return nil
}

// acceptLoop accepts until ln is closed, running handle for each connection
// under wg. It returns when ctx ends or the listener fails for good.
func acceptLoop(ctx context.Context, ln net.Listener, wg *sync.WaitGroup, handle func(net.Conn)) {
	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			// Transient failures such as EMFILE: back off rather than spin.
			slog.Debug("forward: accept failed", "err", err)
			select {
			case <-time.After(50 * time.Millisecond):
				continue
			case <-ctx.Done():
				return
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle(nc)
		}()
	}
}
