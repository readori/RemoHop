package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/nowindow"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/wire"
)

func init() {
	peer.Handle(ServiceName, serveDesktop)
}

// hostViewOnlyEnv makes a host refuse input on every desktop session (and
// in the input service).
const hostViewOnlyEnv = inject.ViewOnlyEnv

const (
	// keyframeSlack: a key frame request is served by the next scheduled
	// key frame if that is at most this far away, instead of restarting
	// the encoder.
	keyframeSlack = 400 * time.Millisecond
	// minRestartGap rate-limits encoder restarts (each costs a key frame
	// and ~100-300 ms of startup), whoever asks for them.
	minRestartGap = time.Second
	// statsEvery is the host stats period.
	statsEvery = time.Second
	// window is the least video, in time, that may be unacknowledged
	// before the host starts dropping; a longer path gets more (flow.go).
	// It bounds end-to-end queueing latency.
	window = 300 * time.Millisecond
	// minWindowFrames keeps very low frame rates workable on long RTTs.
	minWindowFrames = 4
	// bitrate adaptation for automatic bitrate
	backoffFactor  = 0.7
	recoverFactor  = 1.25
	recoverAfter   = 15 * time.Second
	minAutoBitrate = 500_000
	// ceilHold is how long the bitrate stays just below the rate that last
	// caused congestion before probing past it; that rate is remembered for
	// ceilLife (see probeBitrate).
	ceilHold = time.Minute
	ceilLife = 10 * time.Minute
	// Two congestion events within congestionSpan lower the frame rate,
	// not below minAdaptFPS.
	congestionSpan = 10 * time.Second
	minAdaptFPS    = 10
	// keyRestartGap spaces out encoder restarts made only to get a key
	// frame; a request inside it is served when it ends (or by a key frame
	// that comes first), not dropped.
	keyRestartGap = 2 * time.Second
	// startupGrace is how long after the session starts congestion does
	// not lower the bitrate or frame rate.
	startupGrace = 3 * time.Second
	// The frame rate is fitted to what capture and encoder sustain: a
	// pipeline that cannot keep up with its source does not drop frames,
	// it queues them (ffmpeg 7 keeps up to eight frames before each of its
	// decoder, filter and encoder threads), and every queued frame is
	// latency. Measured on a 4-core box, libx264 at 1080p kept up with 47
	// of 60 fps and frames arrived 84 ms after capture; asked for 40 fps,
	// 17 ms. A pipeline whose frames come slower than asked (below
	// fitSlowRatio of the rate) and late (fitLate frame periods plus
	// fitLateMS) for fitHold, starting fitAfter after an encoder start, is
	// given a rate just under what it delivered; the rate is not raised
	// above that again for fitCeilingLife.
	fitAfter       = 3 * time.Second
	fitHold        = 2 * time.Second
	fitSlowRatio   = 0.92
	fitLate        = 2.5
	fitLateMS      = 20
	fitCeilingLife = 2 * time.Minute
	// firstFrameTimeout is how long a freshly started encoder may take to
	// deliver anything. A capture device or encoder that starts but never
	// produces a frame would otherwise leave the viewer waiting forever
	// without a reason.
	firstFrameTimeout = 15 * time.Second
)

func serveDesktop(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var opts Options
	err := peer.DecodeReq(raw, &opts)
	if err == nil {
		err = opts.normalize()
	}
	if err != nil {
		writeFatal(s, fmt.Errorf("bad request: %w", err))
		return err
	}
	// A host whose owner turned the input feature off still lets the
	// client watch; it just cannot drive.
	hostOff := !hostd.Allowed(c, perm.Input)
	if opts.Probe {
		return serveProbe(ctx, s, opts, hostOff)
	}
	h, err := newHostSession(ctx, s, opts, hostOff, fastPath(c))
	if err != nil {
		writeFatal(s, err)
		return err
	}
	// The owner can switch input off (and on again) while the session
	// runs: it applies to this open stream at once, not just to the next.
	stop := watchInput(c, h.setHostInput)
	defer stop()
	h.setHostInput(hostd.Allowed(c, perm.Input)) // a change before the watch began
	return h.run()
}

// fastPathRTT is the largest smallest-round-trip of a path taken to have
// bandwidth to spare (a LAN, Wi-Fi included). Over such a path the
// automatic bitrate is doubled and the encoders get a larger rate-control
// buffer (see autoBitrate and vbv).
const fastPathRTT = 15 * time.Millisecond

// fastPath reports whether the session's connection runs over a fast path:
// directly (not relayed, not through a websocket bridge) with a LAN-like
// round trip. A variable so tests can choose.
var fastPath = func(c *peer.Conn) bool {
	if c == nil {
		return false
	}
	if s := hostd.SessionOf(c); s != nil && s.Path() == "relayed" {
		return false
	}
	q := peer.QUICConn(c)
	if q == nil {
		return false
	}
	rtt := q.ConnectionStats().MinRTT
	return rtt > 0 && rtt <= fastPathRTT
}

// watchInput calls fn whenever the host's owner allows or stops allowing
// keyboard and mouse control for the session behind c, until stop is
// called. A variable so tests can drive it.
var watchInput = func(c *peer.Conn, fn func(allowed bool)) (stop func()) {
	return hostd.WatchFeature(c, perm.Input, fn)
}

// hostInputOff is the reason a session has no input when the host's owner
// switched the input feature off.
const hostInputOff = "the host's owner switched keyboard and mouse control off (feature \"input\")"

func writeFatal(s peer.Stream, err error) {
	s.SetWriteDeadline(time.Now().Add(2 * time.Second))
	body, _ := json.Marshal(errorMsg{Error: err.Error()})
	wire.WriteFrame(s, frameError, body)
}

// inputRefusal says why a session may not send input ("" if it may): the
// client asked for view-only, or the host's owner switched input off (with
// the input feature, hostOff, or for the whole host with hostViewOnlyEnv).
func inputRefusal(opts Options, hostOff bool) string {
	switch {
	case opts.ViewOnly:
		return "view-only session"
	case inject.ViewOnly():
		return "the host only allows viewing (" + hostViewOnlyEnv + ")"
	case hostOff:
		return hostInputOff
	}
	return ""
}

// serveProbe answers a probe: what the host would offer, without capturing.
func serveProbe(ctx context.Context, s peer.Stream, opts Options, hostOff bool) error {
	info := HostInfo{OS: runtime.GOOS, Encoders: map[string]string{}}
	ffmpeg, err := ffmpegPath()
	if err == nil {
		var spec captureSpec
		if spec, err = discoverCapture(0, opts.FPS); err == nil {
			info.ScreenWidth, info.ScreenHeight = spec.width, spec.height
			info.Displays, info.Warning = spec.displays, spec.warning
			info.InputError = inputRefusal(opts, hostOff)
			if info.InputError == "" {
				if inj, ierr := newInjector(spec); ierr != nil {
					info.InputError = ierr.Error()
				} else {
					inj.close()
				}
			}
			info.Input = info.InputError == ""
			info.Unsupported = unsupportedControls(info.InputError)
			var firstErr error
			for _, codec := range []string{codecAV1, codecH264} {
				e, eerr := selectEncoder(ctx, ffmpeg, encRequest{codec: codec})
				if eerr == nil {
					info.Encoders[codec] = e.name
				} else if firstErr == nil {
					firstErr = eerr
				}
			}
			if len(info.Encoders) == 0 {
				err = firstErr
			}
		}
	}
	if err != nil {
		writeFatal(s, err)
		return err
	}
	body, _ := json.Marshal(info)
	s.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return wire.WriteFrame(s, frameInfo, body)
}

// encFrame is one frame read from an encoder process: an AV1 temporal unit
// or an H.264 access unit, or (config) H.264's avcC record.
type encFrame struct {
	gen    int
	config bool
	data   []byte
	ptsUS  int64
	readAt time.Time
}

