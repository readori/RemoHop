package mobile

// Host mode: the phone itself can be viewed, and with its owner's consent
// controlled, from any RemoHop client (the desktop app, the browser
// console, the CLI, another phone).
//
// It runs an in-process host daemon (internal/hostd) with the policy of the
// desktop app's "This computer" and of quick support:
//
//   - every login waits for the owner's answer on the phone (AskAll), and
//     is refused when nobody answers in HostAskTimeout;
//   - only the generated temporary password logs in (TempPasswordOnly), and
//     no pairing token is issued (NoPairing), so a controller cannot come
//     back later on its own;
//   - an allowed login sees the screen (and basic system information);
//     controlling it is a separate tick in the approval (DecideHostRequest).
//     Nothing else a desktop host offers (shell, files, port forwarding, ...)
//     is served at all: the phone's host serves its own short list of
//     services (phoneServices).
//
// The host shares this device's identity with the controller side (one
// device, one key, one ID) and registers with the rendezvous server the
// app is configured with; without one it is reachable by address on the
// local network (HostStateJSON lists the addresses).
//
// Screen and input are native: the app hands the core a ScreenSource
// (Android: MediaProjection into a hardware H.264 MediaCodec encoder)
// that pushes its output back with PushScreenFrame, and an InputInjector
// (Android: an accessibility service the owner enables). Both are
// platform-neutral, so iOS can feed the same interface (view-only, with
// ReplayKit). The core speaks the desktop wire protocol to the viewers
// (internal/svc/desktop's Screen), so every client works unchanged.
//
// The app learns what happens from a HostListener: HostChanged with the
// whole state (HostStateJSON's document) whenever it changes, and
// HostRequest when a request starts waiting, for the approval dialog.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/svc/desktop"
)

// HostAskTimeout is how long a connection request waits for the owner.
const HostAskTimeout = 60 * time.Second

// Phases of host mode (HostStateJSON "phase").
const (
	hostOff      = "off"       // not running
	hostStarting = "starting"  // running, not registered with the server yet
	hostReady    = "ready"     // reachable by ID (online says whether registered right now)
	hostNoServer = "no_server" // running without a rendezvous server: by address only
	hostError    = "error"     // could not start, or stopped by itself
)

// HostListener receives host-mode events. The app implements it; its
// methods are called from the core's goroutines and must return quickly.
type HostListener interface {
	// HostChanged reports the whole state, as HostStateJSON returns it,
	// whenever something in it changed (at most a few times a second; once
	// a second while a request waits, for its countdown).
	HostChanged(state string)
	// HostRequest reports a request that has just started waiting for the
	// owner: one entry of the state's "requests".
	HostRequest(request string)
}

// ScreenSource is the native screen capture and encoder. See
// internal/svc/desktop.ScreenSource, which it is.
type ScreenSource interface {
	// StartScreen starts capturing and encoding (or applies new limits):
	// long edge at most maxSize pixels, at most fps frames per second,
	// bitrate bits per second. The output comes back via PushScreenFrame.
	// An error says why the screen cannot be captured right now.
	StartScreen(maxSize, fps int, bitrate int64) error
	// StopScreen stops capturing (nobody watches).
	StopScreen()
	// RequestKeyframe asks for an IDR frame soon.
	RequestKeyframe()
	// SetBitrate changes the target bitrate of the running encoder.
	SetBitrate(bitrate int64)
}

// InputInjector delivers a controller's input on the device (Android: the
// accessibility service). Coordinates are fractions of the shared picture,
// 0..1 from its top-left corner, whatever its size or rotation: mapping
// them onto the display is the injector's job.
type InputInjector interface {
	// Unavailable says why input cannot be delivered now ("" when it can),
	// e.g. that the accessibility service is off.
	Unavailable() string
	// Move moves the pointer (a finger already down drags).
	Move(x, y float64) error
	// Button presses or releases a mouse button (DOM numbering: 0 left,
	// 1 middle, 2 right, 3 back, 4 forward) at x, y.
	Button(button int, down bool, x, y float64) error
	// Wheel scrolls by dx, dy notches (positive: right, down) where the
	// pointer is.
	Wheel(dx, dy float64) error
	// Key presses or releases a key named by its DOM KeyboardEvent.code.
	Key(code string, down bool) error
	// TypeText enters text into the focused field.
	TypeText(text string) error
	// ReleaseAll lets go of everything held.
	ReleaseAll()
}

