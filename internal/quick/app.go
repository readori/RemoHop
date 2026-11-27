// Package quick is RemoHop's quick-support experience: `remohop quick` (and the
// RemoHop.exe / RemoHop.app builds of cmd/remohop-quick, which run it when started
// without arguments). A person who was asked to "open RemoHop" double-clicks
// one file and sees this machine's ID and a temporary password, approves
// each incoming connection, sees who is connected and can end it. No
// terminal is involved.
//
// # Design
//
// The process is a normal host (internal/hostd) plus a small UI server on
// 127.0.0.1. The page it serves is the whole user interface; it is opened
// in an app-style browser window where one is available (Microsoft Edge on
// Windows) and in the default browser otherwise. The two things that must
// reach the user even when that window is buried are native: the "allow
// this connection?" question (a system dialog on Windows and macOS, in
// addition to the page) and fatal start-up errors.
//
// Product code has to stay pure Go and cross-compile, so there is no native
// toolkit here: no cgo, no window or tray code that could only be tested on
// the real system. Everything the user sees is exercised by the tests in
// this package, in a real browser where one is installed.
//
// # What a quick-support host allows
//
// Whatever the config file says, a host started here
//
//   - asks for approval on every login, pairings and authorised keys
//     included, and refuses when nobody answers within a minute;
//   - accepts only the temporary password generated for this run (a fixed
//     password in the config file is ignored) and mints no pairings;
//   - offers the screen, and by the user's choice in the page: mouse and
//     keyboard, files and clipboard (both on at start), and the terminal,
//     port forwarding, VPN, serial and USB (off at start). Features switched
//     off with `remohop config feature` stay off.
//
// # Security model of the UI
//
// The UI server follows the loopback mode of internal/web. It listens on
// 127.0.0.1 only; every URL starts with a 128-bit random token, which is
// the login; the Host header must name the loopback address (DNS
// rebinding); a request that says where it comes from (Origin,
// Sec-Fetch-Site) must come from the page itself; and everything that
// changes state is a JSON POST carrying a second secret the page obtains
// over that same checked channel. The UI is never reachable from another
// machine and there is no remote API for approving: a client that is
// waiting for approval has no session, hence no way to move this machine's
// mouse. (A client the user has already allowed and given mouse control
// can click anything on this screen, this page included. That is what
// allowing remote control means.)
package quick

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/svc/desktop"
)

// AskTimeout is how long a connection request waits for the user's answer
// before it is refused. Longer than `remohop host`'s: the person answering is
// not sitting at a terminal waiting for it.
const AskTimeout = 60 * time.Second

// Phases of the app, as the page shows them.
const (
	PhaseSetup      = "setup"      // the rendezvous server has not been chosen yet
	PhaseConnecting = "connecting" // host running, no ID yet
	PhaseReady      = "ready"      // ID known
	PhaseError      = "error"      // the host could not start
	PhaseStopped    = "stopped"    // sharing has ended; the process is about to exit
)

// Error codes of PhaseError.
const (
	ErrAlreadyRunning = "already_running"
	ErrStartFailed    = "start_failed"
)

// Options are the user's switches for what an allowed helper may do.
type Options struct {
	// Input: control the mouse and keyboard (otherwise the helper only watches).
	Input bool `json:"input"`
	// Files: transfer files and share the clipboard.
	Files bool `json:"files"`
	// Advanced: terminal, port forwarding, VPN, serial ports, USB devices.
	Advanced bool `json:"advanced"`
}

// DefaultOptions is what a quick-support host starts with.
func DefaultOptions() Options { return Options{Input: true, Files: true} }

// features is the feature set o allows; anything else is off.
func (o Options) features() map[perm.Feature]bool {
	m := map[perm.Feature]bool{perm.Screen: true, perm.Info: true}
	if o.Input {
		m[perm.Input] = true
	}
	if o.Files {
		m[perm.File], m[perm.Clip] = true, true
	}
	if o.Advanced {
		m[perm.Shell], m[perm.Forward], m[perm.VPN], m[perm.Serial], m[perm.USB] = true, true, true, true, true
	}
	return m
}

