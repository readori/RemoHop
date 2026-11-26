package mobile

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/desktop"
	"github.com/readori/RemoHop/internal/svc/desktop/h264test"
)

// fakeScreen stands in for the app's MediaProjection + MediaCodec: once
// started it pushes a codec-config buffer, an IDR and then deltas, and an
// IDR again whenever asked for a key frame.
type fakeScreen struct {
	w, h int

	mu      sync.Mutex
	stop    chan struct{}
	key     bool
	starts  int
	stops   int
	keys    int
	bitrate int64
}

func (f *fakeScreen) StartScreen(maxSize, fps int, bitrate int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.bitrate = bitrate
	if f.stop != nil {
		close(f.stop)
	}
	stop := make(chan struct{})
	f.stop = stop
	f.key = true
	go func() {
		PushScreenFrame(h264test.Config(f.w, f.h), 0)
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			f.mu.Lock()
			key := f.key
			f.key = false
			f.mu.Unlock()
			nal := h264test.Delta(200)
			if key {
				nal = h264test.IDR(2000)
			}
			PushScreenFrame(h264test.AnnexB(nal), time.Now().UnixMicro())
		}
	}()
	return nil
}

func (f *fakeScreen) StopScreen() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	if f.stop != nil {
		close(f.stop)
		f.stop = nil
	}
}

func (f *fakeScreen) RequestKeyframe() {
	f.mu.Lock()
	f.keys++
	f.key = true
	f.mu.Unlock()
}

func (f *fakeScreen) SetBitrate(b int64) {
	f.mu.Lock()
	f.bitrate = b
	f.mu.Unlock()
}

func (f *fakeScreen) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

// fakeInjector records the input that reaches the phone.
type fakeInjector struct {
	mu     sync.Mutex
	why    string
	events []string
	typed  []string
}

func (f *fakeInjector) Unavailable() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.why
}

func (f *fakeInjector) add(s string) error {
	f.mu.Lock()
	f.events = append(f.events, s)
	f.mu.Unlock()
	return nil
}

func (f *fakeInjector) Move(x, y float64) error { return f.add("move") }
func (f *fakeInjector) Button(b int, down bool, x, y float64) error {
	if down {
		return f.add("down")
	}
	return f.add("up")
}
func (f *fakeInjector) Wheel(dx, dy float64) error       { return f.add("wheel") }
func (f *fakeInjector) Key(code string, down bool) error { return f.add("key " + code) }
func (f *fakeInjector) TypeText(text string) error {
	f.mu.Lock()
	f.typed = append(f.typed, text)
	f.mu.Unlock()
	return nil
}
func (f *fakeInjector) ReleaseAll() { f.add("release") }

func (f *fakeInjector) snapshot() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...), append([]string(nil), f.typed...)
}

// listener collects HostListener events.
type listener struct {
	mu       sync.Mutex
	last     string
	requests chan string
}

func (l *listener) HostChanged(state string) {
	l.mu.Lock()
	l.last = state
	l.mu.Unlock()
}

func (l *listener) HostRequest(r string) { l.requests <- r }

func (l *listener) lastState() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

func hostDocNow(t *testing.T) hostDoc {
	t.Helper()
	s, err := HostStateJSON()
	if err != nil {
		t.Fatal(err)
	}
	var d hostDoc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func waitDoc(t *testing.T, what string, cond func(hostDoc) bool) hostDoc {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d := hostDocNow(t); cond(d) {
			return d
		}
	}
	t.Fatalf("timed out waiting for %s: %+v", what, hostDocNow(t))
	return hostDoc{}
}

// startPhoneHost runs host mode on loopback without a rendezvous server,
// with fake native screen and input.
func startPhoneHost(t *testing.T) (*fakeScreen, *fakeInjector, *listener, string) {
	t.Helper()
	initDir(t)
	hostConfigHook = func(c *hostd.Config) {
		c.Listen = "127.0.0.1:0"
		c.Locals = func() []netip.Addr { return nil }
	}
	scr, inj, l := &fakeScreen{w: 864, h: 1920}, &fakeInjector{}, &listener{requests: make(chan string, 8)}
	SetScreenSource(scr)
	SetInputInjector(inj)
	SetHostListener(l)
	SetScreenInfo(1080, 2400, 0)
	t.Cleanup(func() {
		StopHost()
		SetHostListener(nil)
		SetScreenSource(nil)
		SetInputInjector(nil)
		hostConfigHook = nil
	})
	if err := StartHost(); err != nil {
		t.Fatal(err)
	}
	if err := StartHost(); err != nil { // already running: fine
		t.Fatal(err)
	}
	d := waitDoc(t, "the host", func(d hostDoc) bool { return d.Phase == hostNoServer && d.Password != "" })
	if !d.On || d.ID != "" || !d.Control || d.Timeout != 60 {
		t.Fatalf("state %+v", d)
	}
	run, _ := runningHost()
	return scr, inj, l, run.h.Addr().String()
}

