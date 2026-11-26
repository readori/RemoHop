package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

// A provided screen is the desktop service for a host that has no ffmpeg
// and no screen of its own to capture: the RemoHop phone apps. Native code
// captures and encodes (Android: MediaProjection into a hardware MediaCodec
// H.264 encoder) and hands this package H.264 access units in Annex B form;
// this package speaks the same wire protocol as the ffmpeg host (Hello with
// the avcC record, framed video, stats, acknowledgements and the flow
// control of flow.go), so every client works unchanged. Input goes back to
// native code through an InputSink (Android: an accessibility service).
//
// One encoder serves every viewer of the screen (a phone has one), so the
// Screen is a hub: viewers subscribe, each with its own flow control, and
// the encoder gets what they ask for together: the largest picture and
// frame rate any of them wants, the bitrate the most constrained one can
// take, and a key frame whenever one of them needs it.

// ScreenSource is the native side of a provided screen. Its methods are
// called from the sessions' goroutines, one at a time; they must not
// block for long (they run on the session's control path).
type ScreenSource interface {
	// StartScreen starts capturing and encoding, or applies new limits to
	// the running encoder (restarting it if it must): the picture's long
	// edge at most maxSize pixels (0: as large as the screen), at most fps
	// frames per second, aiming at bitrate bits per second. The encoded
	// frames come back through Screen.PushFrame. An error means the screen
	// cannot be captured now (its owner has not allowed it, for example);
	// the sessions report it and ask again a little later.
	StartScreen(maxSize, fps int, bitrate int64) error
	// StopScreen stops capturing: nobody watches any more.
	StopScreen()
	// RequestKeyframe asks for an IDR frame (with its SPS and PPS) soon.
	RequestKeyframe()
	// SetBitrate changes the target bitrate without restarting.
	SetBitrate(bitrate int64)
}

// InputSink delivers the input of sessions that may control the device.
// Its methods are called from session goroutines, one at a time per
// session.
type InputSink interface {
	// Unavailable says why input cannot be delivered right now ("" if it
	// can), e.g. that the accessibility service is off. It is asked about
	// once a second while a session is open, and when one starts.
	Unavailable() string
	// Inject delivers one pointer or keyboard event.
	Inject(ev InputEvent) error
	// TypeText enters text into whatever has the focus.
	TypeText(text string) error
	// ReleaseAll lets go of anything held down (a session ended or lost
	// control in the middle of a gesture).
	ReleaseAll()
}

// ScreenConfig configures NewScreen.
type ScreenConfig struct {
	Source ScreenSource
	// Input, if not nil, takes the input of sessions allowed to control.
	Input InputSink
	// Encoder names the encoder in the Hello (e.g. "android-mediacodec").
	Encoder string
	// OS is reported in probes; empty means runtime.GOOS.
	OS string
	// MaxSize caps the long edge of the picture (default 1920); MaxFPS the
	// frame rate (default 60); MaxBitrate the automatic bitrate (default
	// 16 Mbit/s).
	MaxSize    int
	MaxFPS     int
	MaxBitrate int64
	Logger     *slog.Logger
}

// Defaults of ScreenConfig.
const (
	defaultProvidedSize    = 1920
	defaultProvidedFPS     = 60
	defaultProvidedBitrate = 16_000_000
	// providedRetry spaces out StartScreen calls that failed.
	providedRetry = time.Second
	// providedKeyGap rate-limits key frame requests to the encoder (each
	// costs a large frame for every viewer).
	providedKeyGap = 300 * time.Millisecond
	// providedFirstFrame is how long a session waits for the first frame
	// once the source has started, before it gives up.
	providedFirstFrame = 2 * time.Minute
	// providedWaitLimit is how long a session waits for a source that
	// cannot start (StartScreen keeps failing) before it gives up.
	providedWaitLimit = 10 * time.Minute
)

// Screen is a provided screen: the hub between one ScreenSource and the
// sessions watching it.
type Screen struct {
	cfg ScreenConfig
	log *slog.Logger

	// srcMu serialises calls into the source.
	srcMu sync.Mutex

	mu      sync.Mutex
	subs    map[*providedSession]struct{}
	closed  bool
	wake    chan struct{}
	running bool // the hub loop runs (there are subscribers)
	// What the source runs with, as last applied; started is true while
	// StartScreen has succeeded and StopScreen has not been called.
	started  bool
	applied  screenParams
	startErr string
	lastTry  time.Time
	keyAt    time.Time
	keyWant  bool
	// The stream: its configuration (a new one for every new SPS or PPS)
	// and the parameter sets seen most recently.
	conf     *streamConf
	sps, pps []byte
	gen      int
	// The screen in device pixels and its rotation (0, 90, 180, 270), as
	// the source reported (SetScreenInfo).
	width, height, rotation int
	frames                  uint64
}