// Request is a connection waiting for the user's answer.
type Request struct {
	N int `json:"n"`
	// Name is the pairing or authorised-key name the host knows the
	// client by; empty for a password login.
	Name string `json:"name,omitempty"`
	// Device is what the client calls itself: a claim, not verified.
	Device string `json:"device,omitempty"`
	// Identity is the fingerprint of the client's key.
	Identity string `json:"identity"`
	From     string `json:"from"`
	Auth     string `json:"auth"`
	// Action is set when this is not a login but something a connected,
	// granted client asks to do ("exec: git push"); Grant names its grant.
	Action string `json:"action,omitempty"`
	Grant  string `json:"grant,omitempty"`
	// Left is the number of seconds until the request is refused.
	Left int `json:"left"`
}

// SessionInfo is a connected helper.
type SessionInfo struct {
	ID       int    `json:"id"`
	Name     string `json:"name,omitempty"`
	Device   string `json:"device,omitempty"`
	Identity string `json:"identity"`
	From     string `json:"from"`
	// Path is lan, direct, punched, relayed or "relayed (websocket)".
	Path string `json:"path"`
	// Seconds is how long the session has been open.
	Seconds int `json:"seconds"`
}

// State is everything the page shows.
type State struct {
	Phase     string `json:"phase"`
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
	// ID is the 9-digit ID formatted in groups of three; OtherID is the ID
	// of the host that is already running (ErrAlreadyRunning).
	ID       string `json:"id,omitempty"`
	OtherID  string `json:"other_id,omitempty"`
	Password string `json:"password,omitempty"`
	// Online: registered with the rendezvous server right now.
	Online bool   `json:"online"`
	Server string `json:"server,omitempty"`
	// Transport is "udp" or "websocket" (helpers then arrive relayed).
	Transport string `json:"transport,omitempty"`
	// Relay: this machine accepts a relayed connection when no direct
	// one is possible.
	Relay bool `json:"relay"`
	// Waited is how many seconds the host has been trying to register.
	Waited int `json:"waited,omitempty"`
	// Addresses are where helpers on this network reach this computer
	// when it runs without a rendezvous server (the user chose none).
	// (null when it has a server; empty when none was found).
	Addresses  []string      `json:"addresses"`
	Device     string        `json:"device,omitempty"`
	Requests   []Request     `json:"requests"`
	Sessions   []SessionInfo `json:"sessions"`
	Options    Options       `json:"options"`
	AskTimeout int           `json:"ask_timeout"`
	Version    string        `json:"version"`
	// NoFFmpeg: this computer has no ffmpeg, so helpers cannot see the
	// screen live (screenshots, files and commands still work).
	NoFFmpeg bool `json:"no_ffmpeg,omitempty"`
}

// AppConfig configures an App.
type AppConfig struct {
	// Dir is the remohop config directory; empty means the default.
	Dir string
	// Signal overrides the configured rendezvous server.
	Signal string
	// AskTimeout defaults to the package's AskTimeout.
	AskTimeout time.Duration
	// Prompt, if set, also asks the user about a request outside the page
	// (a native dialog). It returns ok false when it has no answer: the
	// dialog could not be shown, or ctx ended because the request was
	// answered elsewhere or went away.
	Prompt func(ctx context.Context, r Request) (allow, ok bool)
	Logger *slog.Logger

	// Host adjusts the host configuration before it starts; tests bind
	// loopback and run without a server with it.
	Host func(*hostd.Config)
	// NoSignal runs without a rendezvous server (tests).
	NoSignal bool
}

// App runs the quick-support host and answers the UI.
type App struct {
	cfg  AppConfig
	log  *slog.Logger
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}

	// configMu makes Configure calls one at a time.
	configMu sync.Mutex

	mu       sync.Mutex
	phase    string
	errCode  string
	errText  string
	otherID  string
	noFFmpeg bool
	host     *hostd.Host
	hostDone chan struct{}
	// hostStop stops the running host alone (Configure restarts it).
	hostStop context.CancelFunc
	started  time.Time
	server   string
	opts     Options
	subs     map[chan struct{}]struct{}
	prompts  map[int]bool
}

// NewApp prepares an App; Start brings the host up.
func NewApp(cfg AppConfig) *App {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.AskTimeout <= 0 {
		cfg.AskTimeout = AskTimeout
	}
	a := &App{cfg: cfg, log: cfg.Logger, done: make(chan struct{}), phase: PhaseConnecting, opts: DefaultOptions(),
		subs: map[chan struct{}]struct{}{}, prompts: map[int]bool{}}
	a.ctx, a.stop = context.WithCancel(context.Background())
	return a
}

