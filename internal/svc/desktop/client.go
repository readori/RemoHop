package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Frame is one AV1 temporal unit, ready for a decoder (WebCodecs
// EncodedVideoChunk, ffmpeg, dav1d, ...).
type Frame struct {
	Seq uint32
	Key bool // starts with a sequence header and a key frame
	// PTS is the presentation time since the session started.
	PTS time.Duration
	// Capture is when the host captured the frame, on the host's clock
	// (zero if the capture device has no wall-clock timestamps). Use
	// Session.HostToLocal to compare it with local time.
	Capture time.Time
	// Sent is when the host sent the frame, on the host's clock.
	Sent time.Time
	// Received is when it arrived here, on the local clock.
	Received time.Time
	Data     []byte
}

// Event is one item from Session.Next: exactly one field is set.
type Event struct {
	// Hello announces new stream parameters (after an encoder restart);
	// the frames that follow use them, starting with a key frame.
	Hello *Hello
	Frame *Frame
	Stats *HostStats
}

// Session is an open remote-desktop stream.
type Session struct {
	s    peer.Stream
	opts Options

	wmu sync.Mutex // serialises control writes

	events chan Event
	done   chan struct{}

	mu          sync.Mutex
	hello       Hello
	err         error
	offset      time.Duration // host clock minus local clock
	rtt         time.Duration
	rttSamples  []clockSample
	dropping    bool
	droppedHigh uint32 // highest sequence number dropped locally
	localDrops  uint64
	lastKeyReq  time.Time
	viewOnly    bool
	// hostNoInput: the latest Hello says the host takes no input (the
	// host's owner can withdraw it, and give it back, mid-session).
	hostNoInput bool
	nextID      uint32
	waiting     map[uint32]chan Reply

	closeOnce sync.Once
}

type clockSample struct{ rtt, offset time.Duration }

// eventQueue is how many events may wait for the application before the
// session starts dropping video locally.
const eventQueue = 32

// Open starts a remote-desktop stream on conn and waits for the host's first
// Hello (so capture and encoder errors surface here).
func Open(ctx context.Context, conn *peer.Conn, opts Options) (*Session, error) {
	if err := opts.normalize(); err != nil {
		return nil, err
	}
	s, err := conn.Open(ctx, ServiceName, opts)
	if err != nil {
		return nil, err
	}
	sess := &Session{s: s, opts: opts, events: make(chan Event, eventQueue), done: make(chan struct{}),
		viewOnly: opts.ViewOnly, waiting: map[uint32]chan Reply{}}
	first := make(chan error, 1)
	go sess.readLoop(first)
	select {
	case err := <-first:
		if err != nil {
			s.Reset()
			return nil, err
		}
	case <-ctx.Done():
		s.Reset()
		return nil, ctx.Err()
	}
	go sess.pingLoop()
	return sess, nil
}

// Hello is the most recent stream description.
func (s *Session) Hello() Hello {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hello
}

// Next returns the next event. Video is dropped locally (up to the next key
// frame) rather than queued when the caller falls behind, so a slow caller
// sees gaps, never growing latency. Unless Options.ManualAck is set, each
// returned frame is acknowledged to the host.
func (s *Session) Next(ctx context.Context) (Event, error) {
	select {
	case ev := <-s.events:
		if ev.Frame != nil && !s.opts.ManualAck {
			s.Ack(ev.Frame.Seq)
		}
		return ev, nil
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case <-s.done:
		select {
		case ev := <-s.events:
			return ev, nil
		default:
		}
		return Event{}, s.Err()
	}
}

// Ack tells the host the frame seq (and everything before it) has been
// consumed. Only needed with Options.ManualAck.
func (s *Session) Ack(seq uint32) error {
	s.mu.Lock()
	// Once the queue has drained, frames dropped locally are behind the
	// consumer too: acknowledge them, or the host would keep waiting.
	if len(s.events) == 0 && s.droppedHigh-seq < 1<<31 {
		seq = s.droppedHigh
	}
	s.mu.Unlock()
	return s.send(Control{Type: ctlAck, Seq: seq})
}

// RequestKeyframe asks for a key frame soon (decoder error, joining late).
func (s *Session) RequestKeyframe() error { return s.send(Control{Type: ctlKeyframe}) }

// reportBehind tells the host the consumer is skipping to the next key
// frame, so it sends one and slows down.
func (s *Session) reportBehind() error { return s.send(Control{Type: ctlBehind}) }

// Configure changes encoding parameters; zero fields keep their value, a
// negative Bitrate returns to automatic bitrate and a negative MaxHeight
// removes the height limit. The host restarts its encoder to apply it.
func (s *Session) Configure(c Config) error {
	return s.send(Control{Type: ctlConfig, FPS: c.FPS, Bitrate: c.Bitrate, MaxHeight: c.MaxHeight, Scale: c.Scale, Display: c.Display})
}

// SetDisplay switches the stream to another of the host's monitors (0: the
// host's default, DisplayAll: every monitor in one picture) and waits for the host to accept it; the new picture
// follows with a new Hello.
func (s *Session) SetDisplay(ctx context.Context, display int) error {
	return s.call(ctx, Control{Type: ctlConfig, Display: &display})
}

// TypeText types text on the host as if on its keyboard. Any Unicode text
// works, whatever the host's keyboard layout.
func (s *Session) TypeText(ctx context.Context, text string) error {
	if len(text) > maxTypeText {
		return fmt.Errorf("desktop: text to type is limited to %d bytes", maxTypeText)
	}
	return s.hostControl(ctx, Control{Type: ctlType, Text: text})
}

// Lock locks the host's screen.
func (s *Session) Lock(ctx context.Context) error {
	return s.hostControl(ctx, Control{Type: ctlLock})
}

// BlockInput disables (or re-enables) the host's physical keyboard and
// mouse while the session controls it. Hosts that cannot do it return an
// error saying so; see Hello.Unsupported.
func (s *Session) BlockInput(ctx context.Context, on bool) error {
	return s.hostControl(ctx, Control{Type: ctlBlockInput, On: on})
}

// Blackout hides (or shows again) the host's physical screen. Hosts that
// cannot do it return an error saying so; see Hello.Unsupported.
func (s *Session) Blackout(ctx context.Context, on bool) error {
	return s.hostControl(ctx, Control{Type: ctlBlackout, On: on})
}

func (s *Session) hostControl(ctx context.Context, c Control) error {
	if s.ViewOnly() {
		return ErrViewOnly
	}
	return s.call(ctx, c)
}

// call sends a control and waits for the host's Reply.
func (s *Session) call(ctx context.Context, c Control) error {
	ch := make(chan Reply, 1)
	s.mu.Lock()
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	c.ID = s.nextID
	s.waiting[c.ID] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.waiting, c.ID)
		s.mu.Unlock()
	}()
	if err := s.send(c); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return fmt.Errorf("desktop: host: %s", sanitize(r.Error, 600))
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.Err()
	}
}