var (
	hostMu       sync.Mutex
	hostRun      *hostState
	hostListener HostListener
	hostPhase    = hostOff
	hostErr      string
	nativeScreen ScreenSource
	nativeInput  InputInjector
	// hostConfigHook lets tests adjust the host's configuration.
	hostConfigHook func(*hostd.Config)
)

// hostState is one run of the host.
type hostState struct {
	h       *hostd.Host
	screen  *desktop.Screen
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
	server  string
	wake    chan struct{}

	mu        sync.Mutex
	firstSeen map[int]bool // approval N -> the device never asked before
}

// SetHostListener sets (or with nil clears) the receiver of host events.
func SetHostListener(l HostListener) {
	hostMu.Lock()
	hostListener = l
	run := hostRun
	hostMu.Unlock()
	if run != nil {
		run.poke()
	}
}

// SetScreenSource sets (nil clears) the native screen capture.
func SetScreenSource(s ScreenSource) {
	hostMu.Lock()
	nativeScreen = s
	hostMu.Unlock()
}

// SetInputInjector sets (nil clears) the native input injector.
func SetInputInjector(i InputInjector) {
	hostMu.Lock()
	nativeInput = i
	hasInput.Store(i != nil)
	hostMu.Unlock()
}

// PushScreenFrame hands the core one buffer of the native encoder's
// output: H.264 in Annex B form, the parameter sets alone (codec config)
// or an access unit (with or without the parameter sets in front).
// captureMicros is when the picture was captured, in Unix microseconds
// (0 if unknown). The bytes are copied.
func PushScreenFrame(data []byte, captureMicros int64) error {
	hostMu.Lock()
	run := hostRun
	hostMu.Unlock()
	if run == nil {
		return errors.New("mobile: host mode is off")
	}
	return run.screen.PushFrame(data, captureMicros)
}

// SetScreenInfo reports the screen's size in pixels and rotation in
// degrees (0, 90, 180, 270) whenever it changes.
func SetScreenInfo(width, height, rotation int) {
	hostMu.Lock()
	screenInfo = [3]int{width, height, rotation}
	run := hostRun
	hostMu.Unlock()
	if run != nil {
		run.screen.SetScreenInfo(width, height, rotation)
	}
}

var screenInfo [3]int

// screenProxy is the Screen's source: whatever native source is set now.
type screenProxy struct{}

func (screenProxy) src() ScreenSource {
	hostMu.Lock()
	defer hostMu.Unlock()
	return nativeScreen
}

func (p screenProxy) StartScreen(maxSize, fps int, bitrate int64) error {
	s := p.src()
	if s == nil {
		return errors.New("screen sharing is not available on this device")
	}
	return s.StartScreen(maxSize, fps, bitrate)
}

func (p screenProxy) StopScreen() {
	if s := p.src(); s != nil {
		s.StopScreen()
	}
}

func (p screenProxy) RequestKeyframe() {
	if s := p.src(); s != nil {
		s.RequestKeyframe()
	}
}

func (p screenProxy) SetBitrate(b int64) {
	if s := p.src(); s != nil {
		s.SetBitrate(b)
	}
}

// inputProxy is the Screen's input sink: the native injector set now.
type inputProxy struct{}

func (inputProxy) inj() InputInjector {
	hostMu.Lock()
	defer hostMu.Unlock()
	return nativeInput
}

func (p inputProxy) Unavailable() string {
	i := p.inj()
	if i == nil {
		return "this device cannot be controlled, only viewed"
	}
	return cli.Sanitize(i.Unavailable())
}