// screenParams is what the encoder is asked for.
type screenParams struct {
	maxSize, fps int
	bitrate      int64
}

// streamConf is one configuration of the encoded stream.
type streamConf struct {
	gen  int
	avc  AVCInfo
	avcC []byte
}

// providedFrame is one access unit on its way to the sessions.
type providedFrame struct {
	conf    *streamConf
	key     bool
	data    []byte // length-prefixed NAL units
	capture int64  // Unix microseconds, 0 if unknown
	at      time.Time
}

// NewScreen makes a provided screen. Serve it as the desktop service.
func NewScreen(cfg ScreenConfig) *Screen {
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = defaultProvidedSize
	}
	if cfg.MaxFPS <= 0 || cfg.MaxFPS > maxFPS {
		cfg.MaxFPS = defaultProvidedFPS
	}
	if cfg.MaxBitrate <= 0 {
		cfg.MaxBitrate = defaultProvidedBitrate
	}
	if cfg.Encoder == "" {
		cfg.Encoder = "provided"
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Screen{cfg: cfg, log: log, subs: map[*providedSession]struct{}{}, wake: make(chan struct{}, 1)}
}

// SetScreenInfo reports the screen's size in pixels and its rotation in
// degrees. The source calls it when the screen turns or changes size (and
// then restarts its encoder, whose new SPS starts a new stream).
func (sc *Screen) SetScreenInfo(width, height, rotation int) {
	sc.mu.Lock()
	sc.width, sc.height, sc.rotation = max(width, 0), max(height, 0), rotation
	sc.mu.Unlock()
	sc.poke()
}

// ScreenInfo is what SetScreenInfo last reported.
func (sc *Screen) ScreenInfo() (width, height, rotation int) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.width, sc.height, sc.rotation
}

// Viewers counts the sessions watching the screen.
func (sc *Screen) Viewers() int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return len(sc.subs)
}

// errScreenClosed ends sessions when the screen is closed.
var errScreenClosed = errors.New("the device stopped sharing its screen")

// Close ends every session of the screen and stops the source. A closed
// screen refuses new sessions.
func (sc *Screen) Close() {
	sc.mu.Lock()
	sc.closed = true
	subs := make([]*providedSession, 0, len(sc.subs))
	for s := range sc.subs {
		subs = append(subs, s)
	}
	sc.mu.Unlock()
	for _, s := range subs {
		s.end(errScreenClosed)
	}
	sc.poke()
}

// PushFrame takes one encoder output buffer: H.264 in Annex B form, either
// parameter sets alone (a codec-config buffer) or an access unit, which
// may carry the parameter sets in front. capture is when the picture was
// captured, in Unix microseconds (0 if unknown). The data is copied.
func (sc *Screen) PushFrame(annexB []byte, capture int64) error {
	u, err := parseAnnexB(annexB)
	if err != nil {
		return err
	}
	now := time.Now()
	sc.mu.Lock()
	if u.sps != nil {
		sc.sps = bytes.Clone(u.sps)
	}
	if u.pps != nil {
		sc.pps = bytes.Clone(u.pps)
	}
	if sc.sps != nil && sc.pps != nil && (sc.conf == nil || u.sps != nil || u.pps != nil) {
		if err := sc.configureLocked(); err != nil {
			sc.mu.Unlock()
			return err
		}
	}
	conf := sc.conf
	if u.sample == nil {
		sc.mu.Unlock()
		return nil
	}
	if conf == nil {
		// Frames before the parameter sets cannot be decoded.
		sc.keyWant = true
		sc.mu.Unlock()
		sc.poke()
		return nil
	}
	sc.frames++
	if capture != 0 {
		if d := now.UnixMicro() - capture; d < -50_000 || d > 10e6 {
			capture = 0 // not on this clock
		}
	}
	f := providedFrame{conf: conf, key: u.key, data: u.sample, capture: capture, at: now}
	subs := make([]*providedSession, 0, len(sc.subs))
	for s := range sc.subs {
		subs = append(subs, s)
	}
	sc.mu.Unlock()
	for _, s := range subs {
		s.offer(f)
	}
	return nil
}

