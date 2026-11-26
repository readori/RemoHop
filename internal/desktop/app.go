package desktop

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/update"
	"github.com/readori/RemoHop/internal/web"
)

// AskTimeout is how long a connection request waits for an answer.
const AskTimeout = 60 * time.Second

// Phases of "This computer".
const (
	PhaseOff        = "off"       // incoming connections are switched off
	PhaseStarting   = "starting"  // host running, not registered yet
	PhaseReady      = "ready"     // reachable: ID (or address) and password known
	PhaseError      = "error"     // the host could not start
	PhaseElsewhere  = "elsewhere" // another RemoHop host serves this computer (the service, a terminal)
	PhaseNoServer   = "no_server" // ready, but without a rendezvous server: reachable by address only
	serverOfficial  = "official"
	serverCustom    = "custom"
	serverNone      = "none"
	defaultPort     = "21116"
	maxPeersRefresh = 30 * time.Second
)

// Config configures an App.
type Config struct {
	// Dir is the remohop config directory; empty means the default.
	Dir string
	// Signal overrides the configured rendezvous server (--signal).
	Signal string
	Logger *slog.Logger
	// AskTimeout defaults to the package's AskTimeout.
	AskTimeout time.Duration
	// UIOrigin is the origin of the app's own page, which shows the
	// console of each host in a frame (web.Config.FrameAncestor).
	UIOrigin string

	// Seams for tests.
	Host     func(*hostd.Config)
	Gateway  func(*web.Config)
	NoSignal bool
	Now      func() time.Time
	// The update check (update.go): the version file's address (empty:
	// update.URL), the HTTP client, the first check's delay and the
	// interval. NoUpdateCheck leaves the check out altogether.
	UpdateURL     string
	UpdateClient  *http.Client
	UpdateDelay   time.Duration
	UpdateEvery   time.Duration
	NoUpdateCheck bool
	// One-click upgrades: the release download address (update.Fetcher
	// Base), the trusted keys and what restarts the program; the defaults
	// are GitHub, update.Keys and restartInto.
	UpdateBase    string
	UpdateKeys    []ed25519.PublicKey
	UpdateRestart func(update.Plan) error
	UpdatePlan    *update.Plan // what an upgrade replaces (tests); default: this program
}

// Hooks are how the window shell learns what needs the user's attention.
// They run on the App's goroutines and must not block.
type Hooks struct {
	// Request: a connection request arrived (bring the window up).
	Request func(r Request)
	// Sessions: who is connected changed (the "being controlled" banner
	// and the tray). names is empty when nobody is.
	Sessions func(names []string)
	// Lang: the user picked a language.
	Lang func(lang string)
}

// App is the desktop app's state and logic: this computer's host, the
// connection requests, the gateway behind the console, the address book.
// The UI server (server.go) shows and drives it.
type App struct {
	cfg Config
	log *slog.Logger
	dir string

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	settings  Settings
	hooks     Hooks
	subs      map[chan struct{}]struct{}
	phase     string
	errCode   string
	errText   string
	otherID   string
	host      *hostd.Host
	hostStop  context.CancelFunc
	hostDone  chan struct{}
	started   time.Time
	server    string
	firstSeen map[int]bool // approval N -> the device never asked before
	lastNames string

	gw        *web.Server
	gwDone    chan error
	onGateway func(origin string) // the UI server lets the page frame it

	peersMu sync.Mutex
	peers   []Peer
	peersAt time.Time
	// peersMtime is the saved-peers file's modification time when peers
	// was made.
	peersMtime time.Time
	peersErr   string

	upd updateInfo
}