func (p inputProxy) Inject(ev desktop.InputEvent) error {
	i := p.inj()
	if i == nil {
		return errors.New("no input injector")
	}
	switch ev.Kind {
	case desktop.InputMove:
		return i.Move(ev.X, ev.Y)
	case desktop.InputButton:
		return i.Button(ev.Button, ev.Down, ev.X, ev.Y)
	case desktop.InputWheel:
		return i.Wheel(ev.DX, ev.DY)
	case desktop.InputKey:
		return i.Key(ev.Code, ev.Down)
	case desktop.InputReleaseAll:
		i.ReleaseAll()
		return nil
	}
	return fmt.Errorf("unknown input event %q", ev.Kind)
}

func (p inputProxy) TypeText(text string) error {
	if i := p.inj(); i != nil {
		return i.TypeText(text)
	}
	return errors.New("no input injector")
}

func (p inputProxy) ReleaseAll() {
	if i := p.inj(); i != nil {
		i.ReleaseAll()
	}
}

// offeredFeatures are what the phone's host can offer at all: the screen
// and system information, and control only while an input injector is set
// (iOS sets none: its host is view-only, and control is not even offered,
// so viewers are not invited to ask for it).
func offeredFeatures() map[perm.Feature]bool {
	m := map[perm.Feature]bool{perm.Screen: true, perm.Info: true}
	if controlOffered() {
		m[perm.Input] = true
	}
	return m
}

// controlOffered reports whether an input injector is set. Lock-free: the
// host's policy asks from its own goroutines, StartHost among them.
func controlOffered() bool { return hasInput.Load() }

var hasInput atomic.Bool

// Screen options (SetScreenOptions), applied when host mode starts.
var (
	screenEncoder = "mediacodec"
	screenMaxFPS  = 60
)

// SetScreenOptions names the native video encoder, as viewers see it (e.g.
// "mediacodec" on Android, the default, or "videotoolbox" on iOS), and caps
// the frame rate the core asks of it and plans bitrates for (1..60; 0
// keeps 60). It applies from the next StartHost.
func SetScreenOptions(encoder string, maxFPS int) {
	hostMu.Lock()
	defer hostMu.Unlock()
	if e := cli.Sanitize(auth.CleanText(encoder, 32)); e != "" {
		screenEncoder = e
	}
	switch {
	case maxFPS <= 0 || maxFPS > 60:
		screenMaxFPS = 60
	default:
		screenMaxFPS = maxFPS
	}
}

// phoneServices is the phone host's registry: the provided screen as the
// desktop service, system information, and the host's own housekeeping
// services. Everything else the process happens to link (the controller
// side links the shell, file and clipboard packages) is not served.
func phoneServices(screen *desktop.Screen) *peer.Registry {
	keep := map[string]bool{hostd.ServicePing: true, hostd.ServicePair: true, hostd.ServiceLimits: true, hostd.ServiceAccess: true, "sysinfo": true}
	reg := peer.HostServices.Wrapped(func(name string, h peer.Handler) peer.Handler {
		if keep[name] {
			return h
		}
		return nil
	})
	reg.Handle(desktop.ServiceName, screen.Serve)
	return reg
}