type encExit struct {
	gen    int
	err    error
	frames int
	// shared: the encoder read a capture shared between sessions, which
	// can end under it (and is started again by the next encoder).
	shared bool
	// gpu: the encoder was fed by the capture's GPU chain.
	gpu bool
}

// encProc is one running ffmpeg.
type encProc struct {
	gen     int
	cmd     *exec.Cmd
	stderr  *tailBuffer
	bitrate int64
	fps     int
	h264    bool

	// release gives back what the process was started with (its place
	// at a shared capture).
	release func()
	shared  bool
	gpu     bool

	mu     sync.Mutex
	reaped bool
	done   chan struct{} // closed once reaped
}

// kill takes down the encoder's process group, unless it has already been
// reaped (its PID, and so the group ID, could then belong to someone else).
func (p *encProc) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		killGroup(p.cmd)
	}
	p.release()
}

// reap kills and waits for the encoder. Holding mu across Wait is fine: the
// group was just killed, so Wait returns at once.
func (p *encProc) reap() {
	p.mu.Lock()
	defer p.mu.Unlock()
	killGroup(p.cmd)
	p.cmd.Wait()
	p.reaped = true
	p.release()
	close(p.done)
}

// wait waits up to d for the process to be gone.
func (p *encProc) wait(d time.Duration) {
	select {
	case <-p.done:
	case <-time.After(d):
	}
}

type hostSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	s      peer.Stream
	opts   Options

	ffmpeg string
	enc    *encoder
	spec   captureSpec
	start  time.Time
	// fast: the connection runs over a fast path (fastPath).
	fast bool

	// Input. The host's owner can take it away and give it back while
	// the session runs (setHostInput): injMu serialises every injection
	// with that and with teardown, and guards the fields below it.
	injMu sync.Mutex
	inj   injector // nil while the session has no input
	// ctlSpec is spec for readControl's goroutine (Lock needs the
	// captured display): run owns spec and copies it here (setSpec).
	ctlSpec captureSpec
	inErr   string // why inj is nil
	hostOff bool   // the owner switched input off
	// viewOnly is what the client asked for when it opened the session.
	viewOnly bool
	// inputOff mirrors hostOff without the lock, so that text being
	// typed stops between characters when input is switched off.
	inputOff atomic.Bool
	// inputChanged tells run to bring input back (an injector needs the
	// capture area, which run owns) and to announce the change.
	inputChanged chan struct{}

	frames chan encFrame
	exits  chan encExit
	ctl    chan Control
	out    chan []byte
	wdone  chan struct{}
	werr   error

	// Everything below is owned by run's goroutine.
	proc *encProc
	// next is an encoder started to replace proc (make before break, see
	// startEncoder): proc keeps streaming until next delivers its first
	// frame, a key frame. nextCfg is its avcC record, held until then.
	next            *encProc
	nextCfg         *encFrame
	noOverlap       bool // replacing while proc runs failed once: stop it first
	lastGen         int  // last generation handed out
	lastScreenCheck time.Time
	// keyOnly: the pending restart (restartAt) is only for a key frame,
	// so a key frame the encoder sends first makes it unnecessary.
	keyOnly     bool
	gen         int
	helloGen    int      // generation whose hello was sent
	seqHdr      []byte   // AV1: the sequence header OBU
	reduced     bool     // AV1: reduced_still_picture_header
	avc         *AVCInfo // H.264: from the avcC record
	avcC        []byte
	seq         uint32 // last sequence number assigned
	acked       uint32
	acks        ackClock
	waitKey     bool
	lastKey     time.Time
	lastRestart time.Time
	restartAt   time.Time // pending deferred restart, zero if none
	lastDrop    time.Time
	bitrate     int64 // current target
	fps         int
	failures    []encFailure
	// ceilBitrate is the bitrate that last caused link congestion, at
	// ceilAt (see probeBitrate); ceilFails counts probes past it that
	// failed in a row, each doubling the wait before the next.
	ceilBitrate int64
	ceilAt      time.Time
	ceilFails   int
	congestion  []time.Time
	// delivered: an encoder of this session has delivered a frame (the
	// screen can be captured here). While it cannot be for now (paused,
	// the reason), the session waits and retries (pause).
	delivered   bool
	paused      string
	pausedSince time.Time
	pauseTries  int
	// gpuSuspect: the GPU chain failed in this session; once the default
	// chain works, later sessions are kept off it too (gpuFailed).
	gpuSuspect bool
	// excluded are encoders that produced nothing in this session, and
	// encWarn says which one it uses instead.
	excluded []string
	encWarn  string
	// lastKeyRestart is when a restart was last made to get a key frame.
	lastKeyRestart time.Time
	// gpuChain: the next encoder is fed by the capture's GPU chain
	// (captureSpec.gpu); noGPU: that chain failed in this session.
	gpuChain, noGPU bool

	// Throughput of the current encoder over the current second (see
	// fitAfter): frames, and the sum and count of capture-to-encoded
	// delays in milliseconds.
	encWinStart  time.Time
	encWinFrames int
	encWinPipe   float64
	encWinPipeN  int
	slowSince    time.Time
	fpsCeil      int // highest rate the pipeline sustained, 0 if unknown
	fpsCeilAt    time.Time
	limitWarn    string // why the rate is below the one asked for

	// stats
	sentFrames  int
	sentBytes   int64
	dropped     uint64
	restarts    int
	pipelineEW  float64
	lastStatsAt time.Time
}

func newHostSession(ctx context.Context, s peer.Stream, opts Options, hostOff, fast bool) (*hostSession, error) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		return nil, err
	}
	spec, err := discoverCapture(opts.Display, opts.FPS)
	if err != nil {
		return nil, err
	}
	h := &hostSession{
		s: s, opts: opts,
		ffmpeg: ffmpeg, spec: spec, ctlSpec: spec, start: time.Now(),
		frames: make(chan encFrame, 4),
		exits:  make(chan encExit, 4),
		ctl:    make(chan Control, 256),
		wdone:  make(chan struct{}),
		fps:    opts.FPS,
		fast:   fast,

		viewOnly:     opts.ViewOnly,
		hostOff:      hostOff,
		inputChanged: make(chan struct{}, 1),
	}
	h.inputOff.Store(hostOff)
	req := encRequest{forced: opts.Encoder, codec: opts.Codec, decode: opts.Decode}
	h.enc, err = selectEncoder(ctx, ffmpeg, req)
	if err != nil {
		return nil, err
	}
	h.enc = preferGPUChain(ctx, ffmpeg, spec, h.enc, req)
	h.ctx, h.cancel = context.WithCancel(ctx)
	h.bitrate = h.targetBitrate()
	// Room for a full window of video at the highest frame rate a config
	// change can select, plus control traffic.
	h.out = make(chan []byte, int(maxFPS*window.Seconds())*2+32)
	if h.inErr = inputRefusal(opts, hostOff); h.inErr == "" {
		inj, err := newInjector(spec)
		if err != nil {
			h.inErr = err.Error()
		} else {
			h.inj = inj
		}
	}
	return h, nil
}

// preferGPUChain swaps enc for another GPU encoder of the same class whose
// GPU chain works with this capture, when enc's does not. On a laptop with
// two GPUs the panel is usually driven by the integrated one, so Desktop
// Duplication's textures live there: NVENC, ranked first, cannot take them
// and every frame goes through the CPU (download and conversion, the most
// expensive path at 1440p and 4K), while Quick Sync on the same machine
// takes them in place.
func preferGPUChain(ctx context.Context, ffmpeg string, spec captureSpec, enc *encoder, req encRequest) *encoder {
	if spec.gpu == nil || !enc.hw || req.forced != "" {
		return enc
	}
	if _, ok := spec.gpu(enc, 0, 0); ok {
		return enc
	}
	class := classOf(enc, false)
	exclude := append(append([]string(nil), req.exclude...), enc.name)
	for range 4 {
		r := req
		r.exclude = exclude
		e, err := selectEncoder(ctx, ffmpeg, r)
		if err != nil || !e.hw || classOf(e, false) != class {
			return enc
		}
		if _, ok := spec.gpu(e, 0, 0); ok {
			slog.Debug("desktop: using the GPU encoder whose GPU chain works with this capture", "instead_of", enc.name, "encoder", e.name)
			return e
		}
		exclude = append(exclude, e.name)
	}
	return enc
}

