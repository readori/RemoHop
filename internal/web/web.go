// Package web is the RemoHop web gateway: `remohop web` runs on a machine the
// user controls, speaks the normal P2P session to a remote host, and serves
// a browser UI (desktop, terminal, files, clipboard, info).
//
// A browser cannot hole-punch or speak our QUIC session, so the gateway is
// the client. The session between gateway and host is end-to-end encrypted;
// the browser talks to the gateway, which therefore sees everything: screen,
// keystrokes, files, clipboard and the passwords typed into the page.
// Exposing the gateway beyond localhost requires TLS and its own login.
//
// # Security model
//
// Loopback (the default): the gateway listens on 127.0.0.1 only and every
// URL starts with a 128-bit random token, which is the login. Host headers
// must name the loopback address (DNS rebinding), Origin must be the
// gateway's own for WebSocket upgrades and, when present, for every other
// request (cross-site WebSocket hijacking and CSRF).
//
// Password mode (any non-loopback --listen, or --password-file): TLS and a
// gateway password are mandatory for non-loopback listening. Login is
// rate-limited with lockout; the session cookie is HttpOnly, SameSite=Strict
// and Secure; Host must be a name the certificate (or the listen address)
// vouches for.
//
// In both modes every state-changing request carries the per-session CSRF
// token in a header, and WebSockets and downloads authenticate with
// single-use tickets bought through such a request.
//
// Programs: with --api-token-file (and a password), a client may instead
// send "Authorization: Bearer <token>" on every request. That mode is off
// unless asked for. A browser never adds the header by itself, but a
// reverse proxy may inject it into all traffic, browsers' included, so a
// bearer request that carries browser signals (Origin, Sec-Fetch-*,
// cookies) must still be same-origin and, if it changes state, carry the
// CSRF token; a bearer WebSocket or download with an Origin must be
// same-origin.
//
// # API
//
// JSON under /api/v1/ (relative to the token path on loopback):
//
//	POST   session            create or resume the browser session {sid?} -> {sid, csrf, ...}
//	POST   login              {password} -> session (password mode)
//	POST   logout
//	GET    connections        live host sessions (also the page heartbeat)
//	GET    peers              saved peers
//	POST   peers/alias        {target, alias} name a saved host ("" removes the name)
//	POST   peers/forget       {target} forget a saved host (and end its sessions)
//	POST   connect            {target, password, pair, pair_name} -> {attempt}
//	GET    connect/{id}       attempt state: dialing, waiting_approval, connected, failed
//	DELETE connect/{id}       cancel
//	POST   disconnect         {c}
//	POST   ticket             {purpose: ws|dl} -> {ticket}
//	GET    files/list?c&path
//	GET    files/download?t&c&path
//	POST   files/upload?c&dir&name&overwrite   (raw body)
//	POST   files/mkdir|rename|delete
//	POST   clip/get|clip/set
//	POST   access            {c, features} ask the host's owner for more (embed.go)
//	GET    info?c            ; GET screenshot?c&display
//	GET    term/sessions?c   ; POST term/kill
//	WS     term/ws?t&c&session&new&rows&cols
//	WS     desktop/ws?t&c&...
package web

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/svc/desktop"
)

//go:embed assets
var assetFS embed.FS

// Version is shown by the page.
var Version = "dev"