// StartHost starts host mode. It is safe to call while it runs.
func StartHost() error {
	d, err := configDir()
	if err != nil {
		return err
	}
	hostMu.Lock()
	defer hostMu.Unlock()
	if hostRun != nil {
		return nil
	}
	log := quietLogger()
	screen := desktop.NewScreen(desktop.ScreenConfig{Source: screenProxy{}, Input: inputProxy{}, Encoder: screenEncoder, MaxFPS: screenMaxFPS, Logger: log})
	screen.SetScreenInfo(screenInfo[0], screenInfo[1], screenInfo[2])
	run := &hostState{screen: screen, started: time.Now(), wake: make(chan struct{}, 1), firstSeen: map[int]bool{}}
	hc := hostd.Config{
		Dir:        d,
		AskTimeout: HostAskTimeout,
		NoPairing:  true,
		Logger:     log,
		OnApproval: run.onApproval,
		Services:   phoneServices(screen),
		Locals:     localAddrs,
		Password:   ownHostPassword(d),
		Overrides: &hostd.Overrides{AskAll: true, TempPasswordOnly: true, Features: func() map[perm.Feature]bool {
			return offeredFeatures()
		}},
	}
	addr, opts, err := signal.Resolve("")
	switch {
	case errors.Is(err, signal.ErrNotConfigured):
		// No server: reachable by address only.
	case err != nil:
		return hostFailLocked(err)
	default:
		if addr == "" {
			addr = signal.Display("", opts)
		}
		hc.Signal, hc.SignalOptions = addr, opts
	}
	if hostConfigHook != nil {
		hostConfigHook(&hc)
	}
	h, err := hostd.New(hc)
	if err != nil {
		return hostFailLocked(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run.h, run.cancel, run.done, run.server = h, cancel, make(chan struct{}), hc.Signal
	h.OnChange(run.poke)
	hostRun, hostPhase, hostErr = run, hostStarting, ""
	go func() {
		defer close(run.done)
		err := h.Run(ctx)
		screen.Close()
		hostMu.Lock()
		if hostRun == run {
			hostRun = nil
			if ctx.Err() == nil {
				if err == nil {
					err = errors.New("the host stopped unexpectedly")
				}
				hostPhase, hostErr = hostError, cli.Sanitize(err.Error())
				log.Error("host mode stopped", "err", err)
			}
		}
		hostMu.Unlock()
		notifyHost(nil)
	}()
	go run.watch(ctx)
	log.Info("host mode on", "server", hc.Signal)
	return nil
}

func hostFailLocked(err error) error {
	hostPhase, hostErr = hostError, cli.Sanitize(err.Error())
	quietLogger().Error("host mode could not start", "err", err)
	go notifyHost(nil)
	return err
}

// StopHost stops host mode and ends every session. Safe when it is off.
func StopHost() {
	hostMu.Lock()
	run := hostRun
	hostRun, hostPhase, hostErr = nil, hostOff, ""
	hostMu.Unlock()
	if run != nil {
		run.cancel()
		<-run.done
		quietLogger().Info("host mode off")
	}
	notifyHost(nil)
}

// HostRunning reports whether host mode runs.
func HostRunning() bool {
	hostMu.Lock()
	defer hostMu.Unlock()
	return hostRun != nil
}

func runningHost() (*hostState, error) {
	hostMu.Lock()
	defer hostMu.Unlock()
	if hostRun == nil {
		return nil, errors.New("mobile: host mode is off")
	}
	return hostRun, nil
}

func (r *hostState) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// watch sends HostChanged whenever the state document changes.
func (r *hostState) watch(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := ""
	for {
		if s, err := HostStateJSON(); err == nil && s != last {
			last = s
			notifyHost(&s)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			// Coalesce bursts (a login changes several things at once).
			time.Sleep(50 * time.Millisecond)
		case <-t.C:
		}
	}
}

// notifyHost calls the listener's HostChanged with state (computed if nil).
func notifyHost(state *string) {
	hostMu.Lock()
	l := hostListener
	hostMu.Unlock()
	if l == nil {
		return
	}
	if state == nil {
		s, err := HostStateJSON()
		if err != nil {
			return
		}
		state = &s
	}
	l.HostChanged(*state)
}

// onApproval runs for every login (and access request) that waits for the
// owner.
func (r *hostState) onApproval(p hostd.Approval) {
	login := len(p.Access) == 0 && p.Action == ""
	offered := offeredFeatures()
	if !login && !slices.ContainsFunc(p.Access, func(f perm.Feature) bool { return offered[f] }) {
		// Asks only for what the phone does not offer at all.
		go r.h.Decide(p.N, false)
		return
	}
	if login {
		first := markSeen(p.Identity)
		r.mu.Lock()
		r.firstSeen[p.N] = first
		r.mu.Unlock()
	}
	req := r.requestOf(p, time.Now())
	hostMu.Lock()
	l := hostListener
	hostMu.Unlock()
	if l != nil {
		b, _ := json.Marshal(req)
		go l.HostRequest(string(b))
	}
	r.poke()
}

// hostRequest is a request as HostStateJSON lists it.
type hostRequest struct {
	N        int    `json:"n"`
	Name     string `json:"name,omitempty"`
	Device   string `json:"device,omitempty"`
	Identity string `json:"identity"`
	From     string `json:"from,omitempty"`
	Auth     string `json:"auth"`
	// Left is how many seconds remain before it is refused.
	Left int `json:"left"`
	// FirstSeen: this device never asked before.
	FirstSeen bool `json:"first_seen"`
	// Access is set for a connected session that asks for more
	// ("input"); Session is its ID.
	Access  []string `json:"access,omitempty"`
	Session int      `json:"session,omitempty"`
}

// hostSession is a connected device.
type hostSession struct {
	ID       int    `json:"id"`
	Label    string `json:"label"`
	Device   string `json:"device,omitempty"`
	Identity string `json:"identity"`
	From     string `json:"from,omitempty"`
	Path     string `json:"path"`
	Seconds  int    `json:"seconds"`
	Control  bool   `json:"control"`
}

// hostDoc is HostStateJSON's document.
type hostDoc struct {
	On       bool   `json:"on"`
	Phase    string `json:"phase"`
	Error    string `json:"error,omitempty"`
	ID       string `json:"id,omitempty"`
	Password string `json:"password,omitempty"`
	// PasswordMode: "temporary" (new at every start) or "fixed" (the
	// owner's own, SetHostFixedPassword); Password is the one in force.
	PasswordMode string   `json:"password_mode"`
	Online       bool     `json:"online"`
	Server       string   `json:"server,omitempty"`
	Addresses    []string `json:"addresses,omitempty"`
	Waited       int      `json:"waited,omitempty"`
	Device       string   `json:"device,omitempty"`
	Viewers      int      `json:"viewers"`
	Control      bool     `json:"control_available"`
	NoControl    string   `json:"control_unavailable,omitempty"`
	// Offered: the phone offers control at all (an input injector is set);
	// without, the approval should not show a control choice.
	Offered  bool          `json:"control_offered"`
	Timeout  int           `json:"ask_timeout"`
	Requests []hostRequest `json:"requests"`
	Sessions []hostSession `json:"sessions"`
}

func (r *hostState) requestOf(p hostd.Approval, now time.Time) hostRequest {
	left := int((HostAskTimeout - now.Sub(p.Since) + time.Second - 1) / time.Second)
	r.mu.Lock()
	first := r.firstSeen[p.N]
	r.mu.Unlock()
	q := hostRequest{N: p.N, Name: cleanLabel(p.Name), Device: cleanLabel(p.Device), Identity: p.Identity, From: addrOnly(p.From),
		Auth: string(p.Auth), Left: max(left, 0), FirstSeen: first, Session: p.Session}
	for _, f := range p.Access {
		q.Access = append(q.Access, string(f))
	}
	return q
}

func cleanLabel(s string) string { return cli.Sanitize(auth.CleanText(s, auth.MaxTokenName)) }

func addrOnly(ap netip.AddrPort) string {
	if !ap.IsValid() {
		return ""
	}
	return ap.Addr().String()
}

// HostStateJSON returns host mode's state as one JSON object:
//
//	on, phase (off, starting, ready, no_server, error), error,
//	id (formatted "123 456 789"), password (the password in force:
//	the temporary one, or the owner's own), password_mode ("temporary"
//	or "fixed": SetHostFixedPassword, also while host mode is off),
//	online (registered with the server right now), server, addresses
//	(ip:port this device is reachable at on its networks: shown when
//	there is no server or the host is offline), waited (seconds since
//	start while starting), device (this device's name), viewers (open
//	screen streams), control_available and control_unavailable (whether
//	the input injector works, and why not), control_offered (an
//	injector is set: offer control at all), ask_timeout (seconds),
//	requests [{n, name, device, identity, from, auth, left, first_seen,
//	access, session}] and sessions [{id, label, device, identity, from,
//	path, seconds, control}].
func HostStateJSON() (string, error) {
	hostMu.Lock()
	run, phase, herr := hostRun, hostPhase, hostErr
	hostMu.Unlock()
	doc := hostDoc{Phase: phase, Error: herr, Timeout: int(HostAskTimeout / time.Second), Requests: []hostRequest{}, Sessions: []hostSession{}, PasswordMode: "temporary"}
	own := ""
	if d, err := configDir(); err == nil {
		own = ownHostPassword(d)
	}
	if own != "" {
		doc.PasswordMode = "fixed"
	}
	why := inputProxy{}.Unavailable()
	doc.Control, doc.NoControl, doc.Offered = why == "", why, controlOffered()
	if run != nil {
		now := time.Now()
		hs := run.h.Status()
		doc.On, doc.Online, doc.Password, doc.Server = true, hs.Online, hs.Password, run.server
		if own != "" && run.h.PasswordMode() == "fixed" {
			doc.Password = own
		}
		doc.Device = pairLabel()
		doc.Viewers = run.screen.Viewers()
		switch {
		case hs.ID != "":
			doc.Phase, doc.ID = hostReady, signal.FormatID(hs.ID)
		case run.server == "":
			doc.Phase = hostNoServer
		default:
			doc.Phase = hostStarting
			doc.Waited = int(now.Sub(run.started) / time.Second)
		}
		if doc.Phase == hostNoServer || !doc.Online {
			doc.Addresses = append(doc.Addresses, hs.Direct...)
		}
		for _, p := range run.h.Pending() {
			doc.Requests = append(doc.Requests, run.requestOf(p, now))
		}
		for _, s := range run.h.Sessions() {
			all, fs := s.Features()
			label := cleanLabel(s.Auth.ClientName)
			if label == "" {
				label = cleanLabel(s.Auth.TokenName)
			}
			id := auth.Fingerprint(s.Auth.ClientKey)
			if label == "" {
				label = id
			}
			doc.Sessions = append(doc.Sessions, hostSession{ID: s.ID(), Label: label, Device: cleanLabel(s.Auth.ClientName), Identity: id,
				From: addrOnly(s.Remote), Path: s.Path, Seconds: int(now.Sub(s.Started) / time.Second),
				Control: all || slices.Contains(fs, perm.Input)})
		}
		sort.Slice(doc.Sessions, func(i, j int) bool { return doc.Sessions[i].ID < doc.Sessions[j].ID })
	}
	b, err := json.Marshal(doc)
	return string(b), err
}

// hostFeatures is what an allowed login gets: the screen and system
// information (view only), and the input with control.
func hostFeatures(control bool) []perm.Feature {
	f := []perm.Feature{perm.Screen, perm.Info}
	if control {
		f = append(f, perm.Input)
	}
	return f
}

// DecideHostRequest answers request n. For a login, control says whether
// the device may also control the phone (otherwise it only views). For an
// access request of a connected session, allow grants what it asked for.
// The phone offers no file transfer or clipboard: there is no files
// choice.
func DecideHostRequest(n int, allow, control bool) error {
	run, err := runningHost()
	if err != nil {
		return err
	}
	var p *hostd.Approval
	for _, x := range run.h.Pending() {
		if x.N == n {
			p = &x
			break
		}
	}
	if p == nil {
		return errors.New("that request is no longer waiting")
	}
	if len(p.Access) > 0 || p.Action != "" {
		err = run.h.Decide(n, allow)
	} else {
		err = run.h.DecideWith(n, allow, hostFeatures(control && controlOffered()))
	}
	run.mu.Lock()
	delete(run.firstSeen, n)
	run.mu.Unlock()
	quietLogger().Info("host request answered", "n", n, "allow", allow, "control", control)
	run.poke()
	return err
}

// SetHostSessionControl gives connected session id control of the phone,
// or takes it away (it keeps viewing).
func SetHostSessionControl(id int, control bool) error {
	run, err := runningHost()
	if err != nil {
		return err
	}
	err = run.h.SetSessionFeatures(id, false, hostFeatures(control))
	run.poke()
	return err
}

// DisconnectHostSession ends connected session id; 0 ends them all.
func DisconnectHostSession(id int) error {
	run, err := runningHost()
	if err != nil {
		return err
	}
	defer run.poke()
	if id == 0 {
		for _, s := range run.h.Sessions() {
			run.h.Disconnect(s.ID(), hostd.ReasonDisconnectedOnPhone)
		}
		return nil
	}
	return run.h.Disconnect(id, hostd.ReasonDisconnectedOnPhone)
}

// hostPasswordFile holds the owner's own fixed password for host mode
// (SetHostFixedPassword), readable only by the app. Without it, a new
// temporary password is made at every start.
const hostPasswordFile = "host-password"

// ownHostPassword is the owner's fixed password, or "".
func ownHostPassword(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, hostPasswordFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b)) // as typed; the host normalises
}