// setHostInput applies the host owner's input switch to the running
// session. Switching it off takes effect before setHostInput returns:
// every key and button the client holds is released and nothing it sends
// afterwards is injected. The picture keeps coming; run then tells the
// client, with a new Hello, that the session is view-only. Switching it
// back on gives the session input again (unless the client asked for
// view-only), once run has set it up.
func (h *hostSession) setHostInput(allowed bool) {
	h.inputOff.Store(!allowed)
	h.injMu.Lock()
	changed := h.hostOff == allowed
	h.hostOff = !allowed
	if !allowed {
		if h.inj != nil {
			h.inj.close() // lets go of everything held
			h.inj = nil
		}
		if h.inErr == "" || changed {
			h.inErr = inputRefusal(h.opts0(), true)
		}
	}
	h.injMu.Unlock()
	if changed {
		select {
		case h.inputChanged <- struct{}{}:
		default:
		}
	}
}

// opts0 is the options the session was opened with, as far as input is
// concerned (run owns h.opts; ViewOnly never changes).
func (h *hostSession) opts0() Options { return Options{ViewOnly: h.viewOnly} }

// onInputChanged runs on run's goroutine after the owner switched input
// on or off: it brings input back if it is allowed again and restarts the
// encoder, so that the client gets a Hello saying what it may do now
// (before the next key frame, as every Hello is).
func (h *hostSession) onInputChanged(now time.Time) {
	h.injMu.Lock()
	want := !h.hostOff && h.inj == nil
	h.injMu.Unlock()
	if want {
		var inj injector
		reason := inputRefusal(h.opts0(), false)
		if reason == "" {
			var err error
			if inj, err = newInjector(h.spec); err != nil {
				reason = err.Error()
			}
		}
		h.injMu.Lock()
		if !h.hostOff && h.inj == nil {
			h.inj, h.inErr, inj = inj, reason, nil
		}
		h.injMu.Unlock()
		if inj != nil {
			inj.close() // switched off again meanwhile
		}
	}
	h.scheduleRestart(now)
}

// outputPixels estimates the encoded frame size, for the automatic bitrate.
func (h *hostSession) outputPixels() int {
	if h.spec.width == 0 {
		return 0
	}
	k := h.scaleFactor()
	w, hh := int(float64(h.spec.width)*k)&^1, int(float64(h.spec.height)*k)&^1
	return w * hh
}

func (h *hostSession) scaleFactor() float64 {
	k := h.opts.Scale
	if h.opts.MaxHeight > 0 && h.spec.height > 0 {
		k = min(k, float64(h.opts.MaxHeight)/float64(h.spec.height))
	}
	return min(k, 1)
}

func (h *hostSession) targetBitrate() int64 {
	if h.opts.Bitrate > 0 {
		return h.opts.Bitrate
	}
	return autoBitrate(h.outputPixels(), h.fps, h.fast)
}

func (h *hostSession) windowFrames() int {
	return max(minWindowFrames, int(float64(h.fps)*h.acks.window(time.Now()).Seconds()+0.5))
}

func (h *hostSession) framePeriod() time.Duration { return time.Second / time.Duration(max(h.fps, 1)) }

// gopFrames is the interval of the encoder's own key frames. They are only
// a fallback: a viewer that needs one asks, and gets it from a replacement
// encoder without a gap (startEncoder). Hardware encoders get a long one,
// because with the small rate-control buffer they run with off a fast path
// every key frame softens a still screen for a quarter of a second
// (measured with x264 standing in, 1080p60 text at 7.5 Mbit/s and a tenth
// of a second of buffer: SSIM 0.9994 dropping to 0.960, 21 of every 120
// frames visibly softer); libx264, with a quarter of a second of buffer,
// shows no such pulse and keeps two seconds.
func (h *hostSession) gopFrames() int {
	if h.enc != nil && h.enc.hw {
		return 10 * h.fps
	}
	return 2 * h.fps
}

// cpuColor is how swscale converts for the current encoder, size and rate.
func (h *hostSession) cpuColor() colorMode { return h.enc.cpuColor(h.outputPixels(), h.fps) }

// scaleFilter keeps the aspect ratio and even dimensions (4:2:0 needs
// them). Expressions are single-quoted so their commas are not taken as
// filter separators.
func (h *hostSession) scaleFilter() string {
	k := fmt.Sprintf("%g", h.opts.Scale)
	if h.opts.MaxHeight > 0 {
		k = fmt.Sprintf("min(%g,%d/ih)", h.opts.Scale, h.opts.MaxHeight)
	}
	return fmt.Sprintf("scale=w='trunc(iw*min(1,%s)/2)*2':h='trunc(ih*min(1,%s)/2)*2':flags=bilinear", k, k) + h.cpuColor().scaleOpts()
}

func (h *hostSession) ffmpegArgs() ([]string, captureSpec, error) {
	spec, err := discoverCapture(h.opts.Display, h.fps)
	if err != nil {
		return nil, spec, err
	}
	return h.argsFor(spec), spec, nil
}

// argsFor builds the encoder's command line for a capture.
func (h *hostSession) argsFor(spec captureSpec) []string {
	// Capture devices describe their stream up front, so skip probing: it
	// would otherwise wait for several frames (2 s at 10 fps) before the
	// first one is encoded.
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-fflags", "nobuffer",
		"-probesize", "32", "-analyzeduration", "0"}
	if h.enc.preInput != nil {
		args = append(args, h.enc.preInput()...)
	}
	args = append(args, spec.input...)
	var chain []string
	if spec.filter != "" {
		chain = append(chain, spec.filter)
	}
	chain = append(chain, h.scaleFilter(), h.enc.pixFmt)
	color := h.cpuColor()
	h.gpuChain = false
	// A scaled picture keeps the GPU chain where the chain can scale (Quick
	// Sync's video processor): a 4K host viewed at 1080p is the case where
	// downloading and converting every full frame on the CPU costs most.
	sw, sh := 0, 0
	if k := h.scaleFactor(); k < 1 {
		sw, sh = int(float64(spec.width)*k)/2*2, int(float64(spec.height)*k)/2*2
	}
	if spec.gpu != nil && !h.noGPU && (sw > 0 || h.scaleFactor() >= 1) {
		if c, ok := spec.gpu(h.enc, sw, sh); ok {
			h.gpuChain, chain, color = true, nil, gpuColor(h.enc)
			if c != "" {
				chain = []string{c}
			}
		}
	}
	args = append(args, "-an")
	if len(chain) > 0 {
		args = append(args, "-vf", strings.Join(chain, ","))
	}
	// -copyts with passthrough timing keeps the capture device's
	// timestamps, which for x11grab are wall-clock: the host then knows
	// when each frame was captured and the viewer can show true latency.
	args = append(args, "-copyts", "-fps_mode", "passthrough", "-enc_time_base", "1:1000")
	args = append(args, h.enc.args(encParams{fps: h.fps, bitrate: h.bitrate, gop: h.gopFrames(), pixels: h.outputPixels(), fast: h.fast})...)
	args = append(args, color.encArgs()...)
	args = append(args, "-flush_packets", "1")
	if h.enc.codec == codecH264 {
		args = append(args, "-flvflags", "no_duration_filesize", "-f", "flv", "pipe:1")
	} else {
		args = append(args, "-f", "ivf", "pipe:1")
	}
	return args
}