// NewApp loads the settings. Start brings things up.
func NewApp(cfg Config) (*App, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.AskTimeout <= 0 {
		cfg.AskTimeout = AskTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	dir := cfg.Dir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	st, err := loadSettings(dir)
	if err != nil {
		cfg.Logger.Warn("the app's settings could not be read; starting with defaults", "err", err)
	}
	a := &App{cfg: cfg, log: cfg.Logger, dir: dir, settings: st, phase: PhaseOff,
		subs: map[chan struct{}]struct{}{}, firstSeen: map[int]bool{}, upd: newUpdateInfo(cli.Version)}
	if cfg.UpdatePlan != nil {
		a.upd.method, a.upd.plan, a.upd.planOK = update.Method{Kind: update.KindDownload}, *cfg.UpdatePlan, true
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	return a, nil
}

// SetHooks sets the shell's hooks.
func (a *App) SetHooks(h Hooks) {
	a.mu.Lock()
	a.hooks = h
	a.mu.Unlock()
}

// Dir is the config directory the app uses.
func (a *App) Dir() string { return a.dir }

// Settings returns a copy of the settings.
func (a *App) Settings() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.settings
	s.Trusted = append([]TrustedDevice(nil), s.Trusted...)
	s.Seen = nil
	return s
}

// update changes the settings under the lock and saves them.
func (a *App) update(fn func(s *Settings)) error {
	a.mu.Lock()
	fn(&a.settings)
	s := a.settings
	a.mu.Unlock()
	err := saveSettings(a.dir, s)
	if err != nil {
		a.log.Warn("could not save the app's settings", "err", err)
	}
	a.wake()
	return err
}

// Subscribe returns a channel that receives whenever the state may have
// changed, and a function that ends the subscription.
func (a *App) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subs[ch] = struct{}{}
	a.mu.Unlock()
	return ch, func() {
		a.mu.Lock()
		delete(a.subs, ch)
		a.mu.Unlock()
	}
}

func (a *App) wake() {
	a.mu.Lock()
	for ch := range a.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	a.mu.Unlock()
}

// Start starts the gateway behind the console and, if incoming
// connections are on, this computer's host.
func (a *App) Start() error {
	if err := a.startGateway(); err != nil {
		return err
	}
	a.mu.Lock()
	on := a.settings.Incoming
	a.mu.Unlock()
	if on {
		a.startHost()
	}
	go a.watchSessions()
	a.cleanupUpdate()
	if !a.cfg.NoUpdateCheck {
		go a.updateLoop()
	}
	return nil
}

// Stop ends every incoming session, takes the host offline and closes the
// gateway (and with it the connections to other computers).
func (a *App) Stop() {
	a.stopHost()
	a.cancel()
	a.mu.Lock()
	gw, done := a.gw, a.gwDone
	a.gw = nil
	a.mu.Unlock()
	if gw != nil {
		gw.Close()
		<-done
	}
}

func (a *App) startGateway() error {
	wc := web.Config{Listen: "127.0.0.1:0", ConfigDir: a.cfg.Dir, Persistent: true, Logger: a.log, FrameAncestor: a.cfg.UIOrigin}
	if a.cfg.Gateway != nil {
		a.cfg.Gateway(&wc)
	}
	gw, err := web.New(wc)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- gw.Serve(a.ctx) }()
	a.mu.Lock()
	a.gw, a.gwDone = gw, done
	a.mu.Unlock()
	if a.onGateway != nil {
		u, _ := url.Parse(gw.URL())
		a.onGateway(u.Scheme + "://" + u.Host)
	}
	return nil
}

// ConsoleURL is the address of the console (the web client's page in the
// in-process gateway), connecting to target if it is not empty.
func (a *App) ConsoleURL(target string) (string, error) {
	a.mu.Lock()
	gw := a.gw
	lang, theme := a.settings.Lang, a.settings.Theme
	a.mu.Unlock()
	if gw == nil {
		return "", errors.New("the console is not running")
	}
	u := gw.URL()
	q := url.Values{}
	if t := cli.NormalizeTarget(strings.TrimSpace(target)); t != "" {
		if err := checkTarget(t); err != nil {
			return "", err
		}
		q.Set("connect", t)
	}
	if lang != "" {
		q.Set("lang", lang)
	}
	if theme != "" {
		q.Set("theme", theme)
	}
	q.Set("app", "desktop")
	if a.cfg.UIOrigin != "" {
		q.Set("embed", "1")
		q.Set("parent", a.cfg.UIOrigin)
	}
	return u + "?" + q.Encode(), nil
}