// Done is closed when sharing has stopped for good.
func (a *App) Done() <-chan struct{} { return a.done }

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
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *App) fail(code string, err error) {
	a.mu.Lock()
	a.phase, a.errCode, a.errText = PhaseError, code, err.Error()
	a.mu.Unlock()
	a.log.Error("quick support could not start", "code", code, "err", err)
	a.wake()
}

// Start resolves the rendezvous server and starts the host. The outcome is
// in State: the setup phase when no server is configured, the error phase
// when the host cannot start.
func (a *App) Start() {
	hc := hostd.Config{
		Dir:        a.cfg.Dir,
		AskTimeout: a.cfg.AskTimeout,
		NoPairing:  true,
		Control:    true,
		Logger:     a.log,
		OnApproval: a.onApproval,
		Overrides:  &hostd.Overrides{AskAll: true, TempPasswordOnly: true, Features: a.features},
	}
	if !a.cfg.NoSignal {
		addr, opts, err := signal.Resolve(a.cfg.Signal)
		if errors.Is(err, signal.ErrNotConfigured) && !a.noServerChosen() {
			a.mu.Lock()
			a.phase = PhaseSetup
			a.mu.Unlock()
			a.wake()
			return
		}
		if errors.Is(err, signal.ErrNotConfigured) {
			// The user chose no server (here or in the desktop app, which
			// shares the setting): helpers reach this computer by address.
			addr, err = "", nil
		}
		if err == nil && addr != "" {
			_, _, err = signal.SplitAddr(addr)
		}
		if err != nil {
			a.fail(ErrStartFailed, err)
			return
		}
		if addr == "" && opts.WS != "" {
			addr = signal.Display("", opts) // only the websocket bridge is configured
		}
		hc.Signal, hc.SignalOptions = addr, opts
	}
	if hc.Dir == "" {
		if settings, err := config.All(); err == nil {
			hc.Listen = settings[hostd.SettingListen]
		}
	}
	if a.cfg.Host != nil {
		a.cfg.Host(&hc)
	}
	h, release, err := hostd.StartExclusive(hc)
	if err != nil {
		if e, ok := errors.AsType[*hostd.AlreadyRunningError](err); ok {
			a.mu.Lock()
			a.otherID = signal.FormatID(e.Status.ID)
			a.mu.Unlock()
			a.fail(ErrAlreadyRunning, err)
			return
		}
		a.fail(ErrStartFailed, err)
		return
	}
	h.OnChange(a.wake)
	hostDone := make(chan struct{})
	hctx, hstop := context.WithCancel(a.ctx)
	ok, _ := desktop.FFmpegInstalled()
	a.mu.Lock()
	a.host, a.hostDone, a.hostStop, a.started, a.server = h, hostDone, hstop, time.Now(), hc.Signal
	a.phase, a.errCode, a.errText = PhaseConnecting, "", ""
	a.noFFmpeg = !ok
	a.mu.Unlock()
	a.wake()
	go func() {
		defer close(hostDone)
		err := h.Run(hctx)
		release()
		if hctx.Err() == nil {
			if err == nil {
				err = errors.New("the host stopped unexpectedly")
			}
			a.mu.Lock()
			if a.host == h {
				a.host = nil
			}
			a.mu.Unlock()
			a.fail(ErrStartFailed, err)
		}
	}()
}

// stopHost stops the running host, if any, and waits until it has let go
// of everything (its lock, its sockets), so that Start can run again.
func (a *App) stopHost() {
	a.mu.Lock()
	stop, done := a.hostStop, a.hostDone
	a.host, a.hostStop, a.hostDone = nil, nil, nil
	a.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// noServerChosen reports whether the user chose to use no rendezvous server
// (signal.SettingNone, shared with the desktop app).
func (a *App) noServerChosen() bool {
	dir := a.cfg.Dir
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			return false
		}
		dir = d
	}
	s, _ := config.AllIn(dir)
	return config.Bool(s[signal.SettingNone], false)
}

// features is the host's view of the options (hostd.Overrides.Features).
func (a *App) features() map[perm.Feature]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.features()
}

func (a *App) running() (*hostd.Host, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.host == nil || a.phase == PhaseStopped {
		return nil, errors.New("sharing is not running")
	}
	return a.host, nil
}

func requestOf(p hostd.Approval, timeout time.Duration, now time.Time) Request {
	left := int((timeout - now.Sub(p.Since) + time.Second - 1) / time.Second)
	return Request{N: p.N, Name: clean(p.Name), Device: clean(p.Device), Identity: p.Identity,
		From: addrOnly(p.From), Auth: string(p.Auth), Action: cli.Sanitize(auth.CleanText(p.Action, 200)), Grant: clean(p.Grant), Left: max(left, 0)}
}

