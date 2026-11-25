package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// Per-session limits.
//
// A front end that asks its user about every login (the desktop app) lets
// them choose what that one client may do: watch only, or also control
// the mouse and keyboard, or also transfer files. The choice is the
// session's limit, applied on top of the host's own policy: a feature the
// host switched off stays off whatever the limit says. A session with a
// limit can ask its owner for more (ServiceAccess), which is a new
// approval; the owner can also widen or narrow the limit of a live session
// (SetSessionFeatures). Narrowing takes effect at once, like switching a
// feature off: what is open with a withdrawn feature ends.

// ServiceAccess is the service a session uses to ask the host's owner for
// features its limit does not include.
const ServiceAccess = "access"

// accessCooldown is how long a session must wait after its owner refused a
// request before it may ask again: refusing must not be a chore.
const accessCooldown = 10 * time.Second

func init() {
	perm.Register(ServiceAccess, perm.Always)
	peer.Handle(ServiceAccess, serveAccess)
}

// AccessRequest asks for features ("input", "file", ...).
type AccessRequest struct {
	Features []string `json:"features"`
}

// AccessReply answers an AccessRequest. Features is what the session may
// use now; All means it has no limit.
type AccessReply struct {
	Error    string   `json:"error,omitempty"`
	All      bool     `json:"all,omitempty"`
	Features []string `json:"features,omitempty"`
}

func featureSet(fs []perm.Feature) map[perm.Feature]bool {
	m := make(map[perm.Feature]bool, len(fs))
	for _, f := range fs {
		m[f] = true
	}
	return m
}

func featureList(fs []perm.Feature) string {
	s := make([]string, 0, len(fs))
	for _, f := range fs {
		s = append(s, string(f))
	}
	slices.Sort(s)
	return strings.Join(s, ",")
}

// limitedLocked reports whether s's limit leaves out f. host.mu is held.
func (s *Session) limitedLocked(f perm.Feature) bool {
	return s.limit != nil && f != perm.Always && !s.limit[f]
}

func (s *Session) limited(f perm.Feature) bool {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	return s.limitedLocked(f)
}

// Features reports the session's limit: all is true when it has none,
// else allowed lists the features it may use (the host's policy still
// applies on top).
func (s *Session) Features() (all bool, allowed []perm.Feature) {
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	if s.limit == nil {
		return true, nil
	}
	for _, f := range perm.Features() {
		if s.limit[f] {
			allowed = append(allowed, f)
		}
	}
	return false, allowed
}

// SetSessionFeatures replaces the limit of session id: nil features with
// all true lifts it, otherwise the session may use only features. What it
// has open with a feature it loses ends at once.
func (h *Host) SetSessionFeatures(id int, all bool, features []perm.Feature) error {
	h.mu.Lock()
	var sess *Session
	for s := range h.sessions {
		if s.id == id {
			sess = s
			break
		}
	}
	if sess == nil {
		h.mu.Unlock()
		return fmt.Errorf("no session %d", id)
	}
	if all {
		sess.limit = nil
	} else {
		sess.limit = featureSet(features)
	}
	h.mu.Unlock()
	what := "all"
	if !all {
		what = featureList(features)
	}
	h.audit.add("session-features", sess.fields("features", what)...)
	h.PolicyChanged()
	h.notify()
	return nil
}

// serveAccess is ServiceAccess: the session asks for features, the owner
// is asked, and the session's limit grows by what was asked if they agree.
func serveAccess(ctx context.Context, c *peer.Conn, st peer.Stream, raw json.RawMessage) error {
	s := SessionOf(c)
	if s == nil {
		return wire.WriteJSON(st, AccessReply{Error: "not a host session"})
	}
	var req AccessRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return wire.WriteJSON(st, AccessReply{Error: "bad request"})
		}
	}
	reply := func() error {
		all, fs := s.Features()
		r := AccessReply{All: all}
		for _, f := range fs {
			r.Features = append(r.Features, string(f))
		}
		return wire.WriteJSON(st, r)
	}
	var want []perm.Feature
	known := featureSet(perm.Features())
	for _, name := range req.Features {
		f := perm.Feature(name)
		if !known[f] {
			return wire.WriteJSON(st, AccessReply{Error: fmt.Sprintf("unknown feature %q", auth.CleanText(name, 32))})
		}
		if s.limited(f) && !slices.Contains(want, f) {
			want = append(want, f)
		}
	}
	if len(want) == 0 {
		return reply()
	}
	if err := s.host.askAccess(ctx, s, want); err != nil {
		return wire.WriteJSON(st, AccessReply{Error: err.Error()})
	}
	return reply()
}

// askAccess asks the owner whether s may also use want, and widens its
// limit if they agree.
func (h *Host) askAccess(ctx context.Context, s *Session, want []perm.Feature) error {
	h.mu.Lock()
	switch {
	case s.asking:
		h.mu.Unlock()
		return errors.New("a request from this session is already waiting for the host's owner")
	case time.Since(s.denied) < accessCooldown:
		h.mu.Unlock()
		return errors.New("the host's owner just refused; wait a little before asking again")
	}
	s.asking = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		s.asking = false
		h.mu.Unlock()
	}()
	a := &Approval{From: s.Remote, Identity: auth.Fingerprint(s.Auth.ClientKey), Name: s.Auth.TokenName, Device: s.Auth.ClientName,
		Auth: s.Auth.Kind, Action: "access: " + featureList(want), Access: want, Session: s.id, Since: time.Now(), decide: make(chan decision, 1)}
	outcome, _, err := h.wait(ctx, s.conn.Done(), a)
	h.audit.add("approval", s.fields("decision", outcome, "access", featureList(want))...)
	h.log.Info("access request "+outcome, "n", a.N, "session", s.id, "features", featureList(want))
	switch outcome {
	case "allowed":
		h.mu.Lock()
		if s.limit != nil {
			for _, f := range want {
				s.limit[f] = true
			}
		}
		h.mu.Unlock()
		h.PolicyChanged()
		h.notify()
		return nil
	case "denied":
		h.mu.Lock()
		s.denied = time.Now()
		h.mu.Unlock()
		return errors.New("the host's owner refused")
	case "timeout":
		return errors.New("nobody answered at the host")
	}
	return err
}

// RequestAccess asks the host of c for features the session's limit does
// not include, waiting for its owner's answer. It returns what the session
// may use afterwards.
func RequestAccess(ctx context.Context, c *peer.Conn, features []perm.Feature) (*AccessReply, error) {
	req := AccessRequest{}
	for _, f := range features {
		req.Features = append(req.Features, string(f))
	}
	st, err := c.Open(ctx, ServiceAccess, req)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	var r AccessReply
	if err := wire.ReadJSON(st, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return &r, errors.New(r.Error)
	}
	return &r, nil
}