// CloseConsole ends the sessions to target (its tab was closed).
func (a *App) CloseConsole(target string) {
	a.mu.Lock()
	gw := a.gw
	a.mu.Unlock()
	if gw != nil {
		gw.DisconnectTarget(cli.NormalizeTarget(strings.TrimSpace(target)))
	}
}

// checkTarget accepts what the connect field may hold: a 9-digit ID, a
// saved name, or host:port.
func checkTarget(t string) error {
	if len(t) > 255 {
		return Errorf("bad_target", "that is not an ID or an address")
	}
	for _, r := range t {
		if r <= ' ' || r == '/' || r == '\\' || r == '?' || r == '#' || r == '@' {
			return Errorf("bad_target", "that is not an ID or an address")
		}
	}
	return nil
}

// --- this computer -----------------------------------------------------------

// SetIncoming switches incoming connections on or off. Off ends every
// incoming session.
func (a *App) SetIncoming(on bool) error {
	if err := a.update(func(s *Settings) { s.Incoming = on }); err != nil {
		return err
	}
	if on {
		a.startHost()
	} else {
		a.stopHost()
	}
	return nil
}

func (a *App) startHost() {
	a.mu.Lock()
	if a.host != nil {
		a.mu.Unlock()
		return
	}
	a.phase, a.errCode, a.errText, a.otherID = PhaseStarting, "", "", ""
	a.mu.Unlock()
	a.wake()

	hc := hostd.Config{
		Dir:        a.cfg.Dir,
		AskTimeout: a.cfg.AskTimeout,
		NoPairing:  true,
		Control:    true,
		Logger:     a.log,
		OnApproval: a.onApproval,
		Overrides:  &hostd.Overrides{AskAll: true, TempPasswordOnly: true},
	}
	if !a.cfg.NoSignal {
		addr, opts, err := signal.Resolve(a.cfg.Signal)
		switch {
		case errors.Is(err, signal.ErrNotConfigured):
			// No server: reachable by address only.
		case err != nil:
			a.fail("start_failed", err)
			return
		default:
			if addr == "" {
				addr = signal.Display("", opts)
			}
			hc.Signal, hc.SignalOptions = addr, opts
		}
	}
	if a.cfg.Dir == "" {
		if s, err := config.All(); err == nil {
			hc.Listen = s[hostd.SettingListen]
		}
	}
	if a.cfg.Host != nil {
		a.cfg.Host(&hc)
	}
	h, release, err := hostd.StartExclusive(hc)
	if err != nil {
		if e, ok := errors.AsType[*hostd.AlreadyRunningError](err); ok {
			a.mu.Lock()
			a.phase, a.otherID = PhaseElsewhere, signal.FormatID(e.Status.ID)
			a.mu.Unlock()
			a.log.Info("another RemoHop host serves this computer", "id", e.Status.ID)
			a.wake()
			return
		}
		a.fail("start_failed", err)
		return
	}
	h.OnChange(a.wake)
	ctx, stop := context.WithCancel(a.ctx)
	done := make(chan struct{})
	a.mu.Lock()
	a.host, a.hostStop, a.hostDone, a.started, a.server = h, stop, done, a.cfg.Now(), hc.Signal
	a.mu.Unlock()
	go func() {
		defer close(done)
		err := h.Run(ctx)
		release()
		a.mu.Lock()
		if a.host == h {
			a.host = nil
		}
		a.mu.Unlock()
		if ctx.Err() == nil {
			if err == nil {
				err = errors.New("the host stopped unexpectedly")
			}
			a.fail("start_failed", err)
		}
		a.wake()
	}()
	a.log.Info("incoming connections on", "server", hc.Signal)
	a.wake()
}

