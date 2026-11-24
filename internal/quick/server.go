package quick

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
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

//go:embed assets
var assetFS embed.FS

// Controller is what the UI server shows and drives; *App implements it.
type Controller interface {
	State() State
	Subscribe() (changed <-chan struct{}, cancel func())
	Regenerate() error
	Decide(n int, allow bool) error
	Disconnect(id int) error
	SetOption(name string, on bool) error
	Configure(server string) error
	Stop()
}

// Server is the local UI server. See the package comment for its security
// model.
type Server struct {
	ctl   Controller
	log   *slog.Logger
	token string // path prefix: the login
	csrf  string // header on every state-changing request
	port  string
	ln    net.Listener
	srv   *http.Server
	mux   *http.ServeMux

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	lang      string            // the page's language: "zh", "en" or "" (the browser's)
	onLang    func(lang string) // see OnLang
	viewers   int               // pages with an open event stream
	seen      bool              // a page has connected at least once
	lastGone  time.Time         // when viewers last dropped to zero
	byeAt     time.Time         // a page said it is closing (zero: not since a page connected)
	presenceC chan struct{}
}

// NewServer listens on a free loopback port. lang is the language the page
// starts in ("zh", "en", or "" to let the browser's language decide).
func NewServer(ctl Controller, lang string, log *slog.Logger) (*Server, error) {
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
		return nil, errors.New("quick: the UI listener is not on loopback")
	}
	s := &Server{ctl: ctl, log: log, lang: lang, token: randToken(16), csrf: randToken(16),
		port: strconv.Itoa(tcp.Port), ln: ln, presenceC: make(chan struct{}, 1)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mux = s.routes()
	s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context { return s.ctx }}
	return s, nil
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("quick: no randomness: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func ctEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// OnLang sets what runs when the user picks a language in the page: the
// caller saves the choice for the next start and uses it for the native
// dialogs. Set it before Serve.
func (s *Server) OnLang(fn func(lang string)) {
	s.mu.Lock()
	s.onLang = fn
	s.mu.Unlock()
}

// URL is the page's address. It contains the access token.
func (s *Server) URL() string { return "http://127.0.0.1:" + s.port + "/" + s.token + "/" }

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

// hostOK accepts only the names of the loopback address with our port. A
// hostile name that resolves to 127.0.0.1 (DNS rebinding) arrives with
// that name in Host and is refused.
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

// sameOrigin reports whether the Origin header names this very server.
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

// fromPage refuses API requests a browser marks as coming from elsewhere.
// Browsers send Origin on every POST and Sec-Fetch-Site on every request
// (all current ones); a request with neither comes from a program that
// already holds the token.
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

func setHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
}

// ServeHTTP checks the Host header and the token prefix, then routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostOK(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	setHeaders(w)
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

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.Status, map[string]any{"error": e})
}

var (
	errOrigin      = &apiError{Status: http.StatusForbidden, Code: "bad_origin", Message: "cross-site request refused"}
	errCSRF        = &apiError{Status: http.StatusForbidden, Code: "bad_csrf", Message: "missing or wrong CSRF token"}
	errContentType = &apiError{Status: http.StatusUnsupportedMediaType, Code: "bad_content_type", Message: "send application/json"}
)

