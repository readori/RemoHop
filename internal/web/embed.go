package web

import (
	"errors"
	"net"
	"net/url"
	"strings"
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