// startEncoder launches a new ffmpeg generation, replacing the running one.
//
// A restart (for a key frame, another bitrate or frame rate, another
// monitor) is made before break: the new encoder starts while the old one
// keeps streaming, and takes over when it delivers its first frame, a key
// frame (onNextFrame). The viewer sees no gap: stopping first left it
// without a frame for the new encoder's start-up, measured at 110-200 ms
// with libx264 on Linux and longer for a GPU encoder behind Desktop
// Duplication. Where the old capture or encoder cannot share the screen or
// the GPU with the new one (the new one fails), the session falls back to
// stopping first (noOverlap).
func (h *hostSession) startEncoder() error {
	overlap := h.proc != nil && !h.noOverlap
	if h.next != nil {
		h.next.kill()
		h.next, h.nextCfg = nil, nil
	}
	if !overlap && h.proc != nil {
		// The old capture may hold the screen or the GPU until it is
		// gone (one that cannot share them is why there is no overlap).
		old := h.proc
		h.stopEncoder()
		old.wait(time.Second)
	}
	args, spec, err := h.ffmpegArgs()
	if err != nil {
		return err
	}
	if spec.x != h.spec.x || spec.y != h.spec.y || spec.width != h.spec.width || spec.height != h.spec.height {
		// The screen changed under the session (another resolution; on a
		// Windows service, another user's session on the console): aim
		// input at what is captured now.
		h.setSpec(spec)
	}
	h.lastGen++
	gen := h.lastGen
	cmd := nowindow.Set(exec.Command(h.ffmpeg, args...))
	cmd.Env = h.spec.env
	release := func() {}
	if spec.attach != nil {
		// The screen comes from a capture shared between sessions.
		stdin, rel, err := spec.attach(h.fps)
		if err != nil {
			return err
		}
		defer stdin.Close()
		cmd.Stdin, release = stdin, rel
	}
	shared := spec.attach != nil
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		release()
		return err
	}
	tail := &tailBuffer{max: 8 << 10}
	cmd.Stderr = tail
	if err := startGroup(cmd); err != nil {
		release()
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	p := &encProc{gen: gen, cmd: cmd, done: make(chan struct{}), stderr: tail, bitrate: h.bitrate, fps: h.fps, h264: h.enc.codec == codecH264, release: release, shared: shared, gpu: h.gpuChain}
	h.lastRestart = time.Now()
	h.restartAt, h.keyOnly = time.Time{}, false
	if overlap {
		h.next = p
	} else {
		h.adopt(p)
	}
	slog.Debug("desktop: encoder started", "gen", p.gen, "encoder", h.enc.name, "fps", h.fps, "bitrate", h.bitrate, "gpu_chain", p.gpu, "fast_path", h.fast, "overlap", overlap)
	go h.readEncoder(p, stdout)
	return nil
}

// setSpec makes spec the captured area: input aims at it, and Lock finds
// its display.
func (h *hostSession) setSpec(spec captureSpec) {
	h.spec = spec
	h.injMu.Lock()
	h.ctlSpec = spec
	if h.inj != nil {
		h.inj.retarget(spec)
	}
	h.injMu.Unlock()
}

// adopt makes p the session's encoder.
func (h *hostSession) adopt(p *encProc) {
	h.proc, h.gen = p, p.gen
	h.encWinStart, h.encWinFrames, h.encWinPipe, h.encWinPipeN, h.slowSince = time.Now(), 0, 0, 0, time.Time{}
}

// onNextFrame handles a frame from the encoder that is to replace the
// running one: its avcC record is kept, its first frame hands the session
// over to it.
func (h *hostSession) onNextFrame(f encFrame) {
	if f.config {
		h.nextCfg = &f
		return
	}
	old := h.proc
	h.adopt(h.next)
	h.next = nil
	if old != nil {
		old.kill()
	}
	if c := h.nextCfg; c != nil {
		h.nextCfg = nil
		h.classify(c)
	}
	h.onFrame(f)
}

// onNextExit handles the replacement encoder ending before it took over.
// The running one goes on; the replacement is tried again with the old one
// stopped first, in case the two could not run side by side.
func (h *hostSession) onNextExit(e encExit) {
	slog.Debug("desktop: replacement encoder failed while the old one ran; restarting without overlap", "err", e.err)
	h.next, h.nextCfg = nil, nil
	h.noOverlap = true
	h.scheduleRestart(time.Now())
}

func (h *hostSession) stopEncoder() {
	if h.next != nil {
		h.next.kill()
		h.next, h.nextCfg = nil, nil
	}
	if h.proc != nil {
		h.proc.kill()
		h.proc = nil
	}
}

// readEncoder pumps one ffmpeg's output (IVF for AV1, FLV for H.264) into
// h.frames. It never stalls ffmpeg for long: the run loop consumes promptly
// and does its dropping there, so the encoder always works on fresh
// captures.
func (h *hostSession) readEncoder(p *encProc, stdout io.Reader) {
	n := 0
	var started, stalled atomic.Bool
	watch := time.AfterFunc(firstFrameTimeout, func() {
		if !started.Load() {
			stalled.Store(true)
			p.kill()
		}
	})
	defer watch.Stop()
	push := func(f encFrame) error {
		started.Store(true)
		f.gen, f.readAt = p.gen, time.Now()
		if p.h264 {
			// FLV carries 31 bits of the capture time in milliseconds.
			ms := uint32(f.ptsUS)
			if us, ok := flvTime(ms, f.readAt.UnixMilli()); ok {
				f.ptsUS = us
			} else {
				f.ptsUS = int64(ms) * 1000
			}
		}
		select {
		case h.frames <- f:
			return nil
		case <-h.ctx.Done():
			return h.ctx.Err()
		}
	}
	err := func() error {
		if p.h264 {
			fr, err := newFLVReader(stdout)
			if err != nil {
				return err
			}
			for {
				pkt, err := fr.next()
				if err != nil {
					return err
				}
				if !pkt.config {
					n++
				}
				if err := push(encFrame{config: pkt.config, data: pkt.data, ptsUS: int64(pkt.ts)}); err != nil {
					return err
				}
			}
		}
		ir, err := newIVFReader(stdout)
		if err != nil {
			return err
		}
		for {
			data, pts, err := ir.next()
			if err != nil {
				return err
			}
			n++
			if err := push(encFrame{data: data, ptsUS: ir.micros(pts)}); err != nil {
				return err
			}
		}
	}()
	p.reap()
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	if stalled.Load() {
		err = fmt.Errorf("ffmpeg delivered no video within %v of starting (capture or encoder stalled)", firstFrameTimeout)
		if msg := p.stderr.errorText(); msg != "" {
			err = fmt.Errorf("%v: %s", err, msg)
		}
	} else if msg := p.stderr.errorText(); msg != "" {
		err = fmt.Errorf("ffmpeg: %s", msg)
	} else if err == nil {
		err = errors.New("ffmpeg exited")
	}
	select {
	case h.exits <- encExit{gen: p.gen, err: err, frames: n, shared: p.shared, gpu: p.gpu}:
	case <-h.ctx.Done():
	}
}

