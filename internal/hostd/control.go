package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
)

// controlSocket is the running host's local control channel: a Unix socket
// in the config directory. The directory is mode 0700 and the socket 0600,
// so only the user the host runs as can reach it; that is the whole access
// control. It carries one JSON request and one JSON reply per connection.
const controlSocket = "host.sock"

type controlRequest struct {
	Op    string `json:"op"` // status | approvals | decide | otp | otp-new | grants | panic | sessions | disconnect
	N     int    `json:"n,omitempty"`
	Allow bool   `json:"allow,omitempty"`
	// All, for panic: end every session, not only granted ones.
	All bool `json:"all,omitempty"`
}

type controlReply struct {
	Error     string     `json:"error,omitempty"`
	Status    *Status    `json:"status,omitempty"`
	Approvals []Approval `json:"approvals,omitempty"`
	Password  string     `json:"password,omitempty"`
	// Closed is how many sessions a grants, panic or disconnect request ended.
	Closed int `json:"closed,omitempty"`
	// Sessions answers a sessions request.
	Sessions []ControlSession `json:"sessions,omitempty"`
}

// ControlSession is a live session as the control channel reports it (the
// desktop app shows the RemoHop service's sessions with it, and can end them).
type ControlSession struct {
	ID       int       `json:"id"`
	Peer     string    `json:"peer"`             // pairing name or key fingerprint
	Device   string    `json:"device,omitempty"` // the name the client gave itself
	Identity string    `json:"identity"`
	From     string    `json:"from"`
	Path     string    `json:"path"`
	Since    time.Time `json:"since"`
}

// serveControl answers control requests until ctx ends. Not being able to
// listen (a config directory path too long for a socket address, say) only
// costs the commands that talk to the running host.
func (h *Host) serveControl(ctx context.Context) {
	path := filepath.Join(h.dir, controlSocket)
	if runtime.GOOS == "windows" {
		// A socket file has no mode there; the directory's access list is
		// all that keeps other users away from it (config.MakePrivate).
		if private, err := config.IsPrivate(h.dir); err != nil || !private {
			h.log.Warn("no local control socket: the config directory is not private to this user", "dir", h.dir, "err", err)
			return
		}
	}
	os.Remove(path) // left by a host that died; a live one holds the status lock
	ln, err := net.Listen("unix", path)
	if err != nil {
		h.log.Warn("no local control socket: `remohop host approve` and `remohop host otp` will not work", "err", err)
		return
	}
	os.Chmod(path, 0o600)
	go func() {
		<-ctx.Done()
		ln.Close()
		os.Remove(path)
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			var req controlRequest
			if err := json.NewDecoder(&limitedConn{c, 4096}).Decode(&req); err != nil {
				return
			}
			json.NewEncoder(c).Encode(h.control(req))
		}()
	}
}

// limitedConn bounds what is read from a control client.
type limitedConn struct {
	net.Conn
	left int
}

func (l *limitedConn) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, errors.New("request too large")
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	n, err := l.Conn.Read(p)
	l.left -= n
	return n, err
}

func (h *Host) control(req controlRequest) controlReply {
	switch req.Op {
	case "status":
		st := h.Status()
		return controlReply{Status: &st}
	case "approvals":
		return controlReply{Approvals: h.Pending()}
	case "decide":
		if err := h.Decide(req.N, req.Allow); err != nil {
			return controlReply{Error: err.Error()}
		}
		return controlReply{}
	case "otp-new":
		if err := h.RotatePassword(); err != nil {
			return controlReply{Error: err.Error()}
		}
		fallthrough
	case "otp":
		pw, mode := h.TempPassword()
		if pw == "" {
			return controlReply{Error: fmt.Sprintf("the host has no temporary password (password mode: %s)", mode)}
		}
		return controlReply{Password: pw}
	case "grants":
		// The grants file changed (revoke, reissue, suspend): apply it
		// to live sessions now rather than at the next tick.
		return controlReply{Closed: h.enforceGrants()}
	case "panic":
		return controlReply{Closed: h.endGrantSessions(req.All, reasonPanicPrefix+" (remohop host panic)")}
	case "sessions":
		out := []ControlSession{}
		for _, s := range h.Sessions() {
			peer := s.Auth.TokenName
			if peer == "" {
				peer = auth.Fingerprint(s.Auth.ClientKey)
			}
			out = append(out, ControlSession{ID: s.ID(), Peer: peer, Device: s.Auth.ClientName, Identity: auth.Fingerprint(s.Auth.ClientKey),
				From: s.Remote.String(), Path: s.Path, Since: s.Started})
		}
		return controlReply{Sessions: out}
	case "disconnect":
		// N names the session; 0 ends them all.
		n := 0
		for _, s := range h.Sessions() {
			if req.N == 0 || s.ID() == req.N {
				if h.Disconnect(s.ID(), ReasonDisconnectedByOwner) == nil {
					n++
				}
			}
		}
		if req.N != 0 && n == 0 {
			return controlReply{Error: fmt.Sprintf("no session %d", req.N)}
		}
		return controlReply{Closed: n}
	}
	return controlReply{Error: "unknown request"}
}

// errNotRunning is returned when no host answers on the control socket.
var errNotRunning = errors.New("no host is running here (start one with `remohop host`)")

// controlCall sends one request to the host running on dir.
func controlCall(dir string, req controlRequest) (*controlReply, error) {
	c, err := net.DialTimeout("unix", filepath.Join(dir, controlSocket), 2*time.Second)
	if err != nil {
		return nil, errNotRunning
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return nil, err
	}
	var rep controlReply
	if err := json.NewDecoder(c).Decode(&rep); err != nil {
		return nil, fmt.Errorf("the running host did not answer: %w", err)
	}
	if rep.Error != "" {
		return nil, errors.New(rep.Error)
	}
	return &rep, nil
}

// LiveStatus asks the host running on dir for its current status. It
// returns (nil, nil) when none is running.
func LiveStatus(dir string) (*Status, error) {
	rep, err := controlCall(dir, controlRequest{Op: "status"})
	if errors.Is(err, errNotRunning) {
		// A host without a control socket still has its status file.
		return ReadStatus(dir)
	}
	if err != nil {
		return nil, err
	}
	return rep.Status, nil
}

// ControlSessions lists the sessions of the host running on dir.
func ControlSessions(dir string) ([]ControlSession, error) {
	rep, err := controlCall(dir, controlRequest{Op: "sessions"})
	if err != nil {
		return nil, err
	}
	return rep.Sessions, nil
}

// ControlDisconnect ends session id (0: every session) of the host running
// on dir and reports how many it ended.
func ControlDisconnect(dir string, id int) (int, error) {
	rep, err := controlCall(dir, controlRequest{Op: "disconnect", N: id})
	if err != nil {
		return 0, err
	}
	return rep.Closed, nil
}