// SetViewOnly switches input off or back on at run time. It cannot switch
// input on for a session that was opened view-only or while the host
// takes no input: see Hello.Input.
func (s *Session) SetViewOnly(on bool) {
	s.mu.Lock()
	s.viewOnly = on || s.opts.ViewOnly
	s.mu.Unlock()
	if on {
		s.send(Control{Type: InputReleaseAll})
	}
}

// ViewOnly reports whether the session currently refuses to send input:
// it was asked to, or the host said (in its latest Hello) that it takes no
// input, for instance because its owner switched keyboard and mouse off.
func (s *Session) ViewOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewOnly || s.hostNoInput
}

// ErrViewOnly is returned by SendInput on a session without input.
var ErrViewOnly = errors.New("desktop: input is disabled on this session")

// SendInput injects a pointer or keyboard event on the host.
func (s *Session) SendInput(ev InputEvent) error {
	if s.ViewOnly() {
		return ErrViewOnly
	}
	if !isInput(ev.Kind) {
		return fmt.Errorf("desktop: unknown input kind %q", ev.Kind)
	}
	return s.send(ev.control())
}

// HostToLocal converts a host-clock time to the local clock using the
// session's running clock-offset estimate.
func (s *Session) HostToLocal(t time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t.Add(-s.offset)
}

// RTT is the best recent round-trip time to the host.
func (s *Session) RTT() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rtt
}

// LocalDrops is how many frames were dropped because the caller fell
// behind.
func (s *Session) LocalDrops() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.localDrops
}