// configureLocked starts a new stream configuration when the parameter
// sets changed.
func (sc *Screen) configureLocked() error {
	avcC := buildAVCC(sc.sps, sc.pps)
	if sc.conf != nil && bytes.Equal(sc.conf.avcC, avcC) {
		return nil
	}
	info, err := parseAVCC(avcC)
	if err != nil {
		sc.sps, sc.pps = nil, nil
		return fmt.Errorf("desktop: the encoder's SPS: %w", err)
	}
	sc.gen++
	sc.conf = &streamConf{gen: sc.gen, avc: info, avcC: avcC}
	sc.log.Debug("desktop: provided stream configured", "gen", sc.gen, "width", info.Width, "height", info.Height, "codec", info.Codec())
	return nil
}

func (sc *Screen) poke() {
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}

// subscribe adds a session; the first one starts the hub loop.
func (sc *Screen) subscribe(s *providedSession) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.closed {
		return errScreenClosed
	}
	sc.subs[s] = struct{}{}
	sc.keyWant = true // the newcomer needs a key frame
	if !sc.running {
		sc.running = true
		go sc.loop()
	}
	sc.poke()
	return nil
}

func (sc *Screen) unsubscribe(s *providedSession) {
	sc.mu.Lock()
	delete(sc.subs, s)
	sc.mu.Unlock()
	sc.poke()
}

// requestKeyframe asks the encoder for a key frame (rate-limited).
func (sc *Screen) requestKeyframe() {
	sc.mu.Lock()
	sc.keyWant = true
	sc.mu.Unlock()
	sc.poke()
}

// status says whether the source runs and, if it cannot start, why.
func (sc *Screen) status() (started bool, why string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.started, sc.startErr
}

// want is what the subscribers ask of the encoder together.
func (sc *Screen) wantLocked() screenParams {
	var p screenParams
	for s := range sc.subs {
		w := s.wants()
		p.maxSize = max(p.maxSize, w.maxSize)
		p.fps = max(p.fps, w.fps)
		if p.bitrate == 0 || w.bitrate < p.bitrate {
			p.bitrate = w.bitrate
		}
	}
	return p
}

// loop drives the source while anyone watches: starts it, retries a start
// that failed, applies what the sessions ask for, forwards key frame
// requests and stops it when the last session leaves.
func (sc *Screen) loop() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		if sc.stopIfIdle() {
			return
		}
		sc.mu.Lock()
		now := time.Now()
		want := sc.wantLocked()
		var start, bitrate, key bool
		switch {
		case !sc.started:
			start = now.Sub(sc.lastTry) >= providedRetry
		case want.maxSize != sc.applied.maxSize || want.fps != sc.applied.fps:
			start = true
		case want.bitrate != sc.applied.bitrate:
			bitrate = true
		}
		if sc.started && sc.keyWant && now.Sub(sc.keyAt) >= providedKeyGap {
			key, sc.keyWant, sc.keyAt = true, false, now
		}
		if start {
			sc.lastTry = now
		}
		sc.mu.Unlock()

		switch {
		case start:
			sc.srcMu.Lock()
			err := sc.cfg.Source.StartScreen(want.maxSize, want.fps, want.bitrate)
			sc.srcMu.Unlock()
			sc.mu.Lock()
			if err != nil {
				if sc.startErr != err.Error() {
					sc.log.Info("desktop: the screen cannot be captured yet", "err", err)
				}
				sc.started, sc.startErr = false, err.Error()
			} else {
				if !sc.started {
					sc.log.Info("desktop: provided screen started", "max_size", want.maxSize, "fps", want.fps, "bitrate", want.bitrate)
				}
				sc.started, sc.startErr, sc.applied = true, "", want
				sc.keyAt = now // a (re)started encoder begins with a key frame
				sc.keyWant = false
			}
			sc.mu.Unlock()
		case bitrate:
			sc.srcMu.Lock()
			sc.cfg.Source.SetBitrate(want.bitrate)
			sc.srcMu.Unlock()
			sc.mu.Lock()
			sc.applied.bitrate = want.bitrate
			sc.mu.Unlock()
		}
		if key {
			sc.srcMu.Lock()
			sc.cfg.Source.RequestKeyframe()
			sc.srcMu.Unlock()
		}
		select {
		case <-sc.wake:
		case <-t.C:
		}
	}
}

