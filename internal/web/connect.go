package web

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
)

// DialRequest is what the gateway asks of its dial seam.
type DialRequest struct {
	// Target is an ID, alias or host:port as the user typed it.
	Target string
	// Password is the host's password, typed in the page. Empty means
	// "use the pairing saved on this gateway", if any.
	Password string
	// Notify receives progress notices ("waiting for approval ...").
	Notify func(string)
}

// DialFunc connects to a host. The default calls dial.Dial.
type DialFunc func(ctx context.Context, req DialRequest) (*peer.Conn, error)

// PairFunc asks the host on conn for a pairing token and saves it on the
// gateway. The default is the same flow as `remohop <ID> pair`.
type PairFunc func(ctx context.Context, conn *peer.Conn, name string) error

// PeerInfo is a saved peer as shown in the page (never its token).
type PeerInfo struct {
	Target   string `json:"target"`
	Alias    string `json:"alias,omitempty"`
	Paired   bool   `json:"paired"`
	KeyLogin bool   `json:"key_login,omitempty"`
}

func (s *Server) defaultDial(ctx context.Context, req DialRequest) (*peer.Conn, error) {
	return dial.Dial(ctx, req.Target, dial.Options{
		Password: req.Password,
		Notify:   req.Notify,
		Dir:      s.cfg.ConfigDir,
		// Never prompt: there is no terminal, the page is the prompt.
	})
}

func (s *Server) defaultPair(ctx context.Context, conn *peer.Conn, name string) error {
	info := dial.InfoOf(conn)
	if info == nil {
		return errors.New("this connection was not established by remohop and cannot be paired")
	}
	if name == "" {
		name, _ = os.Hostname()
		name = "web gateway " + name
	}
	p, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: name})
	if err != nil {
		return err
	}
	return dial.SavePairing(info, p, "", s.cfg.ConfigDir)
}

func (s *Server) defaultPeers() ([]PeerInfo, error) {
	st, err := dial.OpenStore(s.cfg.ConfigDir)
	if err != nil {
		return nil, err
	}
	ps, err := st.List()
	if err != nil {
		return nil, err
	}
	out := make([]PeerInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, PeerInfo{Target: p.Target, Alias: p.Alias, Paired: p.Paired(), KeyLogin: p.KeyLogin})
	}
	return out, nil
}

// connInfo describes a live host session to the page.
type connInfo struct {
	Key         string `json:"key"`    // handle used by every other call
	Target      string `json:"target"` // the canonical target
	Path        string `json:"path"`   // lan, direct, punched, relayed (or unknown)
	Relayed     bool   `json:"relayed"`
	Auth        string `json:"auth,omitempty"`
	NewHost     bool   `json:"new_host,omitempty"`
	Fingerprint string `json:"host_fingerprint,omitempty"`
	Paired      bool   `json:"paired,omitempty"`
	Remote      string `json:"remote,omitempty"`
}

func describe(conn *peer.Conn, typed string) connInfo {
	ci := connInfo{Key: typed, Target: typed, Path: "unknown"}
	if info := dial.InfoOf(conn); info != nil {
		if info.Target != "" {
			ci.Key, ci.Target = info.Target, info.Target
		}
		ci.Path = string(info.Path)
		ci.Relayed = info.Path == dial.PathRelayed
		ci.Auth = string(info.Auth)
		ci.NewHost = info.NewHost
		if len(info.HostKey) > 0 {
			ci.Fingerprint = auth.Fingerprint(info.HostKey)
		}
		ci.Remote = info.Remote.String()
	}
	return ci
}

// apiError is the JSON error of every API call.
type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.Status, map[string]any{"error": e})
}

// classify turns any error from a dial or a service call into a stable code
// and text for the page. Text that came from the host is passed through
// Sanitize but otherwise kept verbatim, so a host's refusal reads as the
// host wrote it.
func classify(err error) *apiError {
	if ae, ok := errors.AsType[*apiError](err); ok {
		return ae
	}
	msg := cli.Sanitize(err.Error())
	mk := func(status int, code string) *apiError { return &apiError{Status: status, Code: code, Message: msg} }
	if re, ok := errors.AsType[*peer.RemoteError](err); ok {
		e := mk(http.StatusForbidden, "host_refused")
		e.Message = cli.Sanitize(re.Message)
		if strings.Contains(re.Message, "switched off") {
			e.Code = "feature_off"
		}
		return e
	}
	switch auth.CodeOf(err) {
	case auth.CodeAuthFailed:
		return mk(http.StatusUnauthorized, "bad_password")
	case auth.CodeLockedOut:
		return mk(http.StatusTooManyRequests, "host_locked_out")
	case auth.CodeDenied:
		return mk(http.StatusForbidden, "denied")
	case auth.CodeBusy:
		return mk(http.StatusServiceUnavailable, "host_busy")
	case auth.CodeUnknownToken:
		return mk(http.StatusUnauthorized, "pairing_rejected")
	case auth.CodeUnknownKey:
		return mk(http.StatusUnauthorized, "key_rejected")
	case auth.CodeNoPassword:
		return mk(http.StatusForbidden, "password_login_disabled")
	}
	switch signal.CodeOf(err) {
	case signal.CodeUnknownID:
		return mk(http.StatusNotFound, "unknown_id")
	case signal.CodeOffline:
		return mk(http.StatusBadGateway, "host_offline")
	case signal.CodeNoAnswer, signal.CodeDeclined:
		return mk(http.StatusBadGateway, "host_unreachable")
	case signal.CodeRateLimited:
		return mk(http.StatusTooManyRequests, "rendezvous_rate_limited")
	case signal.CodeIsHost: // the rendezvous setting names a host
		return mk(http.StatusBadGateway, "signal_is_host")
	case signal.CodeWrongProtocol:
		return mk(http.StatusBadGateway, "signal_wrong_protocol")
	}
	switch {
	case errors.Is(err, dial.ErrNoCredential):
		return mk(http.StatusUnauthorized, "password_required")
	case errors.Is(err, dial.ErrHostKeyChanged):
		return mk(http.StatusConflict, "host_key_changed")
	case errors.Is(err, nat.ErrNoPath):
		return mk(http.StatusBadGateway, "no_path")
	case errors.Is(err, context.DeadlineExceeded):
		return mk(http.StatusGatewayTimeout, "timeout")
	case errors.Is(err, context.Canceled):
		return mk(http.StatusBadGateway, "canceled")
	case errors.Is(err, syscall.EISDIR), errors.Is(err, syscall.ENOTDIR):
		return mk(http.StatusBadRequest, "not_a_file")
	case errors.Is(err, syscall.ENOTEMPTY):
		return mk(http.StatusConflict, "not_empty")
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return mk(http.StatusInsufficientStorage, "no_space")
	case errors.Is(err, fs.ErrNotExist):
		return mk(http.StatusNotFound, "not_found")
	case errors.Is(err, fs.ErrPermission):
		return mk(http.StatusForbidden, "permission_denied")
	case errors.Is(err, fs.ErrExist):
		return mk(http.StatusConflict, "exists")
	}
	return mk(http.StatusBadGateway, "remote_error")
}