// Done is closed when the stream ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the stream ended (nil while it runs).
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the stream; the host stops capturing.
func (s *Session) Close() error {
	s.finish(errors.New("desktop: session closed"))
	return s.s.Close()
}

func (s *Session) finish(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *Session) send(c Control) error {
	select {
	case <-s.done:
		return s.Err()
	default:
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return wire.WriteJSON(s.s, c)
}

func (s *Session) pingLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		if s.send(Control{Type: ctlPing, T: time.Now().UnixMicro()}) != nil {
			return
		}
		wait := 200 * time.Millisecond // a few quick samples first
		if i >= 3 {
			wait = 2 * time.Second
		}
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
	}
}

func (s *Session) onPong(p pong) {
	now := time.Now()
	t0 := time.UnixMicro(p.T)
	rtt := now.Sub(t0)
	if rtt < 0 || rtt > time.Minute {
		return
	}
	off := time.UnixMicro(p.Host).Sub(t0.Add(rtt / 2))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rttSamples = append(s.rttSamples, clockSample{rtt, off})
	if len(s.rttSamples) > 8 {
		s.rttSamples = s.rttSamples[1:]
	}
	// The sample with the smallest RTT has the least queueing asymmetry.
	best := s.rttSamples[0]
	for _, c := range s.rttSamples[1:] {
		if c.rtt < best.rtt {
			best = c
		}
	}
	s.rtt, s.offset = best.rtt, best.offset
}

var codecRE = regexp.MustCompile(`^(av01\.[0-2]\.[0-3][0-9][MH]\.(08|10|12)|avc1\.[0-9a-f]{6})$`)

// maxCodecConfig bounds a codec configuration record (an avcC with its
// parameter sets, or an av1C with a sequence header, is far smaller).
const maxCodecConfig = 4 << 10

// validateHello checks a host-supplied Hello; the client does not trust the
// host either.
func validateHello(h *Hello) error {
	if h.Width <= 0 || h.Height <= 0 || h.Width > 16384 || h.Height > 16384 {
		return fmt.Errorf("desktop: host sent bad dimensions %dx%d", h.Width, h.Height)
	}
	if !codecRE.MatchString(h.Codec) {
		return fmt.Errorf("desktop: host sent bad codec %q", h.Codec)
	}
	if len(h.CodecConfig) > maxCodecConfig || (h.IsH264() && len(h.CodecConfig) == 0) {
		return errors.New("desktop: host sent a bad codec configuration")
	}
	if len(h.Displays) > maxDisplay || h.Display < DisplayAll || h.Display > maxDisplay {
		return errors.New("desktop: host sent a bad display list")
	}
	h.Encoder = sanitize(h.Encoder, 64)
	h.InputError = sanitize(h.InputError, 300)
	h.Warning = sanitize(h.Warning, 600)
	h.Unsupported = sanitizeReasons(h.Unsupported)
	return nil
}

// sanitizeReasons keeps the known control names of a host-supplied
// Unsupported map, with printable reasons.
func sanitizeReasons(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, k := range []string{ControlType, ControlLock, ControlBlockInput, ControlBlackout, ControlCtrlAltDel, ControlAllMonitors} {
		if v, ok := m[k]; ok {
			if v = sanitize(v, 300); v == "" {
				v = "not supported"
			}
			out[k] = v
		}
	}
	return out
}

func sanitize(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}

