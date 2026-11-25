// Package localui serves a program's own user interface as a web page on
// 127.0.0.1, for the desktop app (internal/desktop), whose native window
// or browser window shows it.
//
// # Security model
//
// It is the model of quick support's page (internal/quick) and of
// internal/web's loopback mode. The server listens on 127.0.0.1 only;
// every URL starts with a 128-bit random token, which is the login; the
// Host header must name the loopback address (DNS rebinding); a request
// that says where it comes from (Origin, Sec-Fetch-Site) must come from
// the page itself; and everything that changes state is a JSON POST
// carrying a second secret (the CSRF token) that the page obtains over
// that same checked channel. Responses forbid framing, caching and
// third-party resources (Content-Security-Policy). The page is never
// reachable from another machine.
package localui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HeaderCSRF carries the CSRF token on every state-changing request.
const HeaderCSRF = "X-RemoHop-CSRF"

// Server is the local UI server.
type Server struct {
	log   *slog.Logger
	token string
	csrf  string
	port  string
	ln    net.Listener
	srv   *http.Server
	mux   *http.ServeMux

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	frameSrc  string
	viewers   int
	seen      bool
	lastGone  time.Time
	presenceC chan struct{}
}

// New listens on a free loopback port and serves the files of assets:
// index.html at the root, the others under assets/.
func New(assets fs.FS, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		ln.Close()
		return nil, errors.New("localui: the listener is not on loopback")
	}
	s := &Server{log: log, token: RandToken(16), csrf: RandToken(16), port: strconv.Itoa(tcp.Port), ln: ln,
		mux: http.NewServeMux(), presenceC: make(chan struct{}, 1)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context { return s.ctx }}
	files := http.FileServerFS(assets)
	page := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := fs.ReadFile(assets, name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(b)
		}
	}
	s.mux.HandleFunc("GET /{$}", page("index.html"))
	// Further pages (overlay.html): any .html file of assets.
	s.mux.HandleFunc("GET /{page}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("page")
		if !strings.HasSuffix(name, ".html") || name == "index.html" {
			http.NotFound(w, r)
			return
		}
		page(name)(w, r)
	})
	s.mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("name")
		files.ServeHTTP(w, r2)
	})
	return s, nil
}

// RandToken returns n random bytes, base64url-encoded.
func RandToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("localui: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func ctEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// URL is the page's address, with the token.
func (s *Server) URL() string { return "http://127.0.0.1:" + s.port + "/" + s.token + "/" }

// CSRF is the token state-changing requests must carry (handed to the page
// by /api/hello).
func (s *Server) CSRF() string { return s.csrf }

// Serve runs until Close.
func (s *Server) Serve() error {
	if err := s.srv.Serve(s.ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops the server and ends the event streams.
func (s *Server) Close() {
	s.cancel()
	s.srv.Close()
}

func (s *Server) hostOK(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port != s.port {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Path != "" && u.Path != "/" || u.User != nil {
		return false
	}
	return u.Scheme == "http" && strings.EqualFold(u.Host, r.Host)
}

// fromPage refuses requests a browser marks as coming from elsewhere. A
// request with neither Origin nor Sec-Fetch-Site comes from a program that
// already holds the token (a second start of the app asking the first to
// show its window).
func fromPage(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" && !sameOrigin(r) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin":
		return true
	}
	return false
}

// SetFrameSrc lets the page show origin (one http://127.0.0.1:PORT
// origin: the desktop app's gateway) in frames. Empty allows no frames.
func (s *Server) SetFrameSrc(origin string) {
	s.mu.Lock()
	s.frameSrc = origin
	s.mu.Unlock()
}

func (s *Server) setHeaders(w http.ResponseWriter) {
	s.mu.Lock()
	frames := s.frameSrc
	s.mu.Unlock()
	src := "'none'"
	if frames != "" {
		src = frames
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; frame-src "+src+"; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
}

// ServeHTTP checks the Host header and the token, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostOK(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.setHeaders(w)
	rest, ok := strings.CutPrefix(r.URL.Path, "/")
	tok, sub, slash := strings.Cut(rest, "/")
	if !ok || !ctEqual(tok, s.token) {
		http.NotFound(w, r)
		return
	}
	if !slash {
		http.Redirect(w, r, "/"+tok+"/", http.StatusTemporaryRedirect)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + sub
	r2.URL.RawPath = ""
	s.mux.ServeHTTP(w, r2)
}

// Error is a refusal the page explains in its own language: Code names it,
// Message is the English text.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf makes a refusal with code (HTTP 409).
func Errorf(code, msg string) *Error {
	return &Error{Status: http.StatusConflict, Code: code, Message: msg}
}

// WriteJSON writes v with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *Error) { WriteJSON(w, e.Status, map[string]any{"error": e}) }

// Get registers a same-origin GET handler.
func (s *Server) Get(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
		if !fromPage(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Code: "bad_origin", Message: "cross-site request refused"})
			return
		}
		h(w, r)
	})
}