// stopIfIdle ends the hub loop when nobody watches any more, stopping the
// source. It holds srcMu across the decision and StopScreen, so that a loop
// started meanwhile by a new session starts the source only after it was
// stopped; it never holds mu while calling the source (the source may wait
// for its encoder thread, which may be inside PushFrame).
func (sc *Screen) stopIfIdle() bool {
	sc.mu.Lock()
	idle := len(sc.subs) == 0 || sc.closed
	sc.mu.Unlock()
	if !idle {
		return false
	}
	sc.srcMu.Lock()
	defer sc.srcMu.Unlock()
	sc.mu.Lock()
	if len(sc.subs) > 0 && !sc.closed {
		sc.mu.Unlock()
		return false
	}
	started := sc.started
	sc.started, sc.running, sc.startErr, sc.applied = false, false, "", screenParams{}
	sc.conf, sc.sps, sc.pps = nil, nil, nil // the next start brings its own
	sc.mu.Unlock()
	if started {
		sc.cfg.Source.StopScreen()
		sc.log.Debug("desktop: provided screen stopped")
	}
	return true
}

// Serve is the desktop service handler for this screen.
func (sc *Screen) Serve(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var opts Options
	err := peer.DecodeReq(raw, &opts)
	if err == nil {
		err = opts.normalize()
	}
	if err == nil && (opts.Codec == codecAV1 || len(opts.Decode) > 0 && !slices.Contains(opts.Decode, codecH264)) {
		err = errors.New("this device streams H.264 only, which the viewer cannot decode")
	}
	if err != nil {
		writeFatal(s, fmt.Errorf("bad request: %w", err))
		return err
	}
	hostOff := c != nil && !hostd.Allowed(c, perm.Input)
	if opts.Probe {
		return sc.serveProbe(s, opts, hostOff)
	}
	p := sc.newSession(ctx, s, opts, hostOff, c != nil && fastPath(c))
	if err := sc.subscribe(p); err != nil {
		writeFatal(s, err)
		return err
	}
	defer sc.unsubscribe(p)
	if c != nil {
		stop := watchInput(c, p.setHostInput)
		defer stop()
		p.setHostInput(hostd.Allowed(c, perm.Input))
	}
	return p.run()
}

func (sc *Screen) serveProbe(s peer.Stream, opts Options, hostOff bool) error {
	w, h, _ := sc.ScreenInfo()
	info := HostInfo{OS: sc.cfg.OS, ScreenWidth: w, ScreenHeight: h, Encoders: map[string]string{codecH264: sc.cfg.Encoder}}
	info.InputError = sc.inputRefusal(opts, hostOff)
	info.Input = info.InputError == ""
	info.Unsupported = providedUnsupported(info.InputError)
	body, _ := json.Marshal(info)
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return wire.WriteFrame(s, frameInfo, body)
}

// inputRefusal says why a session may not control the device ("" if it
// may).
func (sc *Screen) inputRefusal(opts Options, hostOff bool) string {
	if why := inputRefusal(opts, hostOff); why != "" {
		return why
	}
	if sc.cfg.Input == nil {
		return "this device cannot be controlled, only viewed"
	}
	return sc.cfg.Input.Unavailable()
}

// providedUnsupported lists the host controls a provided screen cannot
// offer: all but typing, and typing too when the session has no input.
func providedUnsupported(inputErr string) map[string]string {
	const na = "not available on this device"
	out := map[string]string{ControlLock: na, ControlBlockInput: na, ControlBlackout: na, ControlCtrlAltDel: na, ControlAllMonitors: na}
	if inputErr != "" {
		out[ControlType] = "input is off for this session: " + inputErr
	}
	return out
}

// providedSession is one viewer of a provided screen.
type providedSession struct {
	sc     *Screen
	ctx    context.Context
	cancel context.CancelCauseFunc
	s      peer.Stream
	opts   Options
	start  time.Time
	fast   bool

	frames chan providedFrame
	// overflow: frames were dropped because the session did not take
	// them in time; the next one it sends must be a key frame.
	overflow atomic.Bool
	ctl      chan Control
	out      chan []byte
	wdone    chan struct{}
	werr     error

	// Input. injMu orders injection with the owner switching input off
	// and with the end of the session.
	injMu    sync.Mutex
	inErr    string // why the session has no input ("" if it has)
	hostOff  bool
	holding  bool // input was delivered since the last ReleaseAll
	viewOnly bool
	// inputChanged tells run that inErr changed (a new Hello is due).
	inputChanged chan struct{}

	// What the session asks of the encoder (wants), shared with the hub.
	wantMu sync.Mutex
	want   screenParams

	// Owned by run's goroutine.
	helloConf  *streamConf
	needHello  bool
	hellos     int
	seq, acked uint32
	acks       ackClock
	waitKey    bool
	gotFrame   bool
	lastDrop   time.Time
	lastKeyReq time.Time
	bitrate    int64 // current target
	fps        int
	congestion []time.Time
	paused     string
	pausedAt   time.Time
	startedAt  time.Time // when the source was first seen running

	sentFrames  int
	sentBytes   int64
	dropped     uint64
	lastStatsAt time.Time
	lastInputAt time.Time
}