// Config configures a Server. The zero value is a loopback gateway.
type Config struct {
	// Listen is the TCP address; default "127.0.0.1:0".
	Listen string
	// TLSCert and TLSKey are PEM files; both or neither.
	TLSCert, TLSKey string
	// PasswordFile holds the gateway password (at least 8 characters).
	PasswordFile string
	// APITokenFile, if set, enables program access: a request may
	// authenticate with "Authorization: Bearer <token>", the token read
	// from this file, instead of logging in. Requires PasswordFile. It may
	// be the password file itself (the gateway's behaviour before the
	// mode had to be enabled), but a separate token keeps an injected
	// program credential from also being the browser login.
	APITokenFile string
	// Target pre-fills the connect form.
	Target string
	// ConfigDir is the remohop config directory (identity, saved peers);
	// empty means the default.
	ConfigDir string
	// NoSaved hides saved peers and never uses saved pairings: users type
	// the host's password every time. Meant for shared gateways.
	NoSaved bool

	// PublicURL is the https:// address browsers use when a TLS-terminating
	// reverse proxy or tunnel (for example Cloudflare Tunnel) forwards to this
	// loopback gateway. Its host is accepted in the Host and Origin headers, as
	// https. Requires a loopback Listen and PasswordFile.
	PublicURL string
	// RealIPHeader names the request header in which the proxy or tunnel
	// in front of PublicURL puts the visitor's address (CF-Connecting-IP
	// for Cloudflare; for X-Forwarded-For the last address is taken). The
	// login rate limiter keys on it for requests through PublicURL. Empty:
	// such requests all count as one source, the proxy. Only name a header
	// the proxy always sets or overwrites: a client can send any header.
	RealIPHeader string

	// Persistent is app mode, for the phone apps' in-process gateway (see
	// keep.go): one user, whose host sessions last until they disconnect.
	// Nothing is closed for being idle (PageIdle and ConnIdle do not
	// apply), a session that drops is established again on its own, and a
	// new browser session takes over the host sessions of earlier ones.
	// Loopback only.
	Persistent bool
	// Redial, in persistent mode, is asked after each connect how to log
	// in to that host again without the user. Nil re-runs Dial with the
	// same target and the password typed for it (kept in memory), or with
	// the saved pairing if the connect paired.
	Redial RedialFunc
	// Restore, in persistent mode, makes the gateway keep the list of its
	// open host sessions in ConfigDir and log in to those hosts again when
	// it starts (an app the system ended while it was in the background;
	// see restore.go). It is asked, for each, how to log in with what is
	// saved on this device, and returns nil when nothing is (that session
	// is not restored). Nil: nothing is kept or restored.
	Restore func(target string) Redialer
	// FrameAncestor is the one origin allowed to show the page in a frame
	// (the desktop app's own page, http://127.0.0.1:PORT; see embed.go).
	// Empty: the page cannot be framed. Loopback only.
	FrameAncestor string

	// Seams for tests; the defaults use internal/dial.
	Dial  DialFunc
	Pair  PairFunc
	Peers func() ([]PeerInfo, error)
	// SetAlias names a saved host ("" removes the name); Forget removes
	// one. The defaults edit the saved-peers file in ConfigDir.
	SetAlias func(target, alias string) error
	Forget   func(target string) (int, error)
	// SavePassword saves a saved host's password ("" removes it) and
	// Password reads it back for the page to show. The defaults use the
	// saved-peers file in ConfigDir.
	SavePassword func(target, password string) error
	Password     func(target string) (string, error)

	// Limits.
	MaxSessions        int           // concurrent host sessions, default 16
	MaxBrowserSessions int           // default 64
	DialTimeout        time.Duration // default 4m (hosts may wait for approval)
	ConnIdle           time.Duration // pooled session unused for this long is closed, default 30m
	PageIdle           time.Duration // loopback: no page heartbeat for this long closes the session, default 15m (a laptop's nap or a paused background tab survives)
	SessionIdle        time.Duration // password mode: idle logout, default 30m
	SessionTTL         time.Duration // password mode: absolute lifetime, default 12h

	Now    func() time.Time
	Logger *slog.Logger
}

// Server is a running (or startable) gateway.
type Server struct {
	cfg Config
	log *slog.Logger

	pwHash [sha256.Size]byte
	havePW bool
	// apiHash is the bearer token's (haveAPI: --api-token-file given).
	apiHash [sha256.Size]byte
	haveAPI bool
	useTLS  bool
	leaf    *x509.Certificate // the TLS certificate, for the Host check
	public  *hostMatcher      // PublicURL's host, or nil
	apiSID  string            // the session shared by bearer-authenticated clients
	pubURL  string
	lim     *limiter

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	srv    *http.Server
	mux    *http.ServeMux

	mu       sync.Mutex
	sessions map[string]*webSession
	tickets  map[string]ticket
	slots    int

	// The listener and what depends on it (see listener.go). bind is read
	// by every request; lnMu serialises changing it.
	bind      atomic.Pointer[binding]
	lnMu      sync.Mutex
	lnChanged chan struct{} // closed when bind is replaced
	lnErr     error         // why the current listener stopped serving
}

