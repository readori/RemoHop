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
//	POST peers/password {target}         the saved password, to show it
//	POST peers/password {target, forget} removes the saved password

// errNoSuchPeer: the target is not a saved host of this gateway.
var errNoSuchPeer = errors.New("no saved host with that target")

type peerReq struct {
	Target string `json:"target"`
	Alias  string `json:"alias"`
	Forget bool   `json:"forget"`
}

func (s *Server) defaultSavePassword(target, password string) error {
	st, err := dial.OpenStore(s.cfg.ConfigDir)
	if err != nil {
		return err
	}
	return st.SetPassword(target, password)
}

func (s *Server) defaultPassword(target string) (string, error) {
	st, err := dial.OpenStore(s.cfg.ConfigDir)
	if err != nil {
		return "", err
	}
	p, ok, err := st.Get(target)
	if err != nil {
		return "", err
	}
	if !ok || !p.SavedPassword() {
		return "", errNoSuchPeer
	}
	return p.Password, nil
}

// handlePeerPassword shows or removes a saved host's saved password.
// Showing it is no more than the page can already do with it (log in to
// that host), and it lets the user check what was saved.
func (s *Server) handlePeerPassword(w http.ResponseWriter, r *http.Request, ws *webSession) {
	req, ok := s.readPeerReq(w, r)
	if !ok {
		return
	}
	if req.Forget {
		if err := s.cfg.SavePassword(req.Target, ""); err != nil {
			writeErr(w, classify(err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	pw, err := s.cfg.Password(req.Target)
	if errors.Is(err, errNoSuchPeer) {
		writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "no saved password for that host"})
		return
	}
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"password": pw})
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
