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
	// window is how much video, in time, may be unacknowledged before the
	// host starts dropping. It bounds end-to-end queueing latency.
	window = 300 * time.Millisecond
	// minWindowFrames keeps very low frame rates workable on long RTTs.
	minWindowFrames = 4
	// bitrate adaptation for automatic bitrate
	backoffFactor  = 0.7
	recoverFactor  = 1.25
	recoverAfter   = 15 * time.Second
	minAutoBitrate = 500_000
	// Two congestion events within congestionSpan lower the frame rate,
	// not below minAdaptFPS.
	congestionSpan = 10 * time.Second
	minAdaptFPS    = 10
	// keyRestartGap spaces out encoder restarts made only to get a key
	// frame.
	keyRestartGap = 4 * time.Second
	// startupGrace is how long after the session starts congestion does
	// not lower the bitrate or frame rate.
	startupGrace = 3 * time.Second
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
	if !hostd.Allowed(c, perm.Input) {
		opts.ViewOnly = true
	}
	if opts.Probe {
		return serveProbe(ctx, s, opts)
	}
	h, err := newHostSession(ctx, s, opts)
	if err != nil {
		writeFatal(s, err)
		return err
	}
	return h.run()
}

func writeFatal(s peer.Stream, err error) {
	s.SetWriteDeadline(time.Now().Add(2 * time.Second))
	body, _ := json.Marshal(errorMsg{Error: err.Error()})
	wire.WriteFrame(s, frameError, body)
}

// inputRefusal says why a session may not send input ("" if it may): the
// client asked for view-only, or the host's owner switched input off.
func inputRefusal(opts Options) string {
	switch {
	case opts.ViewOnly:
		return "view-only session"
	case inject.ViewOnly():
		return "the host only allows viewing (" + hostViewOnlyEnv + ")"
	}
	return ""
}