func (s *Server) now() time.Time { return s.cfg.Now() }

func (s *Server) passwordMode() bool { return s.havePW }

// New validates cfg and listens. It refuses a non-loopback address without
// TLS and a password.
func New(cfg Config) (*Server, error) {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, errors.New("--tls-cert and --tls-key must be given together")
	}
	loop, err := isLoopbackAddr(cfg.Listen)
	if err != nil {
		return nil, err
	}
	if !loop && (cfg.TLSCert == "" || cfg.PasswordFile == "") {
		return nil, errNoTLSPassword
	}
	if cfg.Persistent && cfg.PasswordFile != "" {
		return nil, errors.New("web: persistent (app) mode is for a loopback gateway without a login")
	}
	if cfg.FrameAncestor != "" {
		if !loop || cfg.PasswordFile != "" {
			return nil, errors.New("web: a frame ancestor is for a loopback gateway without a login (the desktop app)")
		}
		if err := checkFrameAncestor(cfg.FrameAncestor); err != nil {
			return nil, err
		}
	}
	if cfg.APITokenFile != "" && cfg.PasswordFile == "" {
		return nil, errors.New("--api-token-file needs --password-file: program access is for gateways that have a login")
	}
	var pub *hostMatcher
	if cfg.PublicURL != "" {
		if !loop {
			return nil, errors.New("--public-url needs a loopback --listen address: the proxy or tunnel connects to it locally")
		}
		if cfg.PasswordFile == "" {
			return nil, errors.New("--public-url needs --password-file: visitors from the public address must log in")
		}
		if pub, err = parsePublicURL(cfg.PublicURL); err != nil {
			return nil, err
		}
	}
	if err := checkHeaderName(cfg.RealIPHeader); err != nil {
		return nil, err
	} else if cfg.RealIPHeader != "" && cfg.PublicURL == "" {
		return nil, errors.New("--real-ip-header needs --public-url: it is read only from requests through the public address")
	}
	s := &Server{cfg: cfg, sessions: map[string]*webSession{}, tickets: map[string]ticket{}}
	if s.cfg.Now == nil {
		s.cfg.Now = time.Now
	}
	if s.cfg.Logger == nil {
		s.cfg.Logger = slog.Default()
	}
	s.log = s.cfg.Logger
	def := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	if s.cfg.MaxSessions == 0 {
		s.cfg.MaxSessions = 16
	}
	if s.cfg.MaxBrowserSessions == 0 {
		s.cfg.MaxBrowserSessions = 64
	}
	def(&s.cfg.DialTimeout, 4*time.Minute)
	def(&s.cfg.ConnIdle, 30*time.Minute)
	def(&s.cfg.PageIdle, 15*time.Minute)
	def(&s.cfg.SessionIdle, 30*time.Minute)
	def(&s.cfg.SessionTTL, 12*time.Hour)
	if s.cfg.Dial == nil {
		s.cfg.Dial = s.defaultDial
	}
	if s.cfg.Pair == nil {
		s.cfg.Pair = s.defaultPair
	}
	if s.cfg.Peers == nil {
		s.cfg.Peers = s.defaultPeers
	}
	if s.cfg.SetAlias == nil {
		s.cfg.SetAlias = s.defaultSetAlias
	}
	if s.cfg.Forget == nil {
		s.cfg.Forget = s.defaultForget
	}
	if s.cfg.SavePassword == nil {
		s.cfg.SavePassword = s.defaultSavePassword
	}
	if s.cfg.Password == nil {
		s.cfg.Password = s.defaultPassword
	}
	s.lim = newLimiter(s.cfg.Now)

	var tlsConf *tls.Config
	var leaf *x509.Certificate
	if cfg.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("loading TLS certificate: %w", err)
		}
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, fmt.Errorf("parsing TLS certificate: %w", err)
		}
		tlsConf = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		s.useTLS = true
	}
	if cfg.PasswordFile != "" {
		pw, err := loadPassword(cfg.PasswordFile)
		if err != nil {
			return nil, err
		}
		s.pwHash = sha256.Sum256(pw)
		s.havePW = true
		if cfg.APITokenFile != "" {
			tok, err := loadSecret(cfg.APITokenFile, "the API token")
			if err != nil {
				return nil, err
			}
			s.apiHash = sha256.Sum256(tok)
			s.haveAPI = true
		}
	}
	token := ""
	if !s.havePW {
		token = randToken(16) // loopback mode: the URL's path prefix
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		ln.Close()
		return nil, errors.New("web: not a TCP listener")
	}
	if !tcp.IP.IsLoopback() && !s.useTLS || !tcp.IP.IsLoopback() && !s.havePW {
		ln.Close()
		return nil, errNoTLSPassword
	}
	s.leaf = leaf
	s.bind.Store(s.newBinding(ln, tcp, token))
	s.lnChanged = make(chan struct{})
	if pub != nil {
		s.public = pub
		s.pubURL = strings.TrimSuffix(cfg.PublicURL, "/") + "/"
	}

	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mux = s.routes()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.reapLoop(s.ctx) }()
	s.restore()
	s.srv = &http.Server{Handler: s, TLSConfig: tlsConf, ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10, ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelDebug)}
	return s, nil
}