// clean makes text chosen by the other side safe to show in one line.
func clean(s string) string { return cli.Sanitize(auth.CleanText(s, auth.MaxTokenName)) }

// addrOnly drops the port: it means nothing to the person reading.
func addrOnly(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return ""
	}
	return ap.Addr().String()
}

// State reports what the page shows.
func (a *App) State() State {
	a.mu.Lock()
	st := State{Phase: a.phase, ErrorCode: a.errCode, Error: a.errText, OtherID: a.otherID, Server: a.server,
		Options: a.opts, AskTimeout: int(a.cfg.AskTimeout / time.Second), Version: cli.Version,
		Requests: []Request{}, Sessions: []SessionInfo{}, NoFFmpeg: a.noFFmpeg}
	h, started := a.host, a.started
	a.mu.Unlock()
	if h == nil || st.Phase == PhaseStopped || st.Phase == PhaseError {
		return st
	}
	now := time.Now()
	hs := h.Status()
	st.Device = hs.Name
	st.Online, st.Transport = hs.Online, hs.Transport
	st.Relay = signal.RelayAllowed(a.cfg.Dir)
	st.Password = hs.Password
	if hs.ID != "" || a.cfg.NoSignal || st.Server == "" {
		st.Phase = PhaseReady
		st.ID = signal.FormatID(hs.ID)
		if st.Server == "" && !a.cfg.NoSignal {
			st.Addresses = append([]string{}, hs.Direct...)
		}
	} else {
		st.Waited = int(now.Sub(started) / time.Second)
	}
	for _, p := range h.Pending() {
		st.Requests = append(st.Requests, requestOf(p, a.cfg.AskTimeout, now))
	}
	for _, s := range h.Sessions() {
		st.Sessions = append(st.Sessions, SessionInfo{ID: s.ID(), Name: clean(s.Auth.TokenName), Device: clean(s.Auth.ClientName),
			Identity: auth.Fingerprint(s.Auth.ClientKey), From: addrOnly(s.Remote), Path: s.Path(),
			Seconds: int(now.Sub(s.Started) / time.Second)})
	}
	sort.Slice(st.Sessions, func(i, j int) bool { return st.Sessions[i].ID < st.Sessions[j].ID })
	return st
}

// onApproval runs for each login that starts waiting. It wakes the page
// and, where there is one, puts the native question on screen; whichever is
// answered first decides.
func (a *App) onApproval(p hostd.Approval) {
	a.wake()
	if a.cfg.Prompt == nil {
		return
	}
	h, err := a.running()
	if err != nil {
		return
	}
	a.mu.Lock()
	// One native dialog at a time: a stack of them trains people to click
	// through. Further requests wait in the page.
	busy := len(a.prompts) > 0
	if !busy {
		a.prompts[p.N] = true
	}
	a.mu.Unlock()
	if busy {
		return
	}
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.prompts, p.N)
			a.mu.Unlock()
		}()
		ctx, cancel := context.WithCancel(a.ctx)
		defer cancel()
		go func() {
			// The dialog goes away when the request does.
			t := time.NewTicker(200 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
				if !pending(h, p.N) {
					cancel()
					return
				}
			}
		}()
		allow, ok := a.cfg.Prompt(ctx, requestOf(p, a.cfg.AskTimeout, time.Now()))
		if !ok || ctx.Err() != nil {
			return
		}
		// By number, never "the oldest": an answer must not land on a
		// request the user did not see.
		if err := h.Decide(p.N, allow); err == nil {
			a.log.Info("connection request answered in the native dialog", "n", p.N, "allow", allow)
		}
		a.wake()
	}()
}

func pending(h *hostd.Host, n int) bool {
	for _, p := range h.Pending() {
		if p.N == n {
			return true
		}
	}
	return false
}

// Regenerate replaces the temporary password. Sessions stay; whoever has
// the old password can no longer log in with it.
func (a *App) Regenerate() error {
	h, err := a.running()
	if err != nil {
		return err
	}
	return h.RotatePassword()
}

// Decide answers connection request n.
func (a *App) Decide(n int, allow bool) error {
	h, err := a.running()
	if err != nil {
		return err
	}
	if n <= 0 {
		return errors.New("no such connection request")
	}
	err = h.Decide(n, allow)
	a.wake()
	return err
}