func (s *Server) routes() *http.ServeMux {
	m := http.NewServeMux()
	assets, _ := fs.Sub(assetFS, "assets")
	files := http.FileServerFS(assets)
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := fs.ReadFile(assets, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	m.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("name")
		files.ServeHTTP(w, r2)
	})

	// read: same-origin GETs. post: same-origin JSON POSTs; all but hello
	// (which hands out the CSRF token) must carry that token.
	read := func(pattern string, h http.HandlerFunc) {
		m.HandleFunc("GET "+pattern, func(w http.ResponseWriter, r *http.Request) {
			if !fromPage(r) {
				writeErr(w, errOrigin)
				return
			}
			h(w, r)
		})
	}
	type body struct {
		N      int    `json:"n"`
		ID     int    `json:"id"`
		Allow  bool   `json:"allow"`
		Name   string `json:"name"`
		On     bool   `json:"on"`
		Server string `json:"server"`
		Lang   string `json:"lang"`
	}
	post := func(pattern string, csrf bool, h func(b body) (any, error)) {
		m.HandleFunc("POST "+pattern, func(w http.ResponseWriter, r *http.Request) {
			if !fromPage(r) {
				writeErr(w, errOrigin)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeErr(w, errContentType)
				return
			}
			if csrf && !ctEqual(r.Header.Get(hdrCSRF), s.csrf) {
				writeErr(w, errCSRF)
				return
			}
			var b body
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			if err := dec.Decode(&b); err != nil && !errors.Is(err, io.EOF) {
				writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "bad JSON body"})
				return
			}
			out, err := h(b)
			if err != nil {
				code := "failed"
				if ce, ok := errors.AsType[*codedError](err); ok {
					code = ce.code
				}
				writeErr(w, &apiError{Status: http.StatusConflict, Code: code, Message: err.Error()})
				return
			}
			if out == nil {
				out = map[string]any{"ok": true}
			}
			writeJSON(w, http.StatusOK, out)
		})
	}

	post("/api/hello", false, func(body) (any, error) {
		s.mu.Lock()
		lang := s.lang
		s.mu.Unlock()
		return map[string]any{"csrf": s.csrf, "lang": lang, "state": s.ctl.State()}, nil
	})
	post("/api/lang", true, func(b body) (any, error) {
		lang := normLang(b.Lang)
		if lang == "" {
			return nil, &codedError{code: "bad_request", msg: "lang is zh or en"}
		}
		s.mu.Lock()
		s.lang = lang
		fn := s.onLang
		s.mu.Unlock()
		if fn != nil {
			fn(lang)
		}
		return nil, nil
	})
	read("/api/state", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, s.ctl.State()) })
	read("/api/events", s.handleEvents)
	post("/api/regenerate", true, func(body) (any, error) { return nil, s.ctl.Regenerate() })
	post("/api/decide", true, func(b body) (any, error) { return nil, s.ctl.Decide(b.N, b.Allow) })
	post("/api/disconnect", true, func(b body) (any, error) { return nil, s.ctl.Disconnect(b.ID) })
	post("/api/option", true, func(b body) (any, error) { return nil, s.ctl.SetOption(b.Name, b.On) })
	post("/api/configure", true, func(b body) (any, error) { return nil, s.ctl.Configure(strings.TrimSpace(b.Server)) })
	post("/api/stop", true, func(body) (any, error) {
		// Answer first: stopping closes sessions and can take a moment.
		go s.ctl.Stop()
		return nil, nil
	})
	post("/api/bye", true, func(body) (any, error) {
		s.mu.Lock()
		s.byeAt = time.Now()
		s.mu.Unlock()
		s.presenceChanged()
		return nil, nil
	})
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "no such API call"})
	})
	return m
}

const hdrCSRF = "X-RemoHop-CSRF"

// handleEvents streams the state as server-sent events: once at the start
// and whenever it changes. The open stream is also how the process knows
// that a page is showing (see Presence).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	changed, cancel := s.ctl.Subscribe()
	defer cancel()
	s.viewer(+1)
	defer s.viewer(-1)

	// Countdowns and durations in the state move every second, so the
	// state is also re-read on a timer; unchanged states are not sent.
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	var last []byte
	for {
		b, _ := json.Marshal(s.ctl.State())
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
}

func (s *Server) viewer(delta int) {
	s.mu.Lock()
	s.viewers += delta
	if delta > 0 {
		s.seen = true
		s.byeAt = time.Time{}
	} else if s.viewers == 0 {
		s.lastGone = time.Now()
	}
	s.mu.Unlock()
	s.presenceChanged()
}

func (s *Server) presenceChanged() {
	select {
	case s.presenceC <- struct{}{}:
	default:
	}
}

// Presence says whether the UI is on screen.
type Presence struct {
	// Viewers is the number of pages showing the UI now.
	Viewers int
	// Seen: a page has shown the UI at some point.
	Seen bool
	// Gone is when the last page went away (meaningful when Viewers is 0
	// and Seen).
	Gone time.Time
	// Bye: the last page announced that it was being closed, as opposed to
	// vanishing (a crashed or suspended browser).
	Bye bool
}

// Presence reports whether a page is showing the UI.
func (s *Server) Presence() Presence {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Presence{Viewers: s.viewers, Seen: s.seen, Gone: s.lastGone, Bye: !s.byeAt.IsZero()}
}

// PresenceChanged receives when Presence may have changed.
func (s *Server) PresenceChanged() <-chan struct{} { return s.presenceC }