// serveProbe answers a probe: what the host would offer, without capturing.
func serveProbe(ctx context.Context, s peer.Stream, opts Options) error {
	info := HostInfo{OS: runtime.GOOS, Encoders: map[string]string{}}
	ffmpeg, err := ffmpegPath()
	if err == nil {
		var spec captureSpec
		if spec, err = discoverCapture(0, opts.FPS); err == nil {
			info.ScreenWidth, info.ScreenHeight = spec.width, spec.height
			info.Displays, info.Warning = spec.displays, spec.warning
			info.InputError = inputRefusal(opts)
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
}

// encProc is one running ffmpeg.
type encProc struct {
	gen     int
	cmd     *exec.Cmd
	stderr  *tailBuffer
	bitrate int64
	h264    bool

	// release gives back what the process was started with (its place
	// at a shared capture).
	release func()
	shared  bool

	mu     sync.Mutex
	reaped bool
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
}

type hostSession struct {
	ctx    context.Context
	cancel context.CancelFunc
	s      peer.Stream
	opts   Options

	ffmpeg string
	enc    *encoder
	spec   captureSpec
	injMu  sync.Mutex // serialises injection with teardown
	inj    injector
	inErr  string
	start  time.Time

	frames chan encFrame
	exits  chan encExit
	ctl    chan Control
	out    chan []byte
	wdone  chan struct{}
	werr   error

	// Everything below is owned by run's goroutine.
	proc        *encProc
	gen         int
	helloGen    int      // generation whose hello was sent
	seqHdr      []byte   // AV1: the sequence header OBU
	reduced     bool     // AV1: reduced_still_picture_header
	avc         *AVCInfo // H.264: from the avcC record
	avcC        []byte
	seq         uint32 // last sequence number assigned
	acked       uint32
	waitKey     bool
	lastKey     time.Time
	lastRestart time.Time
	restartAt   time.Time // pending deferred restart, zero if none
	lastDrop    time.Time
	bitrate     int64 // current target
	fps         int
	failures    []time.Time
	congestion  []time.Time
	// lastKeyRestart is when a restart was last made to get a key frame.
	lastKeyRestart time.Time

	// stats
	sentFrames  int
	sentBytes   int64
	dropped     uint64
	restarts    int
	pipelineEW  float64
	lastStatsAt time.Time
}

func newHostSession(ctx context.Context, s peer.Stream, opts Options) (*hostSession, error) {
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
		ffmpeg: ffmpeg, spec: spec, start: time.Now(),
		frames: make(chan encFrame, 4),
		exits:  make(chan encExit, 4),
		ctl:    make(chan Control, 256),
		wdone:  make(chan struct{}),
		fps:    opts.FPS,
	}
	h.enc, err = selectEncoder(ctx, ffmpeg, encRequest{forced: opts.Encoder, codec: opts.Codec, decode: opts.Decode})
	if err != nil {
		return nil, err
	}
	h.ctx, h.cancel = context.WithCancel(ctx)
	h.bitrate = h.targetBitrate()
	// Room for a full window of video at the highest frame rate a config
	// change can select, plus control traffic.
	h.out = make(chan []byte, int(maxFPS*window.Seconds())*2+32)
	if h.inErr = inputRefusal(opts); h.inErr == "" {
		inj, err := newInjector(spec)
		if err != nil {
			h.inErr = err.Error()
		} else {
			h.inj = inj
		}
	}
	return h, nil
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
	return autoBitrate(h.outputPixels(), h.fps)
}

func (h *hostSession) windowFrames() int {
	return max(minWindowFrames, int(float64(h.fps)*window.Seconds()+0.5))
}

func (h *hostSession) gopFrames() int { return 2 * h.fps }

// scaleFilter keeps the aspect ratio and even dimensions (4:2:0 needs
// them). Expressions are single-quoted so their commas are not taken as
// filter separators.
func (h *hostSession) scaleFilter() string {
	k := fmt.Sprintf("%g", h.opts.Scale)
	if h.opts.MaxHeight > 0 {
		k = fmt.Sprintf("min(%g,%d/ih)", h.opts.Scale, h.opts.MaxHeight)
	}
	return fmt.Sprintf("scale=w='trunc(iw*min(1,%s)/2)*2':h='trunc(ih*min(1,%s)/2)*2':flags=bilinear", k, k)
}

func (h *hostSession) ffmpegArgs() ([]string, captureSpec, error) {
	spec, err := discoverCapture(h.opts.Display, h.fps)
	if err != nil {
		return nil, spec, err
	}
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
	// -copyts with passthrough timing keeps the capture device's
	// timestamps, which for x11grab are wall-clock: the host then knows
	// when each frame was captured and the viewer can show true latency.
	args = append(args, "-an", "-vf", strings.Join(chain, ","),
		"-copyts", "-fps_mode", "passthrough", "-enc_time_base", "1:1000")
	args = append(args, h.enc.args(encParams{fps: h.fps, bitrate: h.bitrate, gop: h.gopFrames(), pixels: h.outputPixels()})...)
	args = append(args, "-flush_packets", "1")
	if h.enc.codec == codecH264 {
		args = append(args, "-flvflags", "no_duration_filesize", "-f", "flv", "pipe:1")
	} else {
		args = append(args, "-f", "ivf", "pipe:1")
	}
	return args, spec, nil
}

// startEncoder launches a new ffmpeg generation, replacing any running one.
func (h *hostSession) startEncoder() error {
	h.stopEncoder()
	args, spec, err := h.ffmpegArgs()
	if err != nil {
		return err
	}
	if spec.x != h.spec.x || spec.y != h.spec.y || spec.width != h.spec.width || spec.height != h.spec.height {
		// The screen changed under the session (another resolution; on a
		// Windows service, another user's session on the console): aim
		// input at what is captured now.
		h.spec = spec
		h.injMu.Lock()
		if h.inj != nil {
			h.inj.retarget(spec)
		}
		h.injMu.Unlock()
	}
	h.gen++
	cmd := exec.Command(h.ffmpeg, args...)
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
	p := &encProc{gen: h.gen, cmd: cmd, stderr: tail, bitrate: h.bitrate, h264: h.enc.codec == codecH264, release: release, shared: shared}
	h.proc = p
	h.lastRestart = time.Now()
	h.restartAt = time.Time{}
	slog.Debug("desktop: encoder started", "gen", p.gen, "encoder", h.enc.name, "fps", h.fps, "bitrate", h.bitrate)
	go h.readEncoder(p, stdout)
	return nil
}

func (h *hostSession) stopEncoder() {
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
	case h.exits <- encExit{gen: p.gen, err: err, frames: n, shared: p.shared}:
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
		if h.inj != nil {
			// readControl may still deliver an event until the stream
			// is closed; injMu orders it before or after this release.
			h.injMu.Lock()
			h.inj.close()
			h.inj = nil
			h.injMu.Unlock()
		}
	}()
	if err := h.startEncoder(); err != nil {
		h.fatal(err)
		return err
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	h.lastStatsAt = time.Now()
	for {
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case <-h.wdone:
			return h.werr
		case f := <-h.frames:
			if f.gen == h.gen {
				h.onFrame(f)
			}
		case e := <-h.exits:
			if e.gen != h.gen {
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
		case now := <-tick.C:
			if !h.restartAt.IsZero() && !now.Before(h.restartAt) {
				if err := h.startEncoder(); err != nil {
					h.fatal(err)
					return err
				}
				h.restarts++
			}
			h.adapt(now)
			if now.Sub(h.lastStatsAt) >= statsEvery {
				h.sendStats(now)
			}
		}
	}
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
	if key {
		h.lastKey = f.readAt
	}
	capture := int64(0)
	// Wall-clock capture timestamps (x11grab, or frames stamped on arrival
	// from a shared capture, which are rounded to the frame period and so
	// may lie a fraction of a frame ahead).
	if d := f.readAt.UnixMicro() - f.ptsUS; d >= -50_000 && d < 10e6 {
		d = max(d, 0)
		capture = f.readAt.UnixMicro() - d
		ms := float64(d) / 1000
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
		if !h.waitKey {
			h.onCongestion(true)
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
	hdr := frameHeader{key: key, seq: h.seq, pts: ts - h.start.UnixMicro(), capture: capture, sent: time.Now().UnixMicro()}
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
	h.sentFrames++
	h.sentBytes += int64(len(f.data))
}

func putFrameHead(b []byte, typ byte, n int) {
	b[0], b[1], b[2], b[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	b[4] = typ
}

func (h *hostSession) sendHello() bool {
	hello := Hello{
		FPS: h.fps, Bitrate: h.bitrate, Encoder: h.enc.name, Hardware: h.enc.hw,
		ScreenWidth: h.spec.width, ScreenHeight: h.spec.height,
		Input: h.inj != nil, InputError: h.inErr, Warning: joinWarnings(h.spec.warning, h.enc.warning),
		Display: h.opts.Display, Displays: h.spec.displays, AllDisplays: h.spec.all,
		Unsupported: unsupportedControls(h.inErr),
		Generation:  h.gen,
	}
	if h.enc.codec == codecH264 {
		hello.Width, hello.Height, hello.Codec, hello.CodecConfig = h.avc.Width, h.avc.Height, h.avc.Codec(), h.avcC
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
func (h *hostSession) onCongestion(link bool) {
	now := time.Now()
	h.lastDrop = now
	if now.Sub(h.start) < startupGrace {
		// A viewer that is still setting up its decoder (and decoding
		// its first, large key frame) is not a slow link: drop, but
		// do not lower the quality for the rest of the session.
		return
	}
	if link && h.opts.Bitrate == 0 {
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
	if !h.restartAt.IsZero() || now.Sub(h.lastDrop) < recoverAfter || now.Sub(h.lastRestart) < recoverAfter {
		return
	}
	changed := false
	if h.fps < h.opts.FPS {
		h.fps = min(h.opts.FPS, h.fps*3/2)
		changed = true
	}
	if target := h.targetBitrate(); h.opts.Bitrate == 0 && h.bitrate < target {
		h.bitrate = min(target, int64(float64(h.bitrate)*recoverFactor))
		changed = true
	}
	if changed {
		h.scheduleRestart(now)
	}
}

// requestKeyframe gets a key frame out soon: from the encoder's own schedule
// if one is due anyway, otherwise by restarting the encoder (ffmpeg cannot
// be told to emit one on demand). Restarts for key frames are spaced out
// (keyRestartGap): each one costs an encoder start and a large key frame,
// which on an overloaded machine would feed the very backlog that caused
// the request, so a second request soon after waits for the scheduled key
// frame (at most one GOP) instead. Restarting also applies any pending
// bitrate change.
func (h *hostSession) requestKeyframe(now time.Time) {
	gop := time.Duration(h.gopFrames()) * time.Second / time.Duration(h.fps)
	if h.helloGen != h.gen || !h.restartAt.IsZero() {
		return // a key frame is already on its way
	}
	sameRate := h.proc != nil && h.proc.bitrate == h.bitrate
	if sameRate && !h.lastKey.IsZero() && now.Sub(h.lastKey) > gop-keyframeSlack {
		return
	}
	if !h.lastKeyRestart.IsZero() && now.Sub(h.lastKeyRestart) < keyRestartGap {
		return
	}
	h.lastKeyRestart = now
	h.scheduleRestart(now)
}

func (h *hostSession) scheduleRestart(now time.Time) {
	at := h.lastRestart.Add(minRestartGap)
	if at.Before(now) {
		at = now
	}
	if h.restartAt.IsZero() || at.Before(h.restartAt) {
		h.restartAt = at
	}
}

// onEncoderExit decides whether an encoder that died can be restarted.
func (h *hostSession) onEncoderExit(e encExit) error {
	h.proc = nil
	// An encoder that never produced a frame will not on a second try
	// either, unless what failed was the shared capture it read: that one
	// reports its own failures when the next encoder attaches.
	if e.frames == 0 && (!e.shared || len(h.failures) > 0) {
		return fmt.Errorf("screen capture/encoding failed: %v", e.err)
	}
	now := time.Now()
	recent := h.failures[:0]
	for _, t := range h.failures {
		if now.Sub(t) < 30*time.Second {
			recent = append(recent, t)
		}
	}
	h.failures = append(recent, now)
	if len(h.failures) > 3 {
		return fmt.Errorf("encoder keeps failing: %v", e.err)
	}
	slog.Debug("desktop: encoder exited, restarting", "err", e.err)
	h.scheduleRestart(now)
	return nil
}

func (h *hostSession) onControl(c Control) {
	now := time.Now()
	switch c.Type {
	case ctlAck:
		// Acks for frames not sent yet are ignored; old ones are no-ops.
		if c.Seq-h.acked <= h.seq-h.acked {
			h.acked = c.Seq
		}
	case ctlKeyframe:
		h.requestKeyframe(now)
	case ctlBehind:
		// Deltas would only be skipped on the other side: stop sending
		// them until a key frame, and slow down.
		if !h.waitKey {
			h.onCongestion(false)
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
			h.spec = spec
			h.injMu.Lock()
			if h.inj != nil {
				h.inj.retarget(spec)
			}
			h.injMu.Unlock()
		}
		h.reply(c, nil)
		h.opts = next
		h.fps = next.FPS
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
	if reason := unsupportedControls(h.inErr)[c.Type]; reason != "" {
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
			return errors.New("the session has ended")
		}
		return h.inj.typeText(c.Text)
	case ctlLock:
		ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
		defer cancel()
		return lockScreen(ctx, h.spec)
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