// SetHostFixedPassword sets the owner's own password for host mode, kept
// across starts, instead of a new temporary one at every start; "" goes
// back to temporary passwords. Every login still waits for the owner's
// answer. The rule is hostd.CheckOwnPassword (8 characters, two of
// letters, digits and others); a refusal's message says what is missing.
// It applies at once when host mode is on.
func SetHostFixedPassword(password string) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	pw := strings.TrimSpace(password) // kept as typed, for the owner to read
	if auth.NormalizePassword(pw) == "" {
		pw = ""
	}
	path := filepath.Join(d, hostPasswordFile)
	if pw == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else {
		if err := hostd.CheckOwnPassword(pw); err != nil {
			return err
		}
		if err := config.WriteFileAtomic(path, []byte(pw), 0o600); err != nil {
			return err
		}
	}
	hostMu.Lock()
	run := hostRun
	hostMu.Unlock()
	if run != nil {
		return run.h.SetFixedPassword(pw)
	}
	notifyHost(nil)
	return nil
}

// RegenerateHostPassword replaces the temporary password.
func RegenerateHostPassword() error {
	run, err := runningHost()
	if err != nil {
		return err
	}
	return run.h.RotatePassword()
}

// hostSeenFile remembers the devices that asked to connect before, by key
// fingerprint, for the "new device" mark of a request.
const hostSeenFile = "host-seen.json"

// maxSeen bounds the remembered devices.
const maxSeen = 256

var seenMu sync.Mutex

// markSeen records identity and reports whether it was new.
func markSeen(identity string) bool {
	seenMu.Lock()
	defer seenMu.Unlock()
	d, err := configDir()
	if err != nil {
		return true
	}
	path := filepath.Join(d, hostSeenFile)
	seen := map[string]int64{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &seen)
	}
	_, known := seen[identity]
	seen[identity] = time.Now().Unix()
	if len(seen) > maxSeen {
		type kv struct {
			k string
			t int64
		}
		all := make([]kv, 0, len(seen))
		for k, t := range seen {
			all = append(all, kv{k, t})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t > all[j].t })
		seen = map[string]int64{}
		for _, e := range all[:maxSeen] {
			seen[e.k] = e.t
		}
	}
	if b, err := json.Marshal(seen); err == nil {
		if err := config.WriteFileAtomic(path, b, 0o600); err != nil {
			slog.Debug("mobile: could not save the seen devices", "err", err)
		}
	}
	return !known
}