func (a *App) fail(code string, err error) {
	a.mu.Lock()
	a.phase, a.errCode, a.errText = PhaseError, code, err.Error()
	a.mu.Unlock()
	a.log.Error("incoming connections could not start", "err", err)
	a.wake()
}

func (a *App) stopHost() {
	a.mu.Lock()
	stop, done := a.hostStop, a.hostDone
	a.host, a.hostStop, a.hostDone = nil, nil, nil
	a.phase, a.errCode, a.errText, a.otherID = PhaseOff, "", "", ""
	a.mu.Unlock()
	if stop != nil {
		stop()
		<-done
		a.log.Info("incoming connections off")
	}
	a.wake()
}

// restartHost applies a new rendezvous server.
func (a *App) restartHost() {
	a.mu.Lock()
	on := a.settings.Incoming
	a.mu.Unlock()
	if on {
		a.stopHost()
		a.startHost()
	}
}

func (a *App) running() (*hostd.Host, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.host == nil {
		return nil, Errorf("not_running", "incoming connections are off")
	}
	return a.host, nil
}

// onApproval runs for each login (or access request) that waits for the
// user. A remembered device is let in at once with the access chosen when
// it was remembered; everything else waits for the user, and the shell is
// told so it can bring the window up.
func (a *App) onApproval(p hostd.Approval) {
	now := a.cfg.Now()
	a.mu.Lock()
	h := a.host
	t, trusted := a.settings.trusted(p.Identity)
	login := len(p.Access) == 0 && p.Action == ""
	if login && !trusted {
		a.firstSeen[p.N] = !a.settings.see(p.Identity, now)
	}
	hook := a.hooks.Request
	a.mu.Unlock()
	if login {
		a.update(func(*Settings) {}) // save what see() recorded
	}
	if login && trusted && h != nil {
		if err := h.DecideWith(p.N, true, features(t.Control, t.Files)); err == nil {
			a.log.Info("a remembered device connected", "identity", p.Identity, "control", t.Control, "files", t.Files)
			a.wake()
			return
		}
	}
	a.wake()
	if hook != nil {
		hook(a.requestOf(p, now))
	}
}

// features is what a login gets: the screen and system information
// always (that is what "view only" means), mouse and keyboard with
// control, files and the clipboard with files.
func features(control, files bool) []perm.Feature {
	f := []perm.Feature{perm.Screen, perm.Info}
	if control {
		f = append(f, perm.Input)
	}
	if files {
		f = append(f, perm.File, perm.Clip)
	}
	return f
}

// Decide answers request n. For a login, control and files are what the
// device may do (unknown devices are offered view only), and remember
// trusts it for the next time. For an access request, allow grants what it
// asked for.
func (a *App) Decide(n int, allow, control, files, remember bool) error {
	h, err := a.running()
	if err != nil {
		return err
	}
	var p *hostd.Approval
	for _, x := range h.Pending() {
		if x.N == n {
			p = &x
			break
		}
	}
	if p == nil {
		return Errorf("gone", "that request is no longer waiting")
	}
	if len(p.Access) > 0 || p.Action != "" {
		err = h.Decide(n, allow)
	} else {
		err = h.DecideWith(n, allow, features(control, files))
		if err == nil && allow && remember {
			a.update(func(s *Settings) {
				s.trust(TrustedDevice{Identity: p.Identity, Name: clean(p.Device), Control: control, Files: files, Added: a.cfg.Now().UTC()})
			})
		}
	}
	a.mu.Lock()
	delete(a.firstSeen, n)
	a.mu.Unlock()
	a.wake()
	return err
}