func (sc *Screen) newSession(ctx context.Context, s peer.Stream, opts Options, hostOff, fast bool) *providedSession {
	p := &providedSession{
		sc: sc, s: s, opts: opts, start: time.Now(), fast: fast,
		frames:       make(chan providedFrame, 8),
		ctl:          make(chan Control, 256),
		wdone:        make(chan struct{}),
		inputChanged: make(chan struct{}, 1),
		hostOff:      hostOff,
		viewOnly:     opts.ViewOnly,
		fps:          min(opts.FPS, sc.cfg.MaxFPS),
	}
	p.ctx, p.cancel = context.WithCancelCause(ctx)
	p.out = make(chan []byte, int(maxFPS*window.Seconds())*2+32)
	p.bitrate = p.targetBitrate()
	p.inErr = sc.inputRefusal(opts, hostOff)
	p.setWants()
	return p
}

// end stops the session with err (the screen was closed).
func (p *providedSession) end(err error) { p.cancel(err) }

// offer hands the session a frame without ever blocking the encoder: a
// session that has not taken the previous ones misses this one and
// restarts from a key frame.
func (p *providedSession) offer(f providedFrame) {
	select {
	case p.frames <- f:
	default:
		p.overflow.Store(true)
		p.sc.requestKeyframe()
	}
}

// maxSize is the long edge the session wants, from its MaxHeight and Scale.
func (p *providedSession) maxSize() int {
	size := p.sc.cfg.MaxSize
	w, h, _ := p.sc.ScreenInfo()
	long := max(w, h)
	if long == 0 {
		return size
	}
	k := p.opts.Scale
	if p.opts.MaxHeight > 0 && h > 0 {
		k = min(k, float64(p.opts.MaxHeight)/float64(h))
	}
	if k < 1 {
		size = min(size, int(float64(long)*k))
	}
	return min(size, long)
}

// outputPixels estimates the encoded picture's size.
func (p *providedSession) outputPixels() int {
	w, h, _ := p.sc.ScreenInfo()
	if w == 0 || h == 0 {
		w, h = 1080, 1920
	}
	long := max(w, h)
	k := min(1, float64(p.maxSize())/float64(long))
	return int(float64(w)*k) * int(float64(h)*k)
}

func (p *providedSession) targetBitrate() int64 {
	if p.opts.Bitrate > 0 {
		return p.opts.Bitrate
	}
	return min(autoBitrate(p.outputPixels(), p.fps, p.fast), p.sc.cfg.MaxBitrate)
}

func (p *providedSession) setWants() {
	p.wantMu.Lock()
	p.want = screenParams{maxSize: p.maxSize(), fps: p.fps, bitrate: p.bitrate}
	p.wantMu.Unlock()
}

func (p *providedSession) wants() screenParams {
	p.wantMu.Lock()
	defer p.wantMu.Unlock()
	return p.want
}

// changed tells the hub that the session wants something else.
func (p *providedSession) changed() {
	p.setWants()
	p.sc.poke()
}

func (p *providedSession) windowFrames() int {
	return max(minWindowFrames, int(float64(p.fps)*p.acks.window(time.Now()).Seconds()+0.5))
}

func (p *providedSession) framePeriod() time.Duration {
	return time.Second / time.Duration(max(p.fps, 1))
}

// setHostInput applies the host owner's input switch to the session.
func (p *providedSession) setHostInput(allowed bool) {
	p.injMu.Lock()
	p.hostOff = !allowed
	p.refreshInputLocked()
	p.injMu.Unlock()
}

// refreshInputLocked recomputes why the session has no input and, when
// that changed, tells run (and lets go of anything held when input went
// away).
func (p *providedSession) refreshInputLocked() {
	why := p.sc.inputRefusal(Options{ViewOnly: p.viewOnly}, p.hostOff)
	if why == p.inErr {
		return
	}
	if why != "" && p.holding && p.sc.cfg.Input != nil {
		p.sc.cfg.Input.ReleaseAll()
		p.holding = false
	}
	p.inErr = why
	select {
	case p.inputChanged <- struct{}{}:
	default:
	}
}