// Addr is the listen address.
func (s *Server) Addr() net.Addr { return s.bind.Load().addr }

// URL is what the user opens. On loopback it contains the access token.
func (s *Server) URL() string {
	if s.pubURL != "" {
		return s.pubURL
	}
	b := s.bind.Load()
	tcp := b.addr
	scheme := "http"
	if s.useTLS {
		scheme = "https"
	}
	host := tcp.IP.String()
	if tcp.IP.IsUnspecified() {
		host = "localhost"
	}
	u := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(tcp.Port)) + "/"
	if b.token != "" {
		u += b.token + "/"
	}
	return u
}

// PasswordMode reports whether visitors must log in with the gateway
// password.
func (s *Server) PasswordMode() bool { return s.havePW }

// Serve runs the server until Close or ctx is done. If the listener fails
// (see listener.go: a phone may take it away from a suspended app), Serve
// keeps waiting for Rebind instead of returning, so the host sessions stay;
// Listening reports the failure.
func (s *Server) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	for {
		s.lnMu.Lock()
		b, changed := s.bind.Load(), s.lnChanged
		s.lnMu.Unlock()
		var err error
		if b.ln != nil {
			if s.useTLS {
				err = s.srv.ServeTLS(b.ln, "", "")
			} else {
				err = s.srv.Serve(b.ln)
			}
			if errors.Is(err, http.ErrServerClosed) || s.ctx.Err() != nil {
				return nil
			}
			s.lnMu.Lock()
			if s.bind.Load() == b && s.lnErr == nil {
				s.lnErr = err
				s.log.Warn("web: the listener stopped", "err", err)
			}
			s.lnMu.Unlock()
		}
		select {
		case <-changed:
		case <-s.ctx.Done():
			return nil
		}
	}
}

// Close stops the server and ends every host session. It returns once
// nothing of the server runs any more, reconnections included: after it,
// no gateway code touches the configuration directory.
func (s *Server) Close() {
	s.cancel()
	s.srv.Close()
	s.lnMu.Lock()
	if b := s.bind.Load(); b.ln != nil {
		b.ln.Close() // one Rebind made that Serve has not picked up yet
	}
	s.lnMu.Unlock()
	s.mu.Lock()
	all := make([]*webSession, 0, len(s.sessions))
	for _, ws := range s.sessions {
		all = append(all, ws)
	}
	s.mu.Unlock()
	for _, ws := range all {
		s.dropSession(ws)
	}
	s.wg.Wait()
}

func (s *Server) setHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	if s.cfg.FrameAncestor == "" {
		h.Set("X-Frame-Options", "DENY")
	}
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	ws := "ws://" + r.Host
	if s.secure(r) {
		ws = "wss://" + r.Host
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob: data:; "+
		"connect-src 'self' "+ws+"; frame-ancestors "+s.frameAncestors()+"; base-uri 'none'; form-action 'none'")
}