// SetSessionAccess changes what connected session id may do.
func (a *App) SetSessionAccess(id int, control, files bool) error {
	h, err := a.running()
	if err != nil {
		return err
	}
	err = h.SetSessionFeatures(id, false, features(control, files))
	a.wake()
	return err
}

// Disconnect ends session id; id 0 ends them all.
func (a *App) Disconnect(id int) error {
	h, err := a.running()
	if err != nil {
		return err
	}
	if id == 0 {
		for _, s := range h.Sessions() {
			h.Disconnect(s.ID(), hostd.ReasonDisconnectedAtHost)
		}
		a.wake()
		return nil
	}
	err = h.Disconnect(id, hostd.ReasonDisconnectedAtHost)
	a.wake()
	return err
}

// Regenerate replaces the temporary password.
func (a *App) Regenerate() error {
	h, err := a.running()
	if err != nil {
		return err
	}
	return h.RotatePassword()
}

// ForgetDevice stops trusting a remembered device.
func (a *App) ForgetDevice(identity string) error {
	var found bool
	a.update(func(s *Settings) { found = s.forget(identity) })
	if !found {
		return Errorf("gone", "no such remembered device")
	}
	return nil
}

// watchSessions tells the shell whenever who is connected changes.
func (a *App) watchSessions() {
	ch, cancel := a.Subscribe()
	defer cancel()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		st := a.State()
		var names []string
		for _, s := range st.Incoming.Sessions {
			names = append(names, s.Label)
		}
		key := strings.Join(names, "\x00")
		a.mu.Lock()
		changed := key != a.lastNames
		a.lastNames = key
		hook := a.hooks.Sessions
		a.mu.Unlock()
		if changed && hook != nil {
			hook(names)
		}
		select {
		case <-a.ctx.Done():
			return
		case <-ch:
		case <-t.C:
		}
	}
}

// --- the rendezvous server ----------------------------------------------------

// serverChoice reports which rendezvous server is configured.
func (a *App) serverChoice() (mode, addr string) {
	all, _ := config.AllIn(a.dir)
	v := strings.TrimSpace(all[signal.SettingAddr])
	switch {
	case v == "":
		// Also when quick support (which shares the setting) chose none.
		return serverNone, ""
	case v == OfficialServer || v == OfficialServer+":"+defaultPort:
		return serverOfficial, OfficialServer
	}
	return serverCustom, v
}

// SetServer picks the rendezvous server: "official", "custom" with addr,
// or "none" (connect by address only).
func (a *App) SetServer(mode, addr string) error {
	addr = strings.TrimSpace(addr)
	switch mode {
	case serverOfficial:
		addr = OfficialServer
	case serverCustom:
		if addr == "" {
			return Errorf("no_server", "enter the server address")
		}
		if err := checkServer(addr); err != nil {
			return Errorf("bad_server", err.Error())
		}
	case serverNone:
		addr = ""
	default:
		return Errorf("bad_request", "unknown server choice")
	}
	if err := a.setConfig(signal.SettingAddr, addr); err != nil {
		return err
	}
	// Quick support reads the same choice: "none" is remembered so that
	// it does not ask again either.
	none := ""
	if mode == serverNone {
		none = "on"
	}
	if err := a.setConfig(signal.SettingNone, none); err != nil {
		return err
	}
	a.log.Info("rendezvous server changed", "mode", mode)
	a.restartHost()
	a.peersMu.Lock()
	a.peersAt = time.Time{}
	a.peersMu.Unlock()
	a.wake()
	return nil
}

// setConfig changes a remohop setting (config.json in the config
// directory, shared with the command line).
func (a *App) setConfig(key, value string) error { return config.Set(key, value) }

// checkServer accepts a host name or address with an optional port.
func checkServer(server string) error {
	host, port, err := signal.SplitAddr(server)
	if err != nil {
		return err
	}
	bad := fmt.Errorf("%q is not a server address; it looks like signal.example.com or signal.example.com:21116", server)
	n := 0
	for _, r := range port {
		if r < '0' || r > '9' {
			return bad
		}
		n = n*10 + int(r-'0')
		if n > 65535 {
			return bad
		}
	}
	if n < 1 {
		return bad
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") {
		return bad
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return bad
		}
	}
	return nil
}

