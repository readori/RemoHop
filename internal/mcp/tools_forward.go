package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/svc/forward"
)

type forwardStartIn struct {
	Target  string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Spec    string `json:"spec" jsonschema:"Forward spec like ssh -L: LOCALPORT:REMOTEPORT (target 127.0.0.1 on the remote machine), LOCALPORT:REMOTEHOST:REMOTEPORT (a host reachable from the remote machine), or BIND:LOCALPORT:REMOTEHOST:REMOTEPORT. Use LOCALPORT 0 to pick a free port. Binds to 127.0.0.1 unless BIND is given."`
	Reverse bool   `json:"reverse,omitempty" jsonschema:"Reverse forward (like ssh -R): the REMOTE machine listens on the first port and connections are carried back to REMOTEHOST:REMOTEPORT as seen from this computer."`
}

type forwardInfo struct {
	ID          string `json:"id"`
	Target      string `json:"target" jsonschema:"The remote machine this forward belongs to."`
	Spec        string `json:"spec"`
	Reverse     bool   `json:"reverse"`
	Listen      string `json:"listen" jsonschema:"host:port being listened on: on this computer for a normal forward, on the remote machine for a reverse one."`
	ForwardTo   string `json:"forward_to" jsonschema:"host:port connections are carried to."`
	Connections uint64 `json:"connections" jsonschema:"Connections accepted so far."`
	Active      int64  `json:"active" jsonschema:"Connections open now."`
	BytesUp     int64  `json:"bytes_up" jsonschema:"Bytes sent from the accepted connections towards the destination."`
	BytesDown   int64  `json:"bytes_down" jsonschema:"Bytes received back from the destination."`
	Started     string `json:"started"`
}

type forwardListIn struct {
	Target string `json:"target,omitempty" jsonschema:"Only forwards to this remote machine. Omit to list all."`
}

type forwardListOut struct {
	Forwards []forwardInfo `json:"forwards"`
}

type forwardStopIn struct {
	ID string `json:"id" jsonschema:"Forward id from remohop_forward_start or remohop_forward_list."`
}

// fwEntry is a forward owned by the server.
type fwEntry struct {
	id      string
	target  string
	f       *forward.Forwarder
	started time.Time
	release func()
}

func (e *fwEntry) info() forwardInfo {
	st := e.f.Stats()
	listen := e.f.Addr().String()
	if !e.f.Reverse() {
		listen = loopbackAddr(e.f.Addr())
	}
	return forwardInfo{
		ID: e.id, Target: e.target, Spec: e.f.Spec().String(), Reverse: e.f.Reverse(),
		Listen: listen, ForwardTo: e.f.Target(),
		Connections: st.Connections, Active: st.Active, BytesUp: st.BytesUp, BytesDown: st.BytesDown,
		Started: e.started.Format(time.RFC3339),
	}
}

// loopbackAddr renders a listen address a client can actually connect to: a
// wildcard bind is reachable through the loopback address.
func loopbackAddr(a net.Addr) string {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return a.String()
	}
	if ta.IP == nil || ta.IP.IsUnspecified() {
		return net.JoinHostPort("127.0.0.1", fmt.Sprint(ta.Port))
	}
	return ta.String()
}

// forwards is the registry of forwards started through the MCP server. They
// live in this process: when it exits, every forward goes with it.
type forwards struct {
	mu  sync.Mutex
	seq int
	m   map[string]*fwEntry
}

func newForwards() *forwards { return &forwards{m: map[string]*fwEntry{}} }

func (r *forwards) add(e *fwEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.id = fmt.Sprintf("fwd-%d", r.seq)
	r.m[e.id] = e
}

func (r *forwards) remove(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

func (r *forwards) get(id string) *fwEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.m[id]
}

func (r *forwards) list() []*fwEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*fwEntry, 0, len(r.m))
	for _, e := range r.m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].started.Before(out[j].started) || (out[i].started.Equal(out[j].started) && out[i].id < out[j].id)
	})
	return out
}

func (r *forwards) closeAll() {
	for _, e := range r.list() {
		e.f.Close()
	}
}