// ServeHTTP checks the Host header, strips the loopback token prefix and
// hands over to the router.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b := s.bind.Load()
	if !b.hosts.ok(r.Host) && !s.viaPublic(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.setHeaders(w, r)
	if b.token != "" {
		rest, ok := strings.CutPrefix(r.URL.Path, "/")
		tok, sub, _ := strings.Cut(rest, "/")
		if !ok || !ctEqual(tok, b.token) {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(rest, "/") {
			http.Redirect(w, r, "/"+tok+"/", http.StatusTemporaryRedirect)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + sub
		r2.URL.RawPath = ""
		r = r2
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() *http.ServeMux {
	m := http.NewServeMux()
	assets, _ := fs.Sub(assetFS, "assets")
	files := http.FileServerFS(assets)
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := fs.ReadFile(assets, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
	// The pointer drawn by the page, shared with the standalone viewer.
	m.HandleFunc("GET /assets/pointer.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(desktop.PointerJS)
	})
	m.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + r.PathValue("name")
		files.ServeHTTP(w, r2)
	})

	// Authenticated, CSRF-checked JSON calls.
	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request, ws *webSession)) {
		m.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			ws, aerr := s.authenticate(r)
			if aerr != nil {
				writeErr(w, aerr)
				return
			}
			h(w, r, ws)
		})
	}
	m.HandleFunc("POST /api/v1/session", s.handleSession)
	m.HandleFunc("POST /api/v1/login", s.handleLogin)
	api("POST /api/v1/logout", s.handleLogout)
	api("GET /api/v1/connections", s.handleConnections)
	api("GET /api/v1/peers", s.handlePeers)
	api("POST /api/v1/peers/alias", s.handlePeerAlias)
	api("POST /api/v1/peers/forget", s.handlePeerForget)
	api("POST /api/v1/peers/password", s.handlePeerPassword)
	api("POST /api/v1/connect", s.handleConnect)
	api("GET /api/v1/connect/{id}", s.handleAttempt)
	api("DELETE /api/v1/connect/{id}", s.handleAttemptCancel)
	api("POST /api/v1/disconnect", s.handleDisconnect)
	api("POST /api/v1/ticket", s.handleTicket)
	api("GET /api/v1/files/list", s.handleList)
	api("POST /api/v1/files/upload", s.handleUpload)
	api("POST /api/v1/files/mkdir", s.handleMkdir)
	api("POST /api/v1/files/rename", s.handleRename)
	api("POST /api/v1/files/delete", s.handleDelete)
	api("POST /api/v1/clip/get", s.handleClipGet)
	api("POST /api/v1/clip/set", s.handleClipSet)
	api("POST /api/v1/access", s.handleAccess)
	api("GET /api/v1/info", s.handleInfo)
	api("GET /api/v1/screenshot", s.handleScreenshot)
	api("GET /api/v1/term/sessions", s.handleTermSessions)
	api("POST /api/v1/term/kill", s.handleTermKill)
	// Ticket-authenticated: the browser cannot add headers to these.
	m.HandleFunc("GET /api/v1/files/download", s.handleDownload)
	m.HandleFunc("GET /api/v1/term/ws", s.handleTermWS)
	m.HandleFunc("GET /api/v1/desktop/ws", s.handleDesktopWS)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "no such API call"})
	})
	return m
}

const (
	hdrSession = "X-RemoHop-Session"
	hdrCSRF    = "X-RemoHop-CSRF"
)

func (s *Server) cookieName(r *http.Request) string {
	if s.secure(r) {
		return "__Host-remohop"
	}
	return "remohop_session"
}

func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, ws *webSession) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(r), Value: ws.id, Path: "/", HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteStrictMode, MaxAge: int(s.cfg.SessionTTL / time.Second)})
}