func (s *Session) readLoop(first chan<- error) {
	gotHello := false
	fail := func(err error) {
		if !gotHello {
			first <- err
		}
		s.finish(err)
	}
	for {
		typ, payload, err := wire.ReadFrame(s.s, nil)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			fail(fmt.Errorf("desktop: stream ended: %w", err))
			return
		}
		switch typ {
		case frameError:
			var m errorMsg
			json.Unmarshal(payload, &m)
			fail(fmt.Errorf("desktop: host: %s", sanitize(m.Error, 2000)))
			return
		case frameHello:
			var h Hello
			if err := json.Unmarshal(payload, &h); err != nil {
				fail(fmt.Errorf("desktop: bad hello: %w", err))
				return
			}
			if err := validateHello(&h); err != nil {
				fail(err)
				return
			}
			s.mu.Lock()
			s.hello = h
			s.hostNoInput = !h.Input
			s.mu.Unlock()
			if !gotHello {
				gotHello = true
				first <- nil
				continue
			}
			// Hellos must not be lost; make room by discarding queued
			// video, which the new stream parameters supersede anyway.
			s.pushHello(&h)
		case frameVideo:
			if !gotHello {
				continue
			}
			hdr, err := parseFrameHeader(payload)
			if err != nil {
				fail(err)
				return
			}
			s.onVideo(hdr, payload[frameHeaderLen:])
		case frameStats:
			var st HostStats
			if json.Unmarshal(payload, &st) == nil {
				select {
				case s.events <- Event{Stats: &st}:
				default:
				}
			}
		case frameReply:
			var r Reply
			if json.Unmarshal(payload, &r) == nil {
				s.mu.Lock()
				ch := s.waiting[r.ID]
				s.mu.Unlock()
				if ch != nil {
					select {
					case ch <- r:
					default:
					}
				}
			}
		case framePong:
			var p pong
			if json.Unmarshal(payload, &p) == nil {
				s.onPong(p)
			}
		}
	}
}

func (s *Session) pushHello(h *Hello) {
	for {
		select {
		case s.events <- Event{Hello: h}:
			return
		default:
		}
		select {
		case ev := <-s.events:
			if ev.Frame != nil {
				s.noteDrop(ev.Frame.Seq)
			}
		default:
		}
	}
}

func (s *Session) noteDrop(seq uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropping = true
	s.localDrops++
	if seq-s.droppedHigh < 1<<31 {
		s.droppedHigh = seq
	}
}

func (s *Session) onVideo(h frameHeader, data []byte) {
	f := &Frame{
		Seq: h.seq, Key: h.key, PTS: time.Duration(h.pts) * time.Microsecond,
		Sent: time.UnixMicro(h.sent), Received: time.Now(), Data: data,
	}
	if h.capture != 0 {
		f.Capture = time.UnixMicro(h.capture)
	}
	s.mu.Lock()
	dropping := s.dropping
	s.mu.Unlock()
	if dropping && !f.Key {
		s.noteDrop(f.Seq)
		return
	}
	select {
	case s.events <- Event{Frame: f}:
		if f.Key {
			s.mu.Lock()
			s.dropping = false
			s.mu.Unlock()
		}
	default:
		s.noteDrop(f.Seq)
		s.mu.Lock()
		ask := time.Since(s.lastKeyReq) > time.Second
		if ask {
			s.lastKeyReq = time.Now()
		}
		s.mu.Unlock()
		if ask {
			go s.reportBehind()
		}
	}
}

// Probe asks the host what a desktop session would offer (screen size,
// monitors, codecs, whether input is allowed) without starting one. A host
// that cannot capture or encode answers with an error that says why.
func Probe(ctx context.Context, conn *peer.Conn) (HostInfo, error) {
	s, err := conn.Open(ctx, ServiceName, Options{Probe: true})
	if err != nil {
		return HostInfo{}, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	typ, payload, err := wire.ReadFrame(s, nil)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return HostInfo{}, fmt.Errorf("desktop: probe: %w", err)
	}
	switch typ {
	case frameError:
		var m errorMsg
		json.Unmarshal(payload, &m)
		return HostInfo{}, fmt.Errorf("desktop: host: %s", sanitize(m.Error, 2000))
	case frameInfo:
		var info HostInfo
		if err := json.Unmarshal(payload, &info); err != nil {
			return HostInfo{}, fmt.Errorf("desktop: bad host info: %w", err)
		}
		if len(info.Displays) > maxDisplay {
			info.Displays = info.Displays[:maxDisplay]
		}
		enc := map[string]string{}
		for _, c := range []string{codecAV1, codecH264} {
			if v := sanitize(info.Encoders[c], 64); v != "" {
				enc[c] = v
			}
		}
		info.Encoders = enc
		info.OS = sanitize(info.OS, 32)
		info.InputError = sanitize(info.InputError, 300)
		info.Warning = sanitize(info.Warning, 600)
		info.Unsupported = sanitizeReasons(info.Unsupported)
		return info, nil
	}
	return HostInfo{}, fmt.Errorf("desktop: unexpected answer %q to a probe", typ)
}
