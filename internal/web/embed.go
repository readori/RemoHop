package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
)

// The desktop app (internal/desktop) shows each host it is connected to
// in a tab of its own window: an iframe of this gateway's page, which the
// app's own page (another loopback origin) frames. Config.FrameAncestor
// names that one origin; every other page stays unframeable.

// checkFrameAncestor accepts only a loopback http origin with a port.
func checkFrameAncestor(origin string) error {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("web: the frame ancestor must be an http://127.0.0.1:PORT origin")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" || (host != "127.0.0.1" && host != "localhost") {
		return errors.New("web: the frame ancestor must be an http://127.0.0.1:PORT origin")
	}
	return nil
}

// frameAncestors is the CSP frame-ancestors source list.
func (s *Server) frameAncestors() string {
	if s.cfg.FrameAncestor == "" {
		return "'none'"
	}
	return strings.TrimSuffix(s.cfg.FrameAncestor, "/")
}

// handleAccess asks the host for features its owner did not give this
// session (the desktop app lets a new device only watch): the viewer's
// "Request control". It waits for the owner's answer.
//
//	POST access {c, features: ["input"|"file"|"clip"]} -> {all, features}
func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		C        string   `json:"c"`
		Features []string `json:"features"`
	}
	if aerr := readJSON(w, r, &req, 4096); aerr != nil {
		writeErr(w, aerr)
		return
	}
	var fs []perm.Feature
	for _, f := range req.Features {
		switch perm.Feature(f) {
		case perm.Input, perm.File, perm.Clip:
			fs = append(fs, perm.Feature(f))
		default:
			writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "features are input, file or clip"})
			return
		}
	}
	if len(fs) == 0 {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "no features"})
		return
	}
	p, release, ok := s.conn(w, ws, req.C)
	if !ok {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	rep, err := hostd.RequestAccess(ctx, p.current(), fs)
	if err != nil {
		writeErr(w, &apiError{Status: http.StatusConflict, Code: "access_refused", Message: cli.Sanitize(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