// sessionFromRequest finds the browser session of a request: the cookie in
// password mode, the page-held session id on loopback.
func (s *Server) sessionFromRequest(r *http.Request) *webSession {
	if s.passwordMode() {
		c, err := r.Cookie(s.cookieName(r))
		if err != nil {
			return nil
		}
		return s.lookupSession(c.Value)
	}
	return s.lookupSession(r.Header.Get(hdrSession))
}

var (
	errLoginRequired = &apiError{Status: http.StatusUnauthorized, Code: "login_required", Message: "log in first"}
	errBearerOff     = &apiError{Status: http.StatusUnauthorized, Code: "bearer_disabled",
		Message: "this gateway does not accept Authorization: Bearer; start it with --api-token-file to allow program access"}
	errCrossSite = &apiError{Status: http.StatusForbidden, Code: "bad_origin", Message: "cross-site request refused"}
	errBadCSRF   = &apiError{Status: http.StatusForbidden, Code: "bad_csrf", Message: "missing or wrong CSRF token"}
)

// noSession is the error for a request without a session: log in, or,
// for a program sending a bearer token to a gateway that does not take
// them, how to turn program access on.
func (s *Server) noSession(r *http.Request) *apiError {
	if s.passwordMode() && !s.haveAPI && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return errBearerOff
	}
	return errLoginRequired
}

// authenticate checks a JSON API request: the session, and for anything but
// GET the CSRF token and, if the browser says where it comes from, that it
// is this gateway. A bearer request is a program's, except when it carries
// browser signals: then it is held to the same checks (see fromBrowser).
func (s *Server) authenticate(r *http.Request) (*webSession, *apiError) {
	ws, aerr, bearer := s.bearerSession(r)
	if bearer {
		if aerr != nil {
			return nil, aerr
		}
		if !fromBrowser(r) {
			return ws, nil
		}
	} else if ws = s.sessionFromRequest(r); ws == nil {
		return nil, s.noSession(r)
	}
	if crossSite(r, s.secure(r)) {
		return nil, errCrossSite
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !ctEqual(r.Header.Get(hdrCSRF), ws.csrf) {
		return nil, errBadCSRF
	}
	return ws, nil
}

// checkUnauth gates the calls made before a session exists (session, login):
// same-origin when the browser says where it comes from, and JSON only,
// which a cross-site form cannot send.
func (s *Server) checkUnauth(r *http.Request) *apiError {
	if crossSite(r, s.secure(r)) {
		return errCrossSite
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return &apiError{Status: http.StatusUnsupportedMediaType, Code: "bad_content_type", Message: "send application/json"}
	}
	return nil
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) *apiError {
	body := http.MaxBytesReader(w, r.Body, limit)
	dec := newJSONDecoder(body)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "bad JSON body"}
	}
	return nil
}

func (s *Server) sessionJSON(ws *webSession) map[string]any {
	m := map[string]any{
		"csrf": ws.csrf, "mode": "token", "version": Version, "target": s.cfg.Target,
		"saved_peers": !s.cfg.NoSaved,
		"conns":       ws.list(),
	}
	if s.passwordMode() {
		m["mode"] = "password"
	} else {
		m["sid"] = ws.id
	}
	return m
}