// FinishSetup ends the first-run wizard: the server choice ("" skips it)
// and the language ("" follows the system).
func (a *App) FinishSetup(mode, addr, lang string) error {
	if mode != "" {
		if err := a.SetServer(mode, addr); err != nil {
			return err
		}
	}
	return a.update(func(s *Settings) {
		s.Setup = true
		if l := normLang(lang); l != "" || lang == "" {
			s.Lang = l
		}
	})
}

// SetLang sets the language ("" follows the system).
func (a *App) SetLang(lang string) error {
	l := normLang(lang)
	if lang != "" && l == "" {
		return Errorf("bad_request", "lang is zh, en or empty")
	}
	err := a.update(func(s *Settings) { s.Lang = l })
	a.mu.Lock()
	hook := a.hooks.Lang
	a.mu.Unlock()
	if hook != nil {
		hook(l)
	}
	return err
}

// SetTheme sets the theme ("" follows the system).
func (a *App) SetTheme(theme string) error {
	t := normTheme(theme)
	if theme != "" && t == "" {
		return Errorf("bad_request", "theme is light, dark or empty")
	}
	return a.update(func(s *Settings) { s.Theme = t })
}

// --- state ---------------------------------------------------------------------

// Request is a connection request as the page shows it.
type Request struct {
	N        int    `json:"n"`
	Name     string `json:"name,omitempty"`
	Device   string `json:"device,omitempty"`
	Identity string `json:"identity"`
	From     string `json:"from,omitempty"`
	Auth     string `json:"auth"`
	Left     int    `json:"left"`
	// FirstSeen: this device never asked before.
	FirstSeen bool `json:"first_seen"`
	// Access is set for a connected session that asks for more ("input",
	// "file"); Session is its ID.
	Access  []string `json:"access,omitempty"`
	Session int      `json:"session,omitempty"`
}

// Session is a connected device.
type Session struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Device   string `json:"device,omitempty"`
	Identity string `json:"identity"`
	From     string `json:"from,omitempty"`
	Path     string `json:"path"`
	Seconds  int    `json:"seconds"`
	Control  bool   `json:"control"`
	Files    bool   `json:"files"`
	Trusted  bool   `json:"trusted"`
}

// Incoming is "This computer".
type Incoming struct {
	On        bool      `json:"on"`
	Phase     string    `json:"phase"`
	ErrorCode string    `json:"error_code,omitempty"`
	Error     string    `json:"error,omitempty"`
	ID        string    `json:"id,omitempty"`
	OtherID   string    `json:"other_id,omitempty"`
	Password  string    `json:"password,omitempty"`
	Online    bool      `json:"online"`
	Transport string    `json:"transport,omitempty"`
	Addresses []string  `json:"addresses,omitempty"`
	Waited    int       `json:"waited,omitempty"`
	Device    string    `json:"device,omitempty"`
	Requests  []Request `json:"requests"`
	Sessions  []Session `json:"sessions"`
}

// State is everything the page shows.
type State struct {
	Version  string          `json:"version"`
	Setup    bool            `json:"setup"`
	Lang     string          `json:"lang"`
	Theme    string          `json:"theme"`
	Server   ServerState     `json:"server"`
	Incoming Incoming        `json:"incoming"`
	Trusted  []TrustedDevice `json:"trusted"`
	Timeout  int             `json:"ask_timeout"`
	Update   UpdateState     `json:"update"`
}

// ServerState is the rendezvous server choice.
type ServerState struct {
	Mode string `json:"mode"` // official, custom, none
	Addr string `json:"addr,omitempty"`
	// Chosen: the user made this choice (here, in quick support or with
	// `remohop config`); otherwise none is only the absence of one.
	Chosen bool `json:"chosen"`
}