func (h *hostSession) run() error {
	defer h.cancel()
	go h.writer()
	go h.readControl()
	defer func() {
		h.stopEncoder()
		h.cancel()
		<-h.wdone
		// readControl may still deliver an event until the stream is
		// closed; injMu orders it before or after this release.
		h.injMu.Lock()
		if h.inj != nil {
			h.inj.close()
			h.inj = nil
		}
		h.injMu.Unlock()
	}()
	if err := h.startEncoder(); err != nil {
		h.fatal(err)
		return err
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	// A restart (a key frame for the viewer, a new rate or size) starts
	// when it is due, not at the next tick: waiting for the tick added up
	// to 100 ms to every recovery.
	restart := time.NewTimer(time.Hour)
	restart.Stop()
	defer restart.Stop()
	var armed time.Time
	h.lastStatsAt = time.Now()
	for {
		var restartC <-chan time.Time
		if !h.restartAt.IsZero() && h.next == nil {
			if !armed.Equal(h.restartAt) {
				restart.Reset(time.Until(h.restartAt))
				armed = h.restartAt
			}
			restartC = restart.C
		}
		select {
		case <-h.ctx.Done():
			return h.ended()
		case <-h.wdone:
			return h.werr
		case <-h.inputChanged:
			h.onInputChanged(time.Now())
		case f := <-h.frames:
			switch {
			case h.next != nil && f.gen == h.next.gen:
				h.onNextFrame(f)
			case f.gen == h.gen && h.proc != nil:
				h.onFrame(f)
			}
		case e := <-h.exits:
			if h.next != nil && e.gen == h.next.gen {
				h.onNextExit(e)
				continue
			}
			if e.gen != h.gen || h.proc == nil || h.proc.gen != e.gen {
				continue
			}
			if err := h.onEncoderExit(e); err != nil {
				h.fatal(err)
				return err
			}
		case c, ok := <-h.ctl:
			if !ok {
				return nil // client closed the stream
			}
			h.onControl(c)
		case <-restartC:
			armed = time.Time{}
			if err := h.restartIfDue(time.Now()); err != nil {
				return err
			}
		case now := <-tick.C:
			h.fitRate(now)
			h.adapt(now)
			// After adapt, so that a restart it asks for now goes now.
			if err := h.restartIfDue(now); err != nil {
				return err
			}
			if screenChanged != nil && now.Sub(h.lastScreenCheck) >= screenCheckEvery {
				h.lastScreenCheck = now
				if h.restartAt.IsZero() && screenChanged(h.spec) {
					slog.Debug("desktop: the screen's size changed; capturing it anew")
					h.scheduleRestart(now)
				}
			}
			if now.Sub(h.lastStatsAt) >= statsEvery {
				h.sendStats(now)
			}
		}
	}
}

// restartIfDue starts the pending restart when it is due. One replacement
// at a time: a restart asked for while one is starting waits for it to
// take over.
func (h *hostSession) restartIfDue(now time.Time) error {
	if h.restartAt.IsZero() || now.Before(h.restartAt) || h.next != nil {
		return nil
	}
	if err := h.startEncoder(); err != nil {
		h.fatal(err)
		return err
	}
	h.restarts++
	return nil
}

// ended is run's answer to its context ending. When that is because the
// host's owner switched the screen off for this session (the dispatcher
// cancels the stream's context with a perm.WithdrawnError), the client is
// told so before the stream closes.
func (h *hostSession) ended() error {
	w, ok := perm.Withdrawn(context.Cause(h.ctx))
	if !ok {
		return h.ctx.Err()
	}
	h.stopEncoder()
	<-h.wdone // the writer has stopped: the stream is ours
	writeFatal(h.s, w)
	return w
}

// classify inspects one frame from the encoder: whether a decoder can start
// at it, and (updating the session's codec state) the stream parameters it
// carries. ok is false for input that is not a frame to send.
func (h *hostSession) classify(f *encFrame) (key, ok bool) {
	if h.enc.codec == codecH264 {
		if f.config {
			info, err := parseAVCC(f.data)
			if err != nil {
				slog.Debug("desktop: bad avcC from encoder", "err", err)
				return false, false
			}
			h.avc, h.avcC = &info, f.data
			return false, false
		}
		if h.avc == nil {
			return false, false
		}
		key, err := avcIsIDR(f.data, h.avc.NALLength)
		if err != nil {
			slog.Debug("desktop: bad access unit from encoder", "err", err)
			h.dropped++
			h.waitKey = true
			return false, false
		}
		return key, true
	}
	info, err := classifyTU(f.data, h.reduced)
	if err != nil {
		slog.Debug("desktop: bad temporal unit from encoder", "err", err)
		h.dropped++
		h.waitKey = true
		return false, false
	}
	if info.seq != nil {
		h.reduced = info.seq.ReducedStill
		h.seqHdr = append(h.seqHdr[:0], info.seqOBU...)
	}
	if info.key && info.seq == nil && h.seqHdr != nil {
		// Some encoders emit the sequence header only once; a decoder
		// joining at this key frame needs it in band.
		buf := make([]byte, 0, len(f.data)+len(h.seqHdr))
		buf = append(buf, f.data[:info.tdEnd]...)
		buf = append(buf, h.seqHdr...)
		f.data = append(buf, f.data[info.tdEnd:]...)
	}
	return info.key, h.seqHdr != nil
}

func (h *hostSession) onFrame(f encFrame) {
	key, ok := h.classify(&f)
	if !ok {
		return
	}
	if !h.delivered || h.paused != "" {
		h.onDelivering()
	}
	if key {
		h.lastKey = f.readAt
	}
	h.encWinFrames++
	capture := int64(0)
	// Wall-clock capture timestamps (x11grab, or frames stamped on arrival
	// from a shared capture, which are rounded to the frame period and so
	// may lie a fraction of a frame ahead).
	if d := f.readAt.UnixMicro() - f.ptsUS; d >= -50_000 && d < 10e6 {
		d = max(d, 0)
		capture = f.readAt.UnixMicro() - d
		ms := float64(d) / 1000
		h.encWinPipe += ms
		h.encWinPipeN++
		if h.pipelineEW == 0 {
			h.pipelineEW = ms
		} else {
			h.pipelineEW += (ms - h.pipelineEW) / 16
		}
	}

	if h.helloGen != h.gen {
		if !key {
			h.dropped++
			return
		}
		if !h.sendHello() {
			return
		}
	}

	inflight := int(h.seq - h.acked)
	limit := h.windowFrames()
	switch {
	case h.waitKey && !key, inflight >= limit:
		// A full window is congestion when acks have been coming back
		// late (a queue building up); after a stall on a path that was
		// keeping up it only costs the skip to the next key frame.
		if now := time.Now(); !h.waitKey && h.acks.queueing(now, h.framePeriod()) {
			h.onCongestion(now, true)
		}
		h.waitKey = true
		h.dropped++
		return
	}
	h.waitKey = false
	h.seq++
	ts := capture
	if ts == 0 {
		ts = f.readAt.UnixMicro()
	}
	sentAt := time.Now()
	hdr := frameHeader{key: key, seq: h.seq, pts: ts - h.start.UnixMicro(), capture: capture, sent: sentAt.UnixMicro()}
	msg := make([]byte, 5+frameHeaderLen+len(f.data))
	putFrameHead(msg, frameVideo, frameHeaderLen+len(f.data))
	hdr.put(msg[5:])
	copy(msg[5+frameHeaderLen:], f.data)
	if !h.enqueue(msg, false) {
		h.seq--
		h.dropped++
		h.waitKey = true
		return
	}
	h.acks.onSend(h.seq, sentAt)
	if key && h.keyOnly {
		// The encoder's own schedule beat the restart that was waiting
		// for keyRestartGap to pass.
		h.restartAt, h.keyOnly = time.Time{}, false
	}
	h.sentFrames++
	h.sentBytes += int64(len(f.data))
}

// onDelivering runs when the encoder delivers its first frame after the
// session started or the screen was unavailable.
func (h *hostSession) onDelivering() {
	h.delivered = true
	if h.paused != "" {
		slog.Debug("desktop: the screen can be captured again", "after", time.Since(h.pausedSince).Round(time.Millisecond))
	}
	h.paused, h.pausedSince, h.pauseTries = "", time.Time{}, 0
	if h.gpuSuspect && h.proc != nil && !h.proc.gpu {
		// The default chain works where the GPU chain did not.
		h.gpuSuspect = false
		if h.spec.gpuFailed != nil {
			h.spec.gpuFailed(h.enc)
		}
	}
}

func putFrameHead(b []byte, typ byte, n int) {
	b[0], b[1], b[2], b[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	b[4] = typ
}

func (h *hostSession) sendHello() bool {
	h.injMu.Lock()
	input, inErr := h.inj != nil, h.inErr
	h.injMu.Unlock()
	hello := Hello{
		FPS: h.fps, Bitrate: h.bitrate, Encoder: h.enc.name, Hardware: h.enc.hw,
		ScreenWidth: h.spec.width, ScreenHeight: h.spec.height,
		Input: input, InputError: inErr, Warning: joinWarnings(h.spec.warning, h.enc.warning, h.encWarn, h.limitWarn),
		Display: h.opts.Display, Displays: h.spec.displays, AllDisplays: h.spec.all,
		Unsupported: unsupportedControls(inErr),
		Generation:  h.gen,
	}
	if h.enc.codec == codecH264 {
		// Some encoders (Quick Sync among them) label 1440p60 with level
		// 4.2, which a decoder's support check (WebCodecs
		// isConfigSupported) reads as 1080p at most. The level in the
		// codec string is what that check sees; decoding goes by the
		// stream itself, so it is raised to what the size and rate need.
		info := *h.avc
		if need := h264LevelFor(info.Width, info.Height, h.fps); need > info.Level {
			info.Level = need
		}
		hello.Width, hello.Height, hello.Codec, hello.CodecConfig = info.Width, info.Height, info.Codec(), h.avcC
	} else {
		si, err := parseSequenceHeaderOBU(h.seqHdr)
		if err != nil {
			slog.Debug("desktop: sequence header", "err", err)
			return false
		}
		hello.Width, hello.Height, hello.Codec, hello.CodecConfig = si.Width, si.Height, si.Codec(), si.av1C(h.seqHdr)
	}
	if !h.enqueueJSON(frameHello, hello, false) {
		return false
	}
	h.helloGen = h.gen
	return true
}

// onCongestion runs when the unacknowledged window fills (the link or the
// consumer cannot keep up; link says it may be the network) or the client
// reports it is behind (its decoder is short of CPU). Automatic bitrate
// backs off for the former, never below a quarter of its target; repeated
// congestion of either kind lowers the frame rate.
func (h *hostSession) onCongestion(now time.Time, link bool) {
	h.lastDrop = now
	if now.Sub(h.start) < startupGrace {
		// A viewer that is still setting up its decoder (and decoding
		// its first, large key frame) is not a slow link: drop, but
		// do not lower the quality for the rest of the session.
		return
	}
	if link && h.opts.Bitrate == 0 {
		// Remember what the link could not carry: probing back up slows
		// down near it (adapt).
		if h.ceilBitrate > 0 && now.Sub(h.ceilAt) < ceilLife && h.bitrate <= h.ceilBitrate*115/100 {
			h.ceilFails++ // a probe past the remembered ceiling failed again
		} else {
			h.ceilFails = 0
		}
		h.ceilBitrate, h.ceilAt = h.bitrate, now
		h.bitrate = max(minAutoBitrate, h.targetBitrate()/4, int64(float64(h.bitrate)*backoffFactor))
	}
	recent := h.congestion[:0]
	for _, t := range h.congestion {
		if now.Sub(t) < congestionSpan {
			recent = append(recent, t)
		}
	}
	h.congestion = append(recent, now)
	if len(h.congestion) >= 2 && h.fps > minAdaptFPS {
		h.fps = max(minAdaptFPS, h.fps*2/3)
		h.congestion = h.congestion[:0]
		if h.opts.Bitrate == 0 {
			h.bitrate = min(h.bitrate, h.targetBitrate())
		}
		h.scheduleRestart(now)
	}
}

// adapt runs periodically: it asks for the key frame a drop is waiting on
// once the backlog has drained, and after a quiet period probes the frame
// rate and automatic bitrate back up towards what was requested.
func (h *hostSession) adapt(now time.Time) {
	if h.waitKey && int(h.seq-h.acked) <= h.windowFrames()/2 {
		h.requestKeyframe(now)
	}
	// Acks coming back ever later: a queue is building. Lower the rate
	// before the window fills and frames have to be dropped.
	if !h.waitKey && h.acks.sustainedQueue(now, h.framePeriod()) && now.Sub(h.lastDrop) > 2*time.Second {
		h.acks.highFrom = time.Time{}
		before, fps := h.bitrate, h.fps
		h.onCongestion(now, true)
		if h.bitrate != before && h.fps == fps {
			h.scheduleRestart(now)
		}
	}
	if !h.restartAt.IsZero() || now.Sub(h.lastDrop) < recoverAfter || now.Sub(h.lastRestart) < recoverAfter {
		return
	}
	changed := false
	if h.fps < h.opts.FPS {
		up := min(h.opts.FPS, h.fps*3/2)
		if h.fpsCeil > 0 && now.Sub(h.fpsCeilAt) < fitCeilingLife {
			up = min(up, h.fpsCeil)
		}
		if up > h.fps {
			h.fps = up
			changed = true
		}
		if h.fps >= h.opts.FPS {
			h.limitWarn = ""
		}
	}
	if target := h.targetBitrate(); h.opts.Bitrate == 0 && h.bitrate < target {
		if next, ok := h.probeBitrate(now, target); ok {
			h.bitrate = next
			changed = true
		}
	}
	if changed {
		h.scheduleRestart(now)
	}
}

// probeBitrate is the next step of automatic bitrate back up towards
// target after congestion, and whether to take it now. Every step is an
// encoder restart and a key frame, and on a link that really is narrower
// than the target each probe past what it carries ends in congestion
// again: stepping by 1.25 every 15 s regardless gave a constrained link a
// hitch every 15-30 s indefinitely. So the rate climbs by 1.25 up to just
// below the rate that last caused congestion (ceilBitrate, remembered for
// ceilLife), and probes past it only after ceilHold without congestion
// (doubled for every probe in a row that failed, up to eight times) and by
// 1.1 at a time.
func (h *hostSession) probeBitrate(now time.Time, target int64) (int64, bool) {
	next := min(target, int64(float64(h.bitrate)*recoverFactor))
	if h.ceilBitrate == 0 || now.Sub(h.ceilAt) > ceilLife {
		return next, true
	}
	near := h.ceilBitrate * 9 / 10
	if h.bitrate < near {
		return min(next, near), true
	}
	if now.Sub(h.lastDrop) < ceilHold<<min(h.ceilFails, 3) {
		return 0, false
	}
	return min(target, int64(float64(h.bitrate)*1.1)), true
}

// fitRate runs periodically: once a second it looks at how many frames the
// encoder delivered and how late, and lowers the frame rate to what the
// pipeline sustains when it falls behind its source (see fitAfter).
func (h *hostSession) fitRate(now time.Time) {
	dt := now.Sub(h.encWinStart)
	if dt < time.Second {
		return
	}
	rate := float64(h.encWinFrames) / dt.Seconds()
	late := 0.0
	if h.encWinPipeN > 0 {
		late = h.encWinPipe / float64(h.encWinPipeN)
	}
	h.encWinStart, h.encWinFrames, h.encWinPipe, h.encWinPipeN = now, 0, 0, 0
	period := 1000 / float64(h.fps)
	behind := h.restartAt.IsZero() && now.Sub(h.lastRestart) >= fitAfter &&
		rate < fitSlowRatio*float64(h.fps) && late > fitLate*period+fitLateMS
	if !behind {
		h.slowSince = time.Time{}
		return
	}
	if h.slowSince.IsZero() {
		h.slowSince = now
		return
	}
	if now.Sub(h.slowSince) < fitHold {
		return
	}
	h.slowSince = time.Time{}
	fps := max(minAdaptFPS, int(rate*0.9))
	if fps >= h.fps {
		return
	}
	slog.Debug("desktop: pipeline falls behind its source; lowering the frame rate", "delivered_fps", rate, "late_ms", late, "fps", fps)
	h.fps, h.fpsCeil, h.fpsCeilAt = fps, fps, now
	h.limitWarn = fmt.Sprintf("the host's capture and encoder keep up with about %.0f fps at %s, so it streams %d fps: more would only queue frames and add latency",
		rate, h.sizeText(), fps)
	if h.opts.Bitrate == 0 {
		h.bitrate = min(h.bitrate, h.targetBitrate())
	}
	h.scheduleRestart(now)
}

// sizeText is the output size, for messages.
func (h *hostSession) sizeText() string {
	k := h.scaleFactor()
	return fmt.Sprintf("%dx%d", int(float64(h.spec.width)*k)&^1, int(float64(h.spec.height)*k)&^1)
}

// requestKeyframe gets a key frame out soon: from the encoder's own schedule
// if one is due anyway, otherwise by starting a replacement encoder (ffmpeg
// cannot be told to emit one on demand; the replacement starts with one and
// the old encoder streams until it does, see startEncoder). Restarts for
// key frames are spaced out (keyRestartGap): each one costs an encoder
// start and a large key frame, which on an overloaded machine would feed
// the very backlog that caused the request. A second request soon after is
// served once the gap has passed, unless a key frame comes first; it is
// never dropped, which left the viewer frozen until the scheduled key
// frame. Restarting also applies any pending bitrate change.
func (h *hostSession) requestKeyframe(now time.Time) {
	gop := time.Duration(h.gopFrames()) * time.Second / time.Duration(h.fps)
	if h.helloGen != h.gen || !h.restartAt.IsZero() || h.next != nil {
		return // a key frame is already on its way
	}
	sameRate := h.proc != nil && h.proc.bitrate == h.bitrate
	if sameRate && !h.lastKey.IsZero() && now.Sub(h.lastKey) > gop-keyframeSlack {
		return
	}
	at := now
	if !h.lastKeyRestart.IsZero() {
		at = later(at, h.lastKeyRestart.Add(keyRestartGap))
	}
	h.lastKeyRestart = at
	h.scheduleRestartAt(now, at)
	h.keyOnly = sameRate
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (h *hostSession) scheduleRestart(now time.Time) { h.scheduleRestartAt(now, now) }

// scheduleRestartAt asks for a restart at at or as soon after as restarts
// may be made (minRestartGap).
func (h *hostSession) scheduleRestartAt(now, at time.Time) {
	at = later(later(at, now), h.lastRestart.Add(minRestartGap))
	h.keyOnly = false
	if h.restartAt.IsZero() || at.Before(h.restartAt) {
		h.restartAt = at
	}
}

// onEncoderExit decides whether an encoder that died can be restarted.
func (h *hostSession) onEncoderExit(e encExit) error {
	h.proc = nil
	if h.next != nil {
		// Its replacement is starting already and takes over when it
		// delivers (or fails, and is started again: onNextExit).
		slog.Debug("desktop: encoder exited while its replacement starts", "err", e.err)
		return nil
	}
	now := time.Now()
	secure := secureDesktop()
	if e.frames == 0 && (secure != "" || h.delivered && h.pauseTries < maxBlindRetries) {
		// The screen cannot be captured right now, though it could be:
		// Windows shows the secure desktop (a UAC prompt, the lock or
		// sign-in screen, Ctrl+Alt+Del), where Desktop Duplication and
		// GDI see nothing until it closes. That is no reason to end the
		// session, nor to give up on the GPU chain.
		return h.pause(now, secure, e.err)
	}
	if e.gpu && e.frames == 0 {
		// The GPU chain passed its test run but not a session: fall back
		// to the default chain for this session. Later sessions are only
		// kept off it once the default chain has worked (gpuSuspect):
		// a capture failing on both says nothing against the GPU.
		slog.Debug("desktop: encoder failed on the GPU chain; using the default one", "err", e.err)
		h.noGPU, h.gpuSuspect = true, true
		h.scheduleRestart(now)
		return nil
	}
	if e.frames == 0 && !e.shared {
		// Nothing came out. A GPU encoder that initialises (the
		// probe runs at 320x240) may still refuse the real size (H.264
		// hardware encoders stop at about 4096 pixels): try the next
		// encoder before giving up.
		if h.fallbackEncoder(e.err) {
			h.scheduleRestart(now)
			return nil
		}
	}
	// An encoder that never produced a frame will not on a second try
	// either, unless what failed was the shared capture it read: that one
	// reports its own failures when the next encoder attaches.
	if e.frames == 0 && (!e.shared || len(h.failures) > 0) {
		return fmt.Errorf("screen capture/encoding failed: %v", e.err)
	}
	recent := h.failures[:0]
	gpuFails := 0
	for _, f := range h.failures {
		if now.Sub(f.at) < 30*time.Second {
			recent = append(recent, f)
			if f.gpu {
				gpuFails++
			}
		}
	}
	h.failures = append(recent, encFailure{at: now, gpu: e.gpu})
	if e.gpu && gpuFails >= 1 && !h.noGPU {
		// The GPU chain works for a while and then fails, again: use the
		// default chain for the rest of the session.
		slog.Debug("desktop: the GPU chain keeps failing; using the default one", "err", e.err)
		h.noGPU = true
	}
	if len(h.failures) > 3 {
		return fmt.Errorf("encoder keeps failing: %v", e.err)
	}
	slog.Debug("desktop: encoder exited, restarting", "err", e.err)
	h.scheduleRestart(now)
	return nil
}

// maxBlindRetries is how many encoders in a row may deliver nothing, after
// the session has shown the screen, before that counts as a failure when
// the platform cannot say why (secureDesktop is "").
const maxBlindRetries = 5

// pauseLimit is how long a session waits for the screen to come back.
const pauseLimit = 30 * time.Minute

// pause keeps the session while the screen cannot be captured, trying again
// with a growing interval, and tells the viewer why (HostStats.Paused).
func (h *hostSession) pause(now time.Time, reason string, err error) error {
	if h.pausedSince.IsZero() {
		h.pausedSince = now
		slog.Debug("desktop: the screen cannot be captured for now; waiting for it", "reason", reason, "err", err)
	} else if now.Sub(h.pausedSince) > pauseLimit {
		return fmt.Errorf("the screen could not be captured for %v: %v", pauseLimit, err)
	}
	if reason == "" {
		reason = "the host cannot capture its screen right now"
		if err != nil {
			reason += " (" + err.Error() + ")"
		}
	}
	h.paused = reason
	h.pauseTries++
	h.scheduleRestartAt(now, now.Add(min(2*time.Second, 250*time.Millisecond<<min(h.pauseTries, 4))))
	h.sendStats(now)
	return nil
}

// fallbackEncoder moves the session to the next encoder after the current
// one failed to produce anything, if there is one to try.
func (h *hostSession) fallbackEncoder(why error) bool {
	if h.opts.Encoder != "" || !h.enc.hw {
		return false
	}
	h.excluded = append(h.excluded, h.enc.name)
	next, err := selectEncoder(h.ctx, h.ffmpeg, encRequest{codec: h.opts.Codec, decode: h.opts.Decode, exclude: h.excluded})
	if err != nil {
		return false
	}
	slog.Debug("desktop: encoder produced nothing; trying the next one", "failed", h.enc.name, "next", next.name, "err", why)
	failed := h.enc.name
	h.enc = next
	h.encWarn = fmt.Sprintf("%s could not encode the screen at %s, so the host uses %s", failed, h.sizeText(), next.name)
	h.noGPU = false
	if h.opts.Bitrate == 0 {
		h.bitrate = h.targetBitrate()
	}
	return true
}

func (h *hostSession) onControl(c Control) {
	now := time.Now()
	switch c.Type {
	case ctlAck:
		// Acks for frames not sent yet are ignored; old ones are no-ops.
		if c.Seq-h.acked <= h.seq-h.acked {
			h.acked = c.Seq
			h.acks.onAck(c.Seq, now)
		}
	case ctlKeyframe:
		h.requestKeyframe(now)
	case ctlBehind:
		// Deltas would only be skipped on the other side: stop sending
		// them until a key frame, and slow down.
		if !h.waitKey {
			h.onCongestion(now, false)
		}
		h.waitKey = true
	case ctlConfig:
		next := h.opts
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
			h.reply(c, err)
			return
		}
		if next.Display != h.opts.Display {
			// Another monitor: capture it, and aim input at it.
			spec, err := discoverCapture(next.Display, next.FPS)
			if err != nil {
				h.reply(c, err)
				return
			}
			h.setSpec(spec)
		}
		h.reply(c, nil)
		h.opts = next
		h.fps = next.FPS
		h.fpsCeil, h.limitWarn = 0, ""
		h.bitrate = h.targetBitrate()
		h.scheduleRestart(now)
	}
}

func (h *hostSession) sendStats(now time.Time) {
	dt := now.Sub(h.lastStatsAt).Seconds()
	st := HostStats{
		FPS:        float64(h.sentFrames) / dt,
		Bitrate:    h.bitrate,
		SendRate:   int64(float64(h.sentBytes*8) / dt),
		Dropped:    h.dropped,
		InFlight:   int(h.seq - h.acked),
		PipelineMS: h.pipelineEW,
		Restarts:   h.restarts,
		Paused:     h.paused,
		AckMS:      float64(h.acks.srtt.Microseconds()) / 1000,
		WindowMS:   float64(h.acks.window(now).Milliseconds()),
	}
	h.sentFrames, h.sentBytes, h.lastStatsAt = 0, 0, now
	h.enqueueJSON(frameStats, st, true)
}

func (h *hostSession) enqueueJSON(typ byte, v any, droppable bool) bool {
	body, err := json.Marshal(v)
	if err != nil {
		return false
	}
	msg := make([]byte, 5+len(body))
	putFrameHead(msg, typ, len(body))
	copy(msg[5:], body)
	return h.enqueue(msg, droppable)
}

// enqueue hands a ready wire frame to the writer. The out channel is sized
// for a full window of video plus control traffic, so this only fails when
// the writer is wedged, in which case dropping is the right thing to do.
func (h *hostSession) enqueue(msg []byte, droppable bool) bool {
	select {
	case h.out <- msg:
		return true
	default:
	}
	if droppable {
		return false
	}
	select {
	case h.out <- msg:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	case <-h.ctx.Done():
		return false
	}
}

func (h *hostSession) writer() {
	defer close(h.wdone)
	for {
		select {
		case <-h.ctx.Done():
			return
		case msg := <-h.out:
			if _, err := h.s.Write(msg); err != nil {
				h.werr = err
				return
			}
		}
	}
}

// fatal reports err to the client and ends the session.
func (h *hostSession) fatal(err error) {
	h.stopEncoder()
	body, _ := json.Marshal(errorMsg{Error: err.Error()})
	msg := make([]byte, 5+len(body))
	putFrameHead(msg, frameError, len(body))
	copy(msg[5:], body)
	if h.enqueue(msg, false) {
		// Give the writer a moment to flush what is queued before the
		// error, then the stream is closed by the dispatcher.
		deadline := time.After(2 * time.Second)
		for len(h.out) > 0 {
			select {
			case <-deadline:
				return
			case <-h.wdone:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readControl reads client messages. Input is injected right here, in
// arrival order and without waiting on the video loop; everything else goes
// to the run loop.
func (h *hostSession) readControl() {
	defer close(h.ctl)
	for {
		var c Control
		if err := wire.ReadJSON(h.s, &c); err != nil {
			return
		}
		switch {
		case c.Type == ctlPing:
			h.enqueueJSON(framePong, pong{T: c.T, Host: time.Now().UnixMicro()}, true)
		case isInput(c.Type):
			h.injMu.Lock()
			if h.inj != nil {
				if err := h.inj.inject(c.input()); err != nil {
					slog.Debug("desktop: input", "err", err)
				}
			}
			h.injMu.Unlock()
		case c.Type == ctlType || c.Type == ctlLock || c.Type == ctlBlockInput || c.Type == ctlBlackout:
			h.reply(c, h.hostControl(c))
		default:
			select {
			case h.ctl <- c:
			case <-h.ctx.Done():
				return
			}
		}
	}
}

// parseSequenceHeaderOBU parses a complete sequence header OBU (with its
// OBU header).
func parseSequenceHeaderOBU(b []byte) (SequenceInfo, error) {
	obus, err := splitOBUs(b)
	if err != nil {
		return SequenceInfo{}, err
	}
	if len(obus) != 1 || obus[0].typ != obuSequenceHeader {
		return SequenceInfo{}, errBadOBU
	}
	return parseSequenceHeader(obus[0].payload)
}

// tailBuffer keeps the last max bytes written to it (ffmpeg's stderr).
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

// errorText is the last few meaningful lines, skipping encoder chatter
// (SVT-AV1 prints its banner at info level regardless of -loglevel).
func (t *tailBuffer) errorText() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var keep []string
	for _, l := range strings.Split(string(t.b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Svt[info]") || strings.HasPrefix(l, "Svt[warn]") {
			continue
		}
		keep = append(keep, l)
	}
	if len(keep) > 3 {
		keep = keep[len(keep)-3:]
	}
	return strings.Join(keep, "; ")
}

func joinWarnings(ws ...string) string {
	var out []string
	for _, w := range ws {
		if w != "" {
			out = append(out, w)
		}
	}
	return strings.Join(out, "; ")
}

// reply answers a control that asked for one.
func (h *hostSession) reply(c Control, err error) {
	if err != nil {
		slog.Debug("desktop: control failed", "type", c.Type, "err", err)
	}
	if c.ID == 0 {
		return
	}
	r := Reply{ID: c.ID, Type: c.Type}
	if err != nil {
		r.Error = err.Error()
	}
	h.enqueueJSON(frameReply, r, false)
}

// hostControl runs one of the controls that act on the host beyond plain
// pointer and key events. All of them need the session to be allowed input:
// a view-only client, or a host whose owner switched input off, gets none.
func (h *hostSession) hostControl(c Control) error {
	h.injMu.Lock()
	reason := unsupportedControls(h.inErr)[c.Type]
	h.injMu.Unlock()
	if reason != "" {
		return errors.New(reason)
	}
	switch c.Type {
	case ctlType:
		if len(c.Text) > maxTypeText || !utf8.ValidString(c.Text) {
			return fmt.Errorf("text to type must be valid UTF-8 of at most %d bytes", maxTypeText)
		}
		h.injMu.Lock()
		defer h.injMu.Unlock()
		if h.inj == nil {
			if h.inErr != "" {
				return errors.New("input is off for this session: " + h.inErr)
			}
			return errors.New("the session has ended")
		}
		// Typing a long text takes a while; switching input off stops it
		// between characters.
		err := h.inj.typeText(c.Text, h.inputOff.Load)
		if err == nil && h.inputOff.Load() {
			err = errors.New("typing stopped: " + hostInputOff)
		}
		return err
	case ctlLock:
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		defer cancel()
		h.injMu.Lock()
		spec := h.ctlSpec
		h.injMu.Unlock()
		return lockScreen(ctx, spec)
	}
	return fmt.Errorf("unknown control %q", c.Type)
}

// unsupportedControls lists the controls this host cannot offer to a
// session, with the reason: those the platform lacks (platformUnsupported)
// and, when the session has no input (inputErr says why), all the others.
func unsupportedControls(inputErr string) map[string]string {
	out := map[string]string{}
	for k, v := range platformUnsupported {
		out[k] = v
	}
	if adjustControls != nil {
		adjustControls(out)
	}
	if inputErr != "" {
		for _, k := range []string{ControlType, ControlLock, ControlCtrlAltDel, ControlBlockInput, ControlBlackout} {
			if out[k] == "" {
				out[k] = "input is off for this session: " + inputErr
			}
		}
	}
	return out
}

// encFailure is one encoder exit counted against the session.
type encFailure struct {
	at  time.Time
	gpu bool
}