// handleSession creates (loopback) or confirms (password mode) the browser
// session. On loopback the token in the URL is the login and the page may
// resume a session it kept in sessionStorage.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if aerr := s.checkUnauth(r); aerr != nil {
		writeErr(w, aerr)
		return
	}
	var req struct {
		SID string `json:"sid"`
	}
	if aerr := readJSON(w, r, &req, 1024); aerr != nil {
		writeErr(w, aerr)
		return
	}
	if s.passwordMode() {
		ws, aerr, ok := s.bearerSession(r)
		if !ok {
			ws, aerr = s.sessionFromRequest(r), nil
			if ws == nil {
				aerr = s.noSession(r)
			}
		}
		if aerr != nil {
			writeErr(w, aerr)
			return
		}
		writeJSON(w, http.StatusOK, s.sessionJSON(ws))
		return
	}
	ws := s.lookupSession(req.SID)
	if ws == nil {
		var aerr *apiError
		if ws, aerr = s.newSession(); aerr != nil {
			writeErr(w, aerr)
			return
		}
	}
	writeJSON(w, http.StatusOK, s.sessionJSON(ws))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.passwordMode() {
		writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "this gateway has no password login"})
		return
	}
	if aerr := s.checkUnauth(r); aerr != nil {
		writeErr(w, aerr)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if aerr := readJSON(w, r, &req, 4096); aerr != nil {
		writeErr(w, aerr)
		return
	}
	src := s.clientKey(r)
	if ok, wait := s.lim.allow(src); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, &apiError{Status: http.StatusTooManyRequests, Code: "locked_out",
			Message: fmt.Sprintf("too many failed logins; try again in %d seconds", int(wait.Seconds())+1)})
		return
	}
	if !passwordMatches(s.pwHash, req.Password) {
		s.lim.fail(src)
		s.log.Warn("gateway login failed", "from", src)
		writeErr(w, &apiError{Status: http.StatusUnauthorized, Code: "bad_password", Message: "wrong password"})
		return
	}
	s.lim.success(src)
	ws, aerr := s.newSession()
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	s.setCookie(w, r, ws)
	writeJSON(w, http.StatusOK, s.sessionJSON(ws))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, ws *webSession) {
	s.dropSession(ws)
	if s.passwordMode() {
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(r), Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
			Secure: s.secure(r), SameSite: http.SameSiteStrictMode})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request, ws *webSession) {
	writeJSON(w, http.StatusOK, map[string]any{"conns": ws.list()})
}

func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request, ws *webSession) {
	if s.cfg.NoSaved {
		writeJSON(w, http.StatusOK, map[string]any{"peers": []PeerInfo{}})
		return
	}
	ps, err := s.cfg.Peers()
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	if ps == nil {
		ps = []PeerInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": ps})
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req connectReq
	if aerr := readJSON(w, r, &req, 8192); aerr != nil {
		writeErr(w, aerr)
		return
	}
	a, aerr := s.startConnect(ws, req)
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusAccepted, a.snapshot())
}

func (s *Server) handleAttempt(w http.ResponseWriter, r *http.Request, ws *webSession) {
	ws.mu.Lock()
	a := ws.attempts[r.PathValue("id")]
	ws.mu.Unlock()
	if a == nil {
		writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "no such connection attempt"})
		return
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (s *Server) handleAttemptCancel(w http.ResponseWriter, r *http.Request, ws *webSession) {
	ws.mu.Lock()
	a := ws.attempts[r.PathValue("id")]
	ws.mu.Unlock()
	if a != nil && !a.finished() {
		a.cancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		C string `json:"c"`
	}
	if aerr := readJSON(w, r, &req, 1024); aerr != nil {
		writeErr(w, aerr)
		return
	}
	ws.disconnect(req.C)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTicket(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		Purpose string `json:"purpose"`
	}
	if aerr := readJSON(w, r, &req, 1024); aerr != nil {
		writeErr(w, aerr)
		return
	}
	if req.Purpose != "ws" && req.Purpose != "dl" {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "purpose must be ws or dl"})
		return
	}
	t, aerr := s.issueTicket(ws, req.Purpose)
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ticket": t})
}

// ticketAuth authenticates a ticket request (WebSocket upgrade or
// download): the ticket, plus the cookie in password mode, plus a same-origin
// Origin when the browser sends one (always, for WebSockets).
func (s *Server) ticketAuth(w http.ResponseWriter, r *http.Request, purpose string, needOrigin bool) *webSession {
	if bw, aerr, ok := s.bearerSession(r); ok {
		if aerr != nil {
			writeErr(w, aerr)
			return nil
		}
		// A program's WebSocket library may send no Origin, but one that
		// is sent must be ours: a proxy injecting the bearer header into
		// a browser's cross-site WebSocket must not make it pass.
		if crossSite(r, s.secure(r)) {
			writeErr(w, errCrossSite)
			return nil
		}
		if ws := s.redeemTicket(r.URL.Query().Get("t"), purpose); ws == bw {
			return ws
		}
		writeErr(w, errLoginRequired)
		return nil
	}
	if needOrigin && !sameOrigin(r, s.secure(r)) || crossSite(r, s.secure(r)) {
		writeErr(w, errCrossSite)
		return nil
	}
	ws := s.redeemTicket(r.URL.Query().Get("t"), purpose)
	if ws == nil {
		writeErr(w, s.noSession(r))
		return nil
	}
	if s.passwordMode() {
		if cw := s.sessionFromRequest(r); cw != ws {
			writeErr(w, s.noSession(r))
			return nil
		}
	}
	return ws
}