// Disconnect ends session id.
func (a *App) Disconnect(id int) error {
	h, err := a.running()
	if err != nil {
		return err
	}
	err = h.Disconnect(id, hostd.ReasonDisconnectedAtHost)
	a.wake()
	return err
}

// SetOption switches one of Options ("input", "files", "advanced"). It
// applies at once, also to helpers already connected, before SetOption
// returns: nothing new of what is switched off can be opened, and what
// they have open with it ends (terminals, file transfers, forwards, ...;
// see hostd.WatchFeature). Switching "input" off leaves the screen
// showing: an open desktop stream lets go of any key or button the helper
// holds, takes no more input and tells the helper's viewer so.
func (a *App) SetOption(name string, on bool) error {
	a.mu.Lock()
	switch name {
	case "input":
		a.opts.Input = on
	case "files":
		a.opts.Files = on
	case "advanced":
		a.opts.Advanced = on
	default:
		a.mu.Unlock()
		return fmt.Errorf("unknown option %q", name)
	}
	h := a.host
	a.mu.Unlock()
	if h != nil {
		h.PolicyChanged()
	}
	a.log.Info("option changed", "option", name, "on", on)
	a.wake()
	return nil
}

// Configure stores the user's choice of rendezvous server and starts the
// host with it: mode "official" (signal.OfficialAddr), "custom" (server,
// as typed; also the default for an empty mode) or "none" (no server:
// helpers on this network connect by address). The choice is the same
// setting the desktop app and `remohop config` use, so it is asked once.
//
// It is accepted in the setup phase, and also while the host is still
// trying to reach its server (or could not start), so that a mistyped
// address can be corrected: the host is then restarted with the new
// choice, which also wins over a --signal given at start. Once the host
// is registered, the choice stays.
func (a *App) Configure(mode, server string) error {
	a.configMu.Lock()
	defer a.configMu.Unlock()
	a.mu.Lock()
	phase, code := a.phase, a.errCode
	a.mu.Unlock()
	if st := a.State(); st.Phase == PhaseReady {
		phase = PhaseReady
	}
	switch {
	case phase == PhaseSetup, phase == PhaseConnecting, phase == PhaseError && code == ErrStartFailed:
	default:
		return errors.New("a server is already configured")
	}
	none := false
	switch mode {
	case "official":
		server = signal.OfficialAddr
	case "none":
		none = true
	case "", "custom":
		if server == "" {
			return &codedError{code: "no_server", msg: "enter the server address"}
		}
		if err := checkServer(server); err != nil {
			return &codedError{code: "bad_server", msg: err.Error()}
		}
	default:
		return &codedError{code: "bad_request", msg: "unknown server choice"}
	}
	if phase != PhaseSetup {
		a.stopHost()
		a.cfg.Signal = ""
		a.log.Info("rendezvous server changed in the window", "mode", mode)
	}
	if none {
		if err := config.Set(signal.SettingNone, "on"); err != nil {
			return err
		}
		if phase != PhaseSetup {
			// The address chosen before would win over "none".
			config.Set(signal.SettingAddr, "")
		}
		a.Start()
		return nil
	}
	if err := config.Set(signal.SettingAddr, server); err != nil {
		return err
	}
	config.Set(signal.SettingNone, "")
	a.Start()
	return nil
}

// Stop ends sharing: every session is closed and the host goes offline.
// It returns once that has happened.
func (a *App) Stop() {
	a.mu.Lock()
	already := a.phase == PhaseStopped
	a.phase = PhaseStopped
	hostDone := a.hostDone
	a.mu.Unlock()
	a.stop()
	if hostDone != nil {
		<-hostDone
	}
	if !already {
		a.log.Info("sharing stopped")
		close(a.done)
	}
	a.wake()
}

// codedError is a refusal the page explains in its own language: code
// names it (the page's err_<code> text), the message is the English one.
type codedError struct{ code, msg string }

func (e *codedError) Error() string { return e.msg }

// checkServer is stricter than the config file's own validation: what a
// person types into the window is a host name or an address, optionally
// with a port, and a typo should be caught here rather than show up as a
// server that never answers.
func checkServer(server string) error {
	host, port, err := signal.SplitAddr(server)
	if err != nil {
		return err
	}
	bad := fmt.Errorf("%q is not a server address; it looks like support.example.com or support.example.com:21116", server)
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return bad
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") {
		return bad
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return bad
		}
	}
	return nil
}