func clean(s string) string { return cli.Sanitize(auth.CleanText(s, auth.MaxTokenName)) }

func addrOnly(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return ""
	}
	return ap.Addr().String()
}

func (a *App) requestOf(p hostd.Approval, now time.Time) Request {
	left := int((a.cfg.AskTimeout - now.Sub(p.Since) + time.Second - 1) / time.Second)
	a.mu.Lock()
	first := a.firstSeen[p.N]
	a.mu.Unlock()
	r := Request{N: p.N, Name: clean(p.Name), Device: clean(p.Device), Identity: p.Identity, From: addrOnly(p.From),
		Auth: string(p.Auth), Left: max(left, 0), FirstSeen: first, Session: p.Session}
	for _, f := range p.Access {
		r.Access = append(r.Access, string(f))
	}
	return r
}

// State reports what the page shows.
func (a *App) State() State {
	mode, addr := a.serverChoice()
	all, _ := config.AllIn(a.dir)
	chosen := mode != serverNone || config.Bool(all[signal.SettingNone], false)
	a.mu.Lock()
	st := State{Version: cli.Version, Setup: a.settings.Setup, Lang: a.settings.Lang, Theme: a.settings.Theme,
		Server: ServerState{Mode: mode, Addr: addr, Chosen: chosen}, Timeout: int(a.cfg.AskTimeout / time.Second),
		Trusted: append([]TrustedDevice{}, a.settings.Trusted...)}
	in := Incoming{On: a.settings.Incoming, Phase: a.phase, ErrorCode: a.errCode, Error: a.errText, OtherID: a.otherID,
		Requests: []Request{}, Sessions: []Session{}}
	h, started, server := a.host, a.started, a.server
	trusted := map[string]bool{}
	for _, t := range a.settings.Trusted {
		trusted[t.Identity] = true
	}
	a.mu.Unlock()
	if h != nil {
		now := a.cfg.Now()
		hs := h.Status()
		in.Device, in.Online, in.Transport = clean(hs.Name), hs.Online, hs.Transport
		in.Password = hs.Password
		switch {
		case hs.ID != "":
			in.Phase, in.ID = PhaseReady, signal.FormatID(hs.ID)
		case server == "" || a.cfg.NoSignal:
			in.Phase = PhaseNoServer
		default:
			in.Phase = PhaseStarting
			in.Waited = int(now.Sub(started) / time.Second)
		}
		if in.Phase == PhaseNoServer || !in.Online {
			in.Addresses = append(in.Addresses, hs.Direct...)
		}
		for _, p := range h.Pending() {
			in.Requests = append(in.Requests, a.requestOf(p, now))
		}
		for _, s := range h.Sessions() {
			all, fs := s.Features()
			set := featureSet(fs)
			label := clean(s.Auth.ClientName)
			if label == "" {
				label = clean(s.Auth.TokenName)
			}
			if label == "" {
				label = auth.Fingerprint(s.Auth.ClientKey)
			}
			id := auth.Fingerprint(s.Auth.ClientKey)
			in.Sessions = append(in.Sessions, Session{ID: s.ID(), Label: label, Device: clean(s.Auth.ClientName), Identity: id,
				From: addrOnly(s.Remote), Path: s.Path, Seconds: int(now.Sub(s.Started) / time.Second),
				Control: all || set[perm.Input], Files: all || set[perm.File], Trusted: trusted[id]})
		}
		sort.Slice(in.Sessions, func(i, j int) bool { return in.Sessions[i].ID < in.Sessions[j].ID })
	}
	st.Incoming = in
	st.Update = a.updateState()
	return st
}

func featureSet(fs []perm.Feature) map[perm.Feature]bool {
	m := map[perm.Feature]bool{}
	for _, f := range fs {
		m[f] = true
	}
	return m
}