func (s *Server) registerForward() {
	addTool(s, "remohop_forward_start", "Forward a remote port to localhost",
		"Open a TCP port forward through the remote machine and return the local address to connect to. "+
			"Use it when a service on (or only reachable from) the remote machine is not reachable from here: "+
			"e.g. spec \"0:8080\" exposes the remote's localhost:8080 on a free local port, \"0:db.internal:5432\" reaches a database the remote machine can see. "+
			"Then test it from this computer (curl, psql, a browser) at the returned address. "+
			"Set reverse=true to make the remote machine listen instead and send connections back here. "+
			"The forward lives inside this MCP server process: it ends with remohop_forward_stop, when the server exits, or if the session to the remote machine is lost (the session is kept open while a forward exists).",
		additive(), s.forwardStart)
	addTool(s, "remohop_forward_list", "List active port forwards",
		"List the port forwards started through this MCP server with their addresses and traffic statistics (connections accepted, open now, bytes each way).",
		localReadOnly(), s.forwardList)
	addTool(s, "remohop_forward_stop", "Stop a port forward",
		"Close a port forward started with remohop_forward_start and drop its open connections.",
		idempotentLocal(), s.forwardStop)
}

func (s *Server) forwardStart(ctx context.Context, in forwardStartIn) (*reply[forwardInfo], error) {
	spec, err := forward.ParseSpec(in.Spec)
	if err != nil {
		return nil, fmt.Errorf("bad spec %q: %w (examples: 0:8080, 18080:db.internal:5432)", in.Spec, err)
	}
	target, err := s.resolveTarget(in.Target)
	if err != nil {
		return nil, err
	}
	conn, release, err := s.pool.acquire(ctx, target)
	if err != nil {
		return nil, dialError(target, err)
	}
	// The forward must outlive this request, so it runs on the pool's
	// lifetime, not the request's.
	listen := forward.Listen
	if in.Reverse {
		listen = forward.ListenReverse
	}
	f, err := listen(s.pool.ctx, conn, spec)
	if err != nil {
		release()
		return nil, fmt.Errorf("cannot start the forward: %w", err)
	}
	e := &fwEntry{target: target, f: f, started: time.Now(), release: release}
	s.fwd.add(e)
	go func() {
		<-f.Done()
		s.fwd.remove(e.id)
		release()
	}()
	info := e.info()
	var msg string
	if in.Reverse {
		msg = fmt.Sprintf("%s: the remote machine now listens on %s; connections are carried back to %s from this computer.", info.ID, info.Listen, info.ForwardTo)
	} else {
		msg = fmt.Sprintf("%s: connect to %s on this computer to reach %s via the remote machine (e.g. curl http://%s/).", info.ID, info.Listen, info.ForwardTo, info.Listen)
	}
	return textReply(info, msg), nil
}

func (s *Server) forwardList(_ context.Context, in forwardListIn) (*reply[forwardListOut], error) {
	want := ""
	if strings.TrimSpace(in.Target) != "" {
		t, err := s.resolveTarget(in.Target)
		if err != nil {
			return nil, err
		}
		want = t
	}
	out := forwardListOut{Forwards: []forwardInfo{}}
	var b strings.Builder
	for _, e := range s.fwd.list() {
		if want != "" && e.target != want {
			continue
		}
		fi := e.info()
		out.Forwards = append(out.Forwards, fi)
		kind := "local"
		if fi.Reverse {
			kind = "reverse"
		}
		fmt.Fprintf(&b, "%s  %s  %s  listen %s -> %s  conns %d (active %d)  up %d B  down %d B\n",
			fi.ID, fi.Target, kind, fi.Listen, fi.ForwardTo, fi.Connections, fi.Active, fi.BytesUp, fi.BytesDown)
	}
	if len(out.Forwards) == 0 {
		b.WriteString("no active forwards\n")
	}
	return textReply(out, b.String()), nil
}

func (s *Server) forwardStop(_ context.Context, in forwardStopIn) (*reply[forwardInfo], error) {
	e := s.fwd.get(in.ID)
	if e == nil {
		return nil, fmt.Errorf("no active forward %q (it may have ended already; see remohop_forward_list)", in.ID)
	}
	info := e.info()
	if err := e.f.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return nil, fmt.Errorf("closing forward: %w", err)
	}
	return textReply(info, fmt.Sprintf("stopped %s (%s); it served %d connections, %d B up, %d B down.", info.ID, info.Listen, info.Connections, info.BytesUp, info.BytesDown)), nil
}