// Start prints nothing and opens nothing; it is the helper the command and
// tests use to serve in the background.
func (s *Server) Start() <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- s.Serve(context.Background()) }()
	return ch
}

// viaPublic reports whether r arrived through --public-url: its Host is the
// public host. Only a loopback gateway accepts it, so the request came
// through the local proxy or tunnel (or from a local user).
func (s *Server) viaPublic(r *http.Request) bool {
	return s.public != nil && s.public.ok(r.Host)
}

// secure reports whether the browser reached this request over https: the
// gateway's own TLS, or the public URL in front of it.
func (s *Server) secure(r *http.Request) bool {
	return s.useTLS || s.viaPublic(r)
}

// clientKey is the login rate limiter's key for r. Behind --public-url every
// request comes from the local tunnel, so the visitor's address is taken
// from the header named by --real-ip-header, and only then: a header the
// proxy does not set is the client's to choose.
func (s *Server) clientKey(r *http.Request) string {
	if s.cfg.RealIPHeader != "" && s.viaPublic(r) {
		if ip, ok := realIP(r.Header.Values(s.cfg.RealIPHeader)); ok {
			return srcKey(ip.String())
		}
	}
	return srcKey(r.RemoteAddr)
}

// parsePublicURL accepts https://HOST[:PORT][/] and returns a matcher for
// that host and port.
func parsePublicURL(raw string) (*hostMatcher, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("--public-url must look like https://desk.example.com (https, no path), got %q", raw)
	}
	port := 443
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("--public-url: bad port %q", p)
		}
		port = n
	}
	h := u.Hostname()
	if strings.HasPrefix(h, "*") {
		return nil, errors.New("--public-url: name one host, not a wildcard")
	}
	return newHostMatcher(port, true, h), nil
}

// bearerSession authenticates a program that sends the API token
// (--api-token-file) as "Authorization: Bearer <token>" on every request,
// for example through a proxy that injects the header so the program never
// handles the token itself. ok reports whether the request carried such a
// header and the gateway takes them (otherwise the header is ignored and
// the request needs a browser session). Such clients share one gateway
// session, so host connections persist across their requests. A browser
// never attaches an Authorization header on its own, so a program's
// requests need no CSRF token or Origin; but a proxy may inject the header
// into browser traffic too, so callers still apply the browser checks to
// bearer requests that carry browser signals (fromBrowser). Failures count
// against the login rate limiter.
func (s *Server) bearerSession(r *http.Request) (ws *webSession, aerr *apiError, ok bool) {
	tok, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || !s.passwordMode() || !s.haveAPI {
		return nil, nil, false
	}
	src := s.clientKey(r)
	if allowed, wait := s.lim.allow(src); !allowed {
		return nil, &apiError{Status: http.StatusTooManyRequests, Code: "locked_out",
			Message: fmt.Sprintf("too many failed logins; try again in %d seconds", int(wait.Seconds())+1)}, true
	}
	if !passwordMatches(s.apiHash, strings.TrimSpace(tok)) {
		s.lim.fail(src)
		s.log.Warn("gateway bearer login failed", "from", src)
		return nil, &apiError{Status: http.StatusUnauthorized, Code: "bad_password", Message: "wrong API token"}, true
	}
	s.lim.success(src)
	s.mu.Lock()
	id := s.apiSID
	s.mu.Unlock()
	if ws = s.lookupSession(id); ws != nil {
		return ws, nil, true
	}
	if ws, aerr = s.newSession(); aerr != nil {
		return nil, aerr, true
	}
	s.mu.Lock()
	s.apiSID = ws.id
	s.mu.Unlock()
	return ws, nil, true
}