// Post registers a same-origin JSON POST handler; with csrf it must carry
// the CSRF token. The body (at most 16 KiB) is passed raw. A nil result is
// {"ok": true}; an error is HTTP 409 with its code ("failed" unless it is
// an *Error).
func (s *Server) Post(pattern string, csrf bool, h func(r *http.Request, body json.RawMessage) (any, error)) {
	s.mux.HandleFunc("POST "+pattern, func(w http.ResponseWriter, r *http.Request) {
		if !fromPage(r) {
			writeErr(w, &Error{Status: http.StatusForbidden, Code: "bad_origin", Message: "cross-site request refused"})
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeErr(w, &Error{Status: http.StatusUnsupportedMediaType, Code: "bad_content_type", Message: "send application/json"})
			return
		}
		if csrf && !ctEqual(r.Header.Get(HeaderCSRF), s.csrf) {
			writeErr(w, &Error{Status: http.StatusForbidden, Code: "bad_csrf", Message: "missing or wrong CSRF token"})
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
		if err != nil {
			writeErr(w, &Error{Status: http.StatusRequestEntityTooLarge, Code: "bad_request", Message: "request too large"})
			return
		}
		if len(body) > 0 && !json.Valid(body) {
			writeErr(w, &Error{Status: http.StatusBadRequest, Code: "bad_request", Message: "bad JSON body"})
			return
		}
		out, err := h(r, body)
		if err != nil {
			e, ok := errors.AsType[*Error](err)
			if !ok {
				e = &Error{Status: http.StatusConflict, Code: "failed", Message: err.Error()}
			}
			writeErr(w, e)
			return
		}
		if out == nil {
			out = map[string]any{"ok": true}
		}
		WriteJSON(w, http.StatusOK, out)
	})
}

// Hello registers /api/hello, the one POST without the CSRF token: it
// answers with extra()'s result plus the token as "csrf". Call it once.
func (s *Server) Hello(extra func() map[string]any) {
	s.Post("/api/hello", false, func(*http.Request, json.RawMessage) (any, error) {
		m := extra()
		if m == nil {
			m = map[string]any{}
		}
		m["csrf"] = s.csrf
		return m, nil
	})
}

// Events streams state() as server-sent events: at the start, whenever
// changed receives, and every half second if it differs (countdowns move).
// An open stream counts as a page on screen (see Presence).
func (s *Server) Events(pattern string, state func() any, subscribe func() (<-chan struct{}, func())) {
	s.Get(pattern, func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		changed, cancel := subscribe()
		defer cancel()
		s.viewer(+1)
		defer s.viewer(-1)
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		keep := time.NewTicker(15 * time.Second)
		defer keep.Stop()
		var last []byte
		for {
			b, _ := json.Marshal(state())
			if string(b) != string(last) {
				if _, err := w.Write([]byte("data: " + string(b) + "\n\n")); err != nil {
					return
				}
				fl.Flush()
				last = b
			}
			select {
			case <-r.Context().Done():
				return
			case <-changed:
			case <-tick.C:
			case <-keep.C:
				if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
					return
				}
				fl.Flush()
			}
		}
	})
}

func (s *Server) viewer(delta int) {
	s.mu.Lock()
	s.viewers += delta
	if delta > 0 {
		s.seen = true
	} else if s.viewers == 0 {
		s.lastGone = time.Now()
	}
	s.mu.Unlock()
	select {
	case s.presenceC <- struct{}{}:
	default:
	}
}

// Presence says whether a page shows the UI: how many event streams are
// open, whether one ever was, and when the last one closed.
func (s *Server) Presence() (viewers int, seen bool, gone time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewers, s.seen, s.lastGone
}

// PresenceChanged receives when Presence may have changed.
func (s *Server) PresenceChanged() <-chan struct{} { return s.presenceC }
