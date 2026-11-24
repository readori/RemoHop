package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
)

// The saved hosts of this gateway, as the page edits them: a name (alias)
// for a host, or forgetting it. Both work for paired hosts and for recent
// ones (only the host key pinned). A gateway with saved hosts off
// (NoSaved, a shared gateway) refuses them, as it lists none.
//
//	POST peers/alias   {target, alias}  ("" removes the name)
//	POST peers/forget  {target}         (also ends the sessions to it)

// errNoSuchPeer: the target is not a saved host of this gateway.
var errNoSuchPeer = errors.New("no saved host with that target")

type peerReq struct {
	Target string `json:"target"`
	Alias  string `json:"alias"`
}

func (s *Server) defaultSetAlias(target, alias string) error {
	st, err := dial.OpenStore(s.cfg.ConfigDir)
	if err != nil {
		return err
	}
	// The page renames hosts it lists; it does not add any.
	if _, ok, err := st.Get(target); err != nil {
		return err
	} else if !ok {
		return errNoSuchPeer
	}
	return st.SetAlias(target, alias)
}

func (s *Server) defaultForget(target string) (int, error) {
	st, err := dial.OpenStore(s.cfg.ConfigDir)
	if err != nil {
		return 0, err
	}
	return st.Forget(target)
}

func (s *Server) readPeerReq(w http.ResponseWriter, r *http.Request) (peerReq, bool) {
	var req peerReq
	if s.cfg.NoSaved {
		writeErr(w, &apiError{Status: http.StatusForbidden, Code: "feature_off", Message: "saved hosts are off on this gateway"})
		return req, false
	}
	if aerr := readJSON(w, r, &req, 4096); aerr != nil {
		writeErr(w, aerr)
		return req, false
	}
	req.Target = cli.NormalizeTarget(strings.TrimSpace(req.Target))
	req.Alias = strings.TrimSpace(req.Alias)
	if req.Target == "" {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "missing target"})
		return req, false
	}
	return req, true
}

func (s *Server) handlePeerAlias(w http.ResponseWriter, r *http.Request, ws *webSession) {
	req, ok := s.readPeerReq(w, r)
	if !ok {
		return
	}
	if req.Alias != "" {
		if err := dial.CheckAlias(req.Alias); err != nil {
			writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_alias", Message: cli.Sanitize(err.Error())})
			return
		}
	}
	if err := s.cfg.SetAlias(req.Target, req.Alias); err != nil {
		if errors.Is(err, errNoSuchPeer) {
			writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: err.Error()})
			return
		}
		writeErr(w, classify(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target": req.Target, "alias": req.Alias})
}

func (s *Server) handlePeerForget(w http.ResponseWriter, r *http.Request, ws *webSession) {
	req, ok := s.readPeerReq(w, r)
	if !ok {
		return
	}
	n, err := s.cfg.Forget(req.Target)
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	// Its sessions end too: they would otherwise reconnect with the
	// credentials the user just deleted.
	s.DisconnectTarget(req.Target)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": n})
}