type dialed struct {
	conn *peer.Conn
	err  error
}

// login dials the phone's host as another device and returns the request
// the phone was asked about, with the dial's outcome to come.
func login(t *testing.T, l *listener, addr, password, clientDir string) (hostRequest, chan dialed) {
	t.Helper()
	out := make(chan dialed, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := dial.Dial(ctx, addr, dial.Options{Password: password, Dir: clientDir})
		out <- dialed{c, err}
	}()
	select {
	case r := <-l.requests:
		var req hostRequest
		if err := json.Unmarshal([]byte(r), &req); err != nil {
			t.Fatal(err)
		}
		return req, out
	case d := <-out:
		t.Fatalf("dial ended without asking the phone: %v", d.err)
	case <-time.After(15 * time.Second):
		t.Fatal("no request reached the phone")
	}
	return hostRequest{}, nil
}

func nextHello(t *testing.T, s *desktop.Session) desktop.Hello {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("waiting for a Hello: %v", err)
		}
		if ev.Hello != nil {
			return *ev.Hello
		}
	}
}

// The phone as a host, end to end: a client logs in with the temporary
// password, the phone's owner is asked and allows control, the client
// opens the desktop service and gets the phone's H.264 stream (Hello with
// the size of the encoder's SPS, then key frame and deltas), and its input
// reaches the phone's injector.
func TestHostModeEndToEnd(t *testing.T) {
	scr, inj, l, addr := startPhoneHost(t)
	pw := hostDocNow(t).Password
	device := t.TempDir()

	req, out := login(t, l, addr, pw, device)
	if !req.FirstSeen || req.Identity == "" || req.Auth != "password" || req.From != "127.0.0.1" || req.Left <= 0 || req.Left > 60 {
		t.Fatalf("request %+v", req)
	}
	d := waitDoc(t, "the request in the state", func(d hostDoc) bool { return len(d.Requests) == 1 })
	if d.Requests[0].N != req.N {
		t.Fatalf("state requests %+v", d.Requests)
	}
	if err := DecideHostRequest(req.N, true, true); err != nil {
		t.Fatal(err)
	}
	r := <-out
	if r.err != nil {
		t.Fatal(r.err)
	}
	conn := r.conn
	defer conn.Close()
	d = waitDoc(t, "the session", func(d hostDoc) bool { return len(d.Sessions) == 1 && len(d.Requests) == 0 })
	if !d.Sessions[0].Control || d.Sessions[0].Identity != req.Identity {
		t.Fatalf("sessions %+v", d.Sessions)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := desktop.Open(ctx, conn, desktop.Options{FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := s.Hello()
	if h.Width != 864 || h.Height != 1920 || !h.IsH264() || !h.Input || h.ScreenWidth != 1080 || h.ScreenHeight != 2400 || h.Encoder != "mediacodec" {
		t.Fatalf("hello %+v", h)
	}
	if !strings.HasPrefix(h.Codec, "avc1.42") {
		t.Fatalf("codec %s", h.Codec)
	}
	key, deltas := false, 0
	for deltas < 3 {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if f := ev.Frame; f != nil {
			if !key && !f.Key {
				t.Fatal("the first frame is not a key frame")
			}
			key = true
			if !f.Key {
				deltas++
			}
		}
	}
	if d := hostDocNow(t); d.Viewers != 1 {
		t.Fatalf("viewers %d", d.Viewers)
	}

	if err := s.SendInput(desktop.InputEvent{Kind: desktop.InputButton, X: 0.5, Y: 0.5, Down: true}); err != nil {
		t.Fatal(err)
	}
	s.SendInput(desktop.InputEvent{Kind: desktop.InputButton, X: 0.5, Y: 0.5})
	if err := s.TypeText(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	ev, typed := inj.snapshot()
	if len(ev) < 2 || ev[0] != "down" || ev[1] != "up" || len(typed) != 1 || typed[0] != "hello" {
		t.Fatalf("input %q typed %q", ev, typed)
	}

	// Nothing but the phone's own services is served.
	if _, err := conn.Open(ctx, "exec", map[string]any{"argv": []string{"id"}}); err == nil {
		t.Fatal("the phone's host served exec")
	}
	if _, err := hostd.Pair(ctx, conn, hostd.PairRequest{Name: "x"}); err == nil {
		t.Fatal("the phone's host issued a pairing token")
	}

	// The owner takes control away: the open stream turns view-only.
	if err := SetHostSessionControl(d.Sessions[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if h := nextHello(t, s); h.Input {
		t.Fatalf("hello after control was taken away %+v", h)
	}

	// Disconnecting on the phone ends the session; the screen stops.
	if err := DisconnectHostSession(0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Context().Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end")
	}
	waitDoc(t, "no sessions", func(d hostDoc) bool { return len(d.Sessions) == 0 && d.Viewers == 0 })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if starts, stops := scr.counts(); starts >= 1 && stops >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the screen was not stopped")
		}
	}
	if !strings.Contains(l.lastState(), `"phase":"no_server"`) {
		t.Fatalf("listener state %s", l.lastState())
	}

	// The same device again is no longer new.
	req, out = login(t, l, addr, pw, device)
	if req.FirstSeen {
		t.Fatal("a known device is marked new")
	}
	DecideHostRequest(req.N, false, false)
	if r := <-out; r.err == nil {
		r.conn.Close()
		t.Fatal("a denied login connected")
	}

	StopHost()
	if d := hostDocNow(t); d.On || d.Phase != hostOff {
		t.Fatalf("state after stop %+v", d)
	}
	if err := PushScreenFrame([]byte{0, 0, 0, 1, 0x65}, 0); err == nil {
		t.Fatal("a frame was taken with host mode off")
	}
}

// Allowed without control, the session views only, whatever the injector
// could do; and a phone without an injector is view-only for everyone.
func TestHostModeViewOnly(t *testing.T) {
	_, inj, l, addr := startPhoneHost(t)
	pw := hostDocNow(t).Password
	req, out := login(t, l, addr, pw, t.TempDir())
	if err := DecideHostRequest(req.N, true, false); err != nil {
		t.Fatal(err)
	}
	r := <-out
	if r.err != nil {
		t.Fatal(r.err)
	}
	defer r.conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := desktop.Open(ctx, r.conn, desktop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if h := s.Hello(); h.Input || h.InputError == "" {
		t.Fatalf("hello %+v", h)
	}
	if err := s.TypeText(ctx, "x"); err == nil {
		t.Fatal("typing worked without control")
	}
	if ev, typed := inj.snapshot(); len(ev) != 0 || len(typed) != 0 {
		t.Fatalf("input reached the phone: %q %q", ev, typed)
	}
	if d := hostDocNow(t); len(d.Sessions) != 1 || d.Sessions[0].Control {
		t.Fatalf("sessions %+v", d.Sessions)
	}

	// The accessibility service is switched off on the phone: the state
	// says so.
	inj.mu.Lock()
	inj.why = "the accessibility service is off"
	inj.mu.Unlock()
	if d := hostDocNow(t); d.Control || d.NoControl != "the accessibility service is off" {
		t.Fatalf("state %+v", d)
	}
	SetInputInjector(nil)
	if d := hostDocNow(t); d.Control || d.NoControl == "" {
		t.Fatalf("state %+v", d)
	}
}

func TestHostModeRegenerateAndOff(t *testing.T) {
	if err := RegenerateHostPassword(); err == nil {
		t.Fatal("regenerated with host mode off")
	}
	if err := DecideHostRequest(1, true, true); err == nil {
		t.Fatal("decided with host mode off")
	}
	startPhoneHost(t)
	before := hostDocNow(t).Password
	if err := RegenerateHostPassword(); err != nil {
		t.Fatal(err)
	}
	if after := hostDocNow(t).Password; after == before || after == "" {
		t.Fatalf("password %q -> %q", before, after)
	}
	if err := DecideHostRequest(99, true, true); err == nil {
		t.Fatal("decided a request that does not exist")
	}
	if !HostRunning() {
		t.Fatal("not running")
	}
	StopHost()
	if HostRunning() {
		t.Fatal("still running")
	}
	if err := DisconnectHostSession(0); err == nil {
		t.Fatal("disconnected with host mode off")
	}
}
