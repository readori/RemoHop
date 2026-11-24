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

	"github.com/readori/RemoHop/internal/config"
)

// controlSocket is the running host's local control channel: a Unix socket
// in the config directory. The directory is mode 0700 and the socket 0600,
// so only the user the host runs as can reach it; that is the whole access
// control. It carries one JSON request and one JSON reply per connection.
const controlSocket = "host.sock"

type controlRequest struct {
	Op    string `json:"op"` // status | approvals | decide | otp | otp-new | grants | panic
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
	// Closed is how many sessions a grants or panic request ended.
	Closed int `json:"closed,omitempty"`
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
		return controlReply{Closed: h.endGrantSessions(req.All, "ended by the host's owner (remohop host panic)")}
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