func (p *providedSession) run() error {
	defer p.cancel(nil)
	go p.writer()
	go p.readControl()
	defer func() {
		p.cancel(nil)
		<-p.wdone
		p.injMu.Lock()
		if p.holding && p.sc.cfg.Input != nil {
			p.sc.cfg.Input.ReleaseAll()
		}
		p.holding = false
		p.inErr = "the session has ended"
		p.injMu.Unlock()
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	p.lastStatsAt = time.Now()
	p.lastInputAt = time.Now()
	for {
		select {
		case <-p.ctx.Done():
			return p.ended()
		case <-p.wdone:
			return p.werr
		case <-p.inputChanged:
			// Every Hello comes before a key frame.
			p.needHello = true
			p.askKeyframe(time.Now())
		case f := <-p.frames:
			p.onFrame(f)
		case c, ok := <-p.ctl:
			if !ok {
				return nil // the client closed the stream
			}
			p.onControl(c)
		case now := <-tick.C:
			if err := p.onTick(now); err != nil {
				p.fatal(err)
				return err
			}
		}
	}
}

// ended is run's answer to its context ending: the host's owner withdrew
// the screen, or the screen was closed. The client is told why.
func (p *providedSession) ended() error {
	cause := context.Cause(p.ctx)
	w, withdrawn := perm.Withdrawn(cause)
	if !withdrawn && !errors.Is(cause, errScreenClosed) {
		return p.ctx.Err()
	}
	<-p.wdone
	if withdrawn {
		writeFatal(p.s, w)
		return w
	}
	writeFatal(p.s, cause)
	return cause
}

func (p *providedSession) onTick(now time.Time) error {
	started, why := p.sc.status()
	switch {
	case started && p.startedAt.IsZero():
		p.startedAt = now
	case !started && why != "":
		if p.pausedAt.IsZero() {
			p.pausedAt = now
		}
		p.paused = why
		if now.Sub(p.pausedAt) > providedWaitLimit {
			return fmt.Errorf("the device's screen could not be captured: %s", why)
		}
	}
	if started {
		p.paused, p.pausedAt = "", time.Time{}
	}
	if !p.gotFrame && !p.startedAt.IsZero() && now.Sub(p.startedAt) > providedFirstFrame {
		return fmt.Errorf("the device's encoder delivered no video within %v", providedFirstFrame)
	}
	if now.Sub(p.lastInputAt) >= time.Second {
		p.lastInputAt = now
		p.injMu.Lock()
		p.refreshInputLocked()
		p.injMu.Unlock()
	}
	p.adapt(now)
	if now.Sub(p.lastStatsAt) >= statsEvery {
		p.sendStats(now)
	}
	return nil
}

// askKeyframe asks the hub for a key frame, at most every providedKeyGap.
func (p *providedSession) askKeyframe(now time.Time) {
	if now.Sub(p.lastKeyReq) < providedKeyGap {
		return
	}
	p.lastKeyReq = now
	p.sc.requestKeyframe()
}

func (p *providedSession) onFrame(f providedFrame) {
	now := time.Now()
	p.gotFrame = true
	if p.overflow.Swap(false) {
		p.waitKey = true
		p.dropped++
	}
	if p.helloConf != f.conf || p.needHello {
		if !f.key {
			p.dropped++
			p.askKeyframe(now)
			return
		}
		if !p.sendHello(f.conf) {
			return
		}
	}
	inflight := int(p.seq - p.acked)
	if (p.waitKey && !f.key) || inflight >= p.windowFrames() {
		if !p.waitKey && p.acks.queueing(now, p.framePeriod()) {
			p.onCongestion(now, true)
		}
		p.waitKey = true
		p.dropped++
		return
	}
	p.waitKey = false
	p.seq++
	ts := f.capture
	if ts == 0 {
		ts = f.at.UnixMicro()
	}
	hdr := frameHeader{key: f.key, seq: p.seq, pts: ts - p.start.UnixMicro(), capture: f.capture, sent: now.UnixMicro()}
	msg := make([]byte, 5+frameHeaderLen+len(f.data))
	putFrameHead(msg, frameVideo, frameHeaderLen+len(f.data))
	hdr.put(msg[5:])
	copy(msg[5+frameHeaderLen:], f.data)
	if !p.enqueue(msg, false) {
		p.seq--
		p.dropped++
		p.waitKey = true
		return
	}
	p.acks.onSend(p.seq, now)
	p.sentFrames++
	p.sentBytes += int64(len(f.data))
}

func (p *providedSession) sendHello(conf *streamConf) bool {
	p.injMu.Lock()
	inErr := p.inErr
	p.injMu.Unlock()
	w, h, _ := p.sc.ScreenInfo()
	info := conf.avc
	if need := h264LevelFor(info.Width, info.Height, p.fps); need > info.Level {
		info.Level = need
	}
	p.hellos++
	hello := Hello{
		Width: info.Width, Height: info.Height, FPS: p.fps, Bitrate: p.bitrate,
		Codec: info.Codec(), CodecConfig: conf.avcC, Encoder: p.sc.cfg.Encoder, Hardware: true,
		ScreenWidth: w, ScreenHeight: h, Input: inErr == "", InputError: inErr,
		Unsupported: providedUnsupported(inErr), Generation: p.hellos,
	}
	if !p.enqueueJSON(frameHello, hello, false) {
		return false
	}
	p.helloConf, p.needHello = conf, false
	return true
}

// onCongestion lowers the session's automatic bitrate (link congestion) and,
// when it repeats, its frame rate; see hostSession.onCongestion.
func (p *providedSession) onCongestion(now time.Time, link bool) {
	p.lastDrop = now
	if now.Sub(p.start) < startupGrace {
		return
	}
	if link && p.opts.Bitrate == 0 {
		p.bitrate = max(minAutoBitrate, p.targetBitrate()/4, int64(float64(p.bitrate)*backoffFactor))
	}
	recent := p.congestion[:0]
	for _, t := range p.congestion {
		if now.Sub(t) < congestionSpan {
			recent = append(recent, t)
		}
	}
	p.congestion = append(recent, now)
	if len(p.congestion) >= 2 && p.fps > minAdaptFPS {
		p.fps = max(minAdaptFPS, p.fps*2/3)
		p.congestion = p.congestion[:0]
		if p.opts.Bitrate == 0 {
			p.bitrate = min(p.bitrate, p.targetBitrate())
		}
	}
	p.changed()
}

// adapt asks for the key frame a drop waits on once the backlog has
// drained, lowers the bitrate when acks show a queue building, and after a
// quiet period brings frame rate and bitrate back up.
func (p *providedSession) adapt(now time.Time) {
	if p.waitKey && int(p.seq-p.acked) <= p.windowFrames()/2 {
		p.askKeyframe(now)
	}
	if !p.waitKey && p.acks.sustainedQueue(now, p.framePeriod()) && now.Sub(p.lastDrop) > 2*time.Second {
		p.acks.highFrom = time.Time{}
		p.onCongestion(now, true)
	}
	if now.Sub(p.lastDrop) < recoverAfter || p.lastDrop.IsZero() {
		return
	}
	changed := false
	if want := min(p.opts.FPS, p.sc.cfg.MaxFPS); p.fps < want {
		p.fps = min(want, p.fps*3/2)
		changed = true
	}
	if target := p.targetBitrate(); p.opts.Bitrate == 0 && p.bitrate < target {
		p.bitrate = min(target, int64(float64(p.bitrate)*recoverFactor))
		changed = true
	}
	if changed {
		p.lastDrop = now // the next step after another quiet period
		p.changed()
	}
}

func (p *providedSession) onControl(c Control) {
	now := time.Now()
	switch c.Type {
	case ctlAck:
		if c.Seq-p.acked <= p.seq-p.acked {
			p.acked = c.Seq
			p.acks.onAck(c.Seq, now)
		}
	case ctlKeyframe:
		p.askKeyframe(now)
	case ctlBehind:
		if !p.waitKey {
			p.onCongestion(now, false)
		}
		p.waitKey = true
	case ctlConfig:
		next := p.opts
		if c.FPS != 0 {
			next.FPS = c.FPS
		}
		switch {
		case c.Bitrate < 0:
			next.Bitrate = 0
		case c.Bitrate > 0:
			next.Bitrate = c.Bitrate
		}
		if c.MaxHeight < 0 {
			next.MaxHeight = 0
		} else if c.MaxHeight > 0 {
			next.MaxHeight = c.MaxHeight
		}
		if c.Scale != 0 {
			next.Scale = c.Scale
		}
		if c.Display != nil {
			next.Display = *c.Display
		}
		if err := next.normalize(); err != nil {
			p.reply(c, err)
			return
		}
		if next.Display > 1 || next.Display == DisplayAll {
			p.reply(c, errors.New("this device has one screen"))
			return
		}
		p.reply(c, nil)
		p.opts = next
		p.fps = min(next.FPS, p.sc.cfg.MaxFPS)
		p.bitrate = p.targetBitrate()
		p.needHello = true // the stream's parameters change
		p.changed()
		p.askKeyframe(now)
	}
}

func (p *providedSession) sendStats(now time.Time) {
	dt := now.Sub(p.lastStatsAt).Seconds()
	st := HostStats{
		FPS:      float64(p.sentFrames) / dt,
		Bitrate:  p.bitrate,
		SendRate: int64(float64(p.sentBytes*8) / dt),
		Dropped:  p.dropped,
		InFlight: int(p.seq - p.acked),
		Paused:   p.paused,
		AckMS:    float64(p.acks.srtt.Microseconds()) / 1000,
		WindowMS: float64(p.acks.window(now).Milliseconds()),
	}
	p.sentFrames, p.sentBytes, p.lastStatsAt = 0, 0, now
	p.enqueueJSON(frameStats, st, true)
}

func (p *providedSession) enqueueJSON(typ byte, v any, droppable bool) bool {
	body, err := json.Marshal(v)
	if err != nil {
		return false
	}
	msg := make([]byte, 5+len(body))
	putFrameHead(msg, typ, len(body))
	copy(msg[5:], body)
	return p.enqueue(msg, droppable)
}

func (p *providedSession) enqueue(msg []byte, droppable bool) bool {
	select {
	case p.out <- msg:
		return true
	default:
	}
	if droppable {
		return false
	}
	select {
	case p.out <- msg:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	case <-p.ctx.Done():
		return false
	}
}

func (p *providedSession) writer() {
	defer close(p.wdone)
	for {
		select {
		case <-p.ctx.Done():
			return
		case msg := <-p.out:
			if _, err := p.s.Write(msg); err != nil {
				p.werr = err
				return
			}
		}
	}
}

// fatal reports err to the client before the session ends.
func (p *providedSession) fatal(err error) {
	body, _ := json.Marshal(errorMsg{Error: err.Error()})
	msg := make([]byte, 5+len(body))
	putFrameHead(msg, frameError, len(body))
	copy(msg[5:], body)
	if p.enqueue(msg, false) {
		deadline := time.After(2 * time.Second)
		for len(p.out) > 0 {
			select {
			case <-deadline:
				return
			case <-p.wdone:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *providedSession) reply(c Control, err error) {
	if c.ID == 0 {
		return
	}
	r := Reply{ID: c.ID, Type: c.Type}
	if err != nil {
		r.Error = err.Error()
	}
	p.enqueueJSON(frameReply, r, false)
}

// readControl reads client messages: input is delivered right here, in
// order; the rest goes to run.
func (p *providedSession) readControl() {
	defer close(p.ctl)
	for {
		var c Control
		if err := wire.ReadJSON(p.s, &c); err != nil {
			return
		}
		switch {
		case c.Type == ctlPing:
			p.enqueueJSON(framePong, pong{T: c.T, Host: time.Now().UnixMicro()}, true)
		case isInput(c.Type):
			p.inject(c.input())
		case c.Type == ctlType:
			p.reply(c, p.typeText(c.Text))
		case c.Type == ctlLock || c.Type == ctlBlockInput || c.Type == ctlBlackout:
			p.reply(c, errors.New("not available on this device"))
		default:
			select {
			case p.ctl <- c:
			case <-p.ctx.Done():
				return
			}
		}
	}
}

func (p *providedSession) inject(ev InputEvent) {
	p.injMu.Lock()
	defer p.injMu.Unlock()
	if p.inErr != "" || p.sc.cfg.Input == nil {
		return
	}
	if ev.Kind == InputReleaseAll {
		p.sc.cfg.Input.ReleaseAll()
		p.holding = false
		return
	}
	ev.X, ev.Y = clamp01(ev.X), clamp01(ev.Y)
	if err := p.sc.cfg.Input.Inject(ev); err != nil {
		slog.Debug("desktop: input", "err", err)
		return
	}
	p.holding = true
}

func (p *providedSession) typeText(text string) error {
	if len(text) > maxTypeText || !utf8.ValidString(text) {
		return fmt.Errorf("text to type must be valid UTF-8 of at most %d bytes", maxTypeText)
	}
	p.injMu.Lock()
	defer p.injMu.Unlock()
	if p.inErr != "" || p.sc.cfg.Input == nil {
		return errors.New("input is off for this session: " + p.inErr)
	}
	return p.sc.cfg.Input.TypeText(text)
}