// attempt is one dial in progress (or finished), polled by the page. Dials
// can take minutes when the host asks its owner for approval, so they are
// not tied to a request.
type attempt struct {
	id     string
	cancel context.CancelFunc

	mu      sync.Mutex
	state   string // dialing, waiting_approval, connected, failed, canceled
	notices []string
	err     *apiError
	conn    *connInfo
	done    time.Time
}

func (a *attempt) finished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.done.IsZero()
}

func (a *attempt) doneAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.done
}

func (a *attempt) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]any{"attempt": a.id, "state": a.state, "notices": append([]string(nil), a.notices...)}
	if a.err != nil {
		m["error"] = a.err
	}
	if a.conn != nil {
		m["conn"] = a.conn
	}
	return m
}

func (a *attempt) notice(msg string) {
	msg = cli.Sanitize(msg)
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.notices) < 20 {
		a.notices = append(a.notices, msg)
	}
	if a.done.IsZero() && strings.Contains(msg, "approval") {
		a.state = "waiting_approval"
	}
}

func (a *attempt) finish(state string, err *apiError, ci *connInfo, now time.Time) {
	a.mu.Lock()
	a.state, a.err, a.conn, a.done = state, err, ci, now
	a.mu.Unlock()
}

type connectReq struct {
	Target   string `json:"target"`
	Password string `json:"password"`
	Pair     bool   `json:"pair"`
	PairName string `json:"pair_name"`
}

const maxAttempts = 4

// startConnect begins a dial for ws.
func (s *Server) startConnect(ws *webSession, req connectReq) (*attempt, *apiError) {
	target := cli.NormalizeTarget(strings.TrimSpace(req.Target))
	if target == "" || len(target) > 255 || strings.ContainsAny(target, "\x00\r\n") {
		return nil, &apiError{Status: http.StatusBadRequest, Code: "bad_target", Message: "enter a 9-digit ID, a saved name, or host:port"}
	}
	if s.cfg.NoSaved && req.Password == "" {
		return nil, &apiError{Status: http.StatusUnauthorized, Code: "password_required",
			Message: "this gateway does not use saved pairings: enter the host's password"}
	}
	release, aerr := s.reserve()
	if aerr != nil {
		return nil, aerr
	}
	ws.mu.Lock()
	running := 0
	for _, a := range ws.attempts {
		if !a.finished() {
			running++
		}
	}
	if ws.closed || running >= maxAttempts {
		ws.mu.Unlock()
		release()
		return nil, &apiError{Status: http.StatusTooManyRequests, Code: "busy", Message: "too many connections in progress"}
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.DialTimeout)
	a := &attempt{id: randToken(9), cancel: cancel, state: "dialing"}
	ws.attempts[a.id] = a
	ws.mu.Unlock()

	password := req.Password
	req.Password = ""
	go func() {
		defer cancel()
		conn, err := s.cfg.Dial(ctx, DialRequest{Target: target, Password: password, Notify: a.notice})
		password = ""
		if err != nil {
			release()
			if ctx.Err() == context.Canceled {
				a.finish("canceled", &apiError{Status: 499, Code: "canceled", Message: "canceled"}, nil, s.now())
				return
			}
			a.finish("failed", classify(err), nil, s.now())
			return
		}
		ci := describe(conn, target)
		if req.Pair {
			pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
			err := s.cfg.Pair(pctx, conn, req.PairName)
			pcancel()
			if err != nil {
				// The session works; only the pairing failed. Say so.
				a.notice("pairing failed: " + err.Error())
			} else {
				ci.Paired = true
			}
		}
		p := &pooled{key: ci.Key, conn: conn, info: ci}
		got, mine := s.adopt(ws, p, release)
		if !mine {
			conn.Close()
			release()
			if got == nil {
				a.finish("canceled", &apiError{Status: 499, Code: "canceled", Message: "the browser session ended"}, nil, s.now())
				return
			}
			ci = got.info
		}
		a.finish("connected", nil, &ci, s.now())
	}()
	return a, nil
}
