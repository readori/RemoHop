package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/svc/desktop/h264test"
	"github.com/readori/RemoHop/internal/wire"
)

func TestAnnexB(t *testing.T) {
	sps, pps := h264test.SPS(1080, 2400), h264test.PPS()
	// Mixed three- and four-byte start codes, an access unit delimiter and
	// trailing zero bytes.
	var b []byte
	b = append(b, 0, 0, 1, 0x09, 0xf0)
	b = append(b, 0, 0, 0, 1)
	b = append(b, sps...)
	b = append(b, 0, 0, 1)
	b = append(b, pps...)
	b = append(b, 0, 0, 0, 1)
	b = append(b, h264test.IDR(10)...)
	b = append(b, 0, 0)
	u, err := parseAnnexB(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(u.sps, sps) || !bytes.Equal(u.pps, pps) || !u.key {
		t.Fatalf("sps %x pps %x key %v", u.sps, u.pps, u.key)
	}
	if want := lengthPrefixed([][]byte{h264test.IDR(10)}); !bytes.Equal(u.sample, want) {
		t.Fatalf("sample %x, want %x", u.sample, want)
	}
	info, err := parseAVCC(buildAVCC(sps, pps))
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 1080 || info.Height != 2400 || info.NALLength != 4 || info.Codec() != "avc1.42c01f" {
		t.Fatalf("avcC: %+v %s", info, info.Codec())
	}
	if key, err := avcIsIDR(u.sample, 4); err != nil || !key {
		t.Fatalf("avcIsIDR: %v %v", key, err)
	}
	// Parameter sets alone (a codec-config buffer) carry no sample.
	if u, err := parseAnnexB(h264test.Config(640, 480)); err != nil || u.sample != nil || u.sps == nil || u.pps == nil {
		t.Fatalf("config buffer: %+v %v", u, err)
	}
	for _, size := range [][2]int{{320, 240}, {1366, 768}, {1080, 2400}, {2400, 1080}, {722, 1442}} {
		w, h, err := parseSPSSize(h264test.SPS(size[0], size[1]))
		if err != nil || w != size[0] || h != size[1] {
			t.Fatalf("SPS for %v parses as %dx%d (%v)", size, w, h, err)
		}
	}
}

// fakeSource is a ScreenSource that records what it is asked.
type fakeSource struct {
	mu       sync.Mutex
	starts   []screenParams
	stops    int
	keys     int
	bitrates []int64
	fail     error
	started  chan screenParams
}

func newFakeSource() *fakeSource { return &fakeSource{started: make(chan screenParams, 16)} }

func (f *fakeSource) StartScreen(maxSize, fps int, bitrate int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	p := screenParams{maxSize, fps, bitrate}
	f.starts = append(f.starts, p)
	select {
	case f.started <- p:
	default:
	}
	return nil
}

func (f *fakeSource) StopScreen() {
	f.mu.Lock()
	f.stops++
	f.mu.Unlock()
}

func (f *fakeSource) RequestKeyframe() {
	f.mu.Lock()
	f.keys++
	f.mu.Unlock()
}

func (f *fakeSource) SetBitrate(b int64) {
	f.mu.Lock()
	f.bitrates = append(f.bitrates, b)
	f.mu.Unlock()
}

func (f *fakeSource) snapshot() (starts []screenParams, stops, keys int, bitrates []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]screenParams(nil), f.starts...), f.stops, f.keys, append([]int64(nil), f.bitrates...)
}

// fakeInput is an InputSink that records what reaches it.
type fakeInput struct {
	mu       sync.Mutex
	why      string
	events   []InputEvent
	typed    []string
	releases int
}

func (f *fakeInput) Unavailable() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.why
}

func (f *fakeInput) setUnavailable(why string) {
	f.mu.Lock()
	f.why = why
	f.mu.Unlock()
}

func (f *fakeInput) Inject(ev InputEvent) error {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
	return nil
}

func (f *fakeInput) TypeText(text string) error {
	f.mu.Lock()
	f.typed = append(f.typed, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeInput) ReleaseAll() {
	f.mu.Lock()
	f.releases++
	f.mu.Unlock()
}

func (f *fakeInput) snapshot() ([]InputEvent, []string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InputEvent(nil), f.events...), append([]string(nil), f.typed...), f.releases
}

// wireFrame is one host -> client frame.
type wireFrame struct {
	typ     byte
	payload []byte
}

// pviewer is the client end of a provided session over a pipe.
type pviewer struct {
	t      *testing.T
	conn   net.Conn
	frames chan wireFrame
	done   chan error
}

// openPViewer serves a session of sc with opts and reads what it sends.
func openPViewer(t *testing.T, sc *Screen, opts Options) *pviewer {
	t.Helper()
	hostEnd, clientEnd := net.Pipe()
	raw, _ := json.Marshal(opts)
	v := &pviewer{t: t, conn: clientEnd, frames: make(chan wireFrame, 1024), done: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { v.done <- sc.Serve(ctx, nil, newPipeStream(hostEnd), raw) }()
	go func() {
		for {
			typ, payload, err := wire.ReadFrame(clientEnd, nil)
			if err != nil {
				close(v.frames)
				return
			}
			v.frames <- wireFrame{typ, append([]byte(nil), payload...)}
		}
	}()
	t.Cleanup(func() { cancel(); clientEnd.Close(); hostEnd.Close() })
	return v
}

func (v *pviewer) send(c Control) {
	v.t.Helper()
	if err := wire.WriteJSON(v.conn, c); err != nil {
		v.t.Fatal(err)
	}
}

// next returns the next frame of type typ, skipping stats and pongs.
func (v *pviewer) next(typ byte) wireFrame {
	v.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f, ok := <-v.frames:
			if !ok {
				v.t.Fatalf("stream ended waiting for %q", typ)
			}
			switch {
			case f.typ == typ:
				return f
			case f.typ == frameStats, f.typ == framePong, f.typ == frameVideo:
				// Not what this wait is about.
			default:
				v.t.Fatalf("got %q (%s) waiting for %q", f.typ, f.payload, typ)
			}
		case <-deadline:
			v.t.Fatalf("no %q frame", typ)
		}
	}
}

func (v *pviewer) hello() Hello {
	v.t.Helper()
	var h Hello
	if err := json.Unmarshal(v.next(frameHello).payload, &h); err != nil {
		v.t.Fatal(err)
	}
	if err := validateHello(&h); err != nil {
		v.t.Fatal(err)
	}
	return h
}

func (v *pviewer) video() (frameHeader, []byte) {
	v.t.Helper()
	f := v.next(frameVideo)
	hdr, err := parseFrameHeader(f.payload)
	if err != nil {
		v.t.Fatal(err)
	}
	return hdr, f.payload[frameHeaderLen:]
}

// waitFor polls cond for up to 5 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitStart(t *testing.T, src *fakeSource) screenParams {
	t.Helper()
	select {
	case p := <-src.started:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("the source was not started")
	}
	return screenParams{}
}

func newTestScreen(src *fakeSource, in InputSink) *Screen {
	sc := NewScreen(ScreenConfig{Source: src, Input: in, Encoder: "test-mediacodec", OS: "android"})
	sc.SetScreenInfo(1080, 2400, 0)
	return sc
}

func TestProvidedHello(t *testing.T) {
	src, in := newFakeSource(), &fakeInput{}
	sc := newTestScreen(src, in)
	v := openPViewer(t, sc, Options{FPS: 30})
	p := waitStart(t, src)
	if p.maxSize != 1920 || p.fps != 30 || p.bitrate <= 0 {
		t.Fatalf("started with %+v", p)
	}
	// Deltas before the parameter sets and the first key frame are dropped.
	sc.PushFrame(h264test.AnnexB(h264test.Delta(20)), 0)
	if err := sc.PushFrame(h264test.Config(864, 1920), 0); err != nil {
		t.Fatal(err)
	}
	sc.PushFrame(h264test.AnnexB(h264test.Delta(20)), 0)
	capture := time.Now().Add(-20 * time.Millisecond).UnixMicro()
	if err := sc.PushFrame(h264test.AnnexB(h264test.IDR(100)), capture); err != nil {
		t.Fatal(err)
	}
	h := v.hello()
	if h.Width != 864 || h.Height != 1920 || !h.IsH264() || h.Encoder != "test-mediacodec" || !h.Hardware {
		t.Fatalf("hello %+v", h)
	}
	if h.ScreenWidth != 1080 || h.ScreenHeight != 2400 || !h.Input || h.FPS != 30 {
		t.Fatalf("hello %+v", h)
	}
	info, err := parseAVCC(h.CodecConfig)
	if err != nil || info.Width != 864 || info.Height != 1920 {
		t.Fatalf("avcC %+v %v", info, err)
	}
	if h.Unsupported[ControlLock] == "" || h.Unsupported[ControlType] != "" {
		t.Fatalf("unsupported %v", h.Unsupported)
	}
	hdr, data := v.video()
	if !hdr.key || hdr.seq != 1 || hdr.capture != capture {
		t.Fatalf("first frame %+v", hdr)
	}
	if !bytes.Equal(data, lengthPrefixed([][]byte{h264test.IDR(100)})) {
		t.Fatal("frame data is not the IDR, length-prefixed")
	}
	sc.PushFrame(h264test.AnnexB(h264test.Delta(30)), 0)
	if hdr, _ := v.video(); hdr.key || hdr.seq != 2 {
		t.Fatalf("second frame %+v", hdr)
	}

	// The screen turns: the encoder restarts with a new SPS, and the
	// pviewer gets a new Hello before the new stream's first key frame.
	sc.SetScreenInfo(2400, 1080, 90)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(1920, 864), h264test.PPS(), h264test.IDR(50)), 0)
	h = v.hello()
	if h.Width != 1920 || h.Height != 864 || h.ScreenWidth != 2400 {
		t.Fatalf("hello after rotation %+v", h)
	}
	if hdr, _ := v.video(); !hdr.key || hdr.seq != 3 {
		t.Fatalf("frame after rotation %+v", hdr)
	}

	// The last pviewer leaving stops the source.
	v.conn.Close()
	waitFor(t, "the source to stop", func() bool { _, stops, _, _ := src.snapshot(); return stops == 1 })
}

func TestProvidedKeyframeAndBitrate(t *testing.T) {
	src := newFakeSource()
	sc := newTestScreen(src, nil)
	v := openPViewer(t, sc, Options{})
	waitStart(t, src)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	v.hello()
	v.video()
	_, _, keys0, _ := src.snapshot()
	v.send(Control{Type: ctlKeyframe})
	waitFor(t, "a key frame request", func() bool { _, _, keys, _ := src.snapshot(); return keys > keys0 })

	// A fixed bitrate goes to the encoder without restarting it.
	v.send(Control{Type: ctlConfig, Bitrate: 3_000_000, ID: 7})
	var r Reply
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.ID != 7 || r.Error != "" {
		t.Fatalf("reply %+v", r)
	}
	waitFor(t, "the bitrate", func() bool {
		_, _, _, b := src.snapshot()
		return len(b) > 0 && b[len(b)-1] == 3_000_000
	})
	if starts, _, _, _ := src.snapshot(); len(starts) != 1 {
		t.Fatalf("encoder restarted for a bitrate change: %+v", starts)
	}
	// A lower frame rate restarts it.
	v.send(Control{Type: ctlConfig, FPS: 20})
	if p := waitStart(t, src); p.fps != 20 || p.bitrate != 3_000_000 {
		t.Fatalf("restarted with %+v", p)
	}
	// A config change is announced by a Hello before the next key frame.
	sc.PushFrame(h264test.AnnexB(h264test.Delta(10)), 0)
	sc.PushFrame(h264test.AnnexB(h264test.IDR(10)), 0)
	if h := v.hello(); h.FPS != 20 || h.Bitrate != 3_000_000 {
		t.Fatalf("hello %+v", h)
	}
	if hdr, _ := v.video(); !hdr.key {
		t.Fatal("not a key frame after the Hello")
	}
	// Another monitor: there is only one.
	two := 2
	v.send(Control{Type: ctlConfig, Display: &two, ID: 8})
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.ID != 8 || r.Error == "" {
		t.Fatalf("reply %+v", r)
	}
}

func TestProvidedInput(t *testing.T) {
	src, in := newFakeSource(), &fakeInput{}
	sc := newTestScreen(src, in)
	v := openPViewer(t, sc, Options{})
	waitStart(t, src)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	if !v.hello().Input {
		t.Fatal("no input")
	}
	v.send(Control{Type: InputButton, X: 0.25, Y: 1.5, Button: 0, Down: true})
	v.send(Control{Type: InputMove, X: 0.5, Y: 0.5})
	v.send(Control{Type: InputButton, X: 0.5, Y: 0.5, Button: 0})
	v.send(Control{Type: ctlType, Text: "héllo", ID: 3})
	var r Reply
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.ID != 3 || r.Error != "" {
		t.Fatalf("reply %+v", r)
	}
	ev, typed, _ := in.snapshot()
	if len(ev) != 3 || ev[0].Kind != InputButton || !ev[0].Down || ev[0].X != 0.25 || ev[0].Y != 1 || ev[2].Down {
		t.Fatalf("events %+v", ev)
	}
	if len(typed) != 1 || typed[0] != "héllo" {
		t.Fatalf("typed %q", typed)
	}
	v.send(Control{Type: ctlLock, ID: 4})
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.ID != 4 || r.Error == "" {
		t.Fatalf("lock reply %+v", r)
	}

	// The accessibility service goes off: the session becomes view-only,
	// with a new Hello saying why, before the next key frame.
	in.setUnavailable("the accessibility service is off")
	waitFor(t, "a key frame request", func() bool { _, _, keys, _ := src.snapshot(); return keys > 0 })
	sc.PushFrame(h264test.AnnexB(h264test.IDR(10)), 0)
	h := v.hello()
	if h.Input || h.InputError != "the accessibility service is off" || h.Unsupported[ControlType] == "" {
		t.Fatalf("hello %+v", h)
	}
	if _, _, releases := in.snapshot(); releases != 1 {
		t.Fatalf("%d releases, want 1 (input was held)", releases)
	}
	v.send(Control{Type: InputMove, X: 0.1, Y: 0.1})
	v.send(Control{Type: ctlType, Text: "x", ID: 5})
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.ID != 5 || r.Error == "" {
		t.Fatalf("type reply %+v", r)
	}
	if ev, typed, _ := in.snapshot(); len(ev) != 3 || len(typed) != 1 {
		t.Fatalf("input delivered while off: %+v %q", ev, typed)
	}
	// And on again.
	in.setUnavailable("")
	sc.PushFrame(h264test.AnnexB(h264test.Delta(10)), 0)
	time.Sleep(1100 * time.Millisecond)
	sc.PushFrame(h264test.AnnexB(h264test.IDR(10)), 0)
	if h := v.hello(); !h.Input {
		t.Fatalf("hello %+v", h)
	}
}

func TestProvidedViewOnly(t *testing.T) {
	src, in := newFakeSource(), &fakeInput{}
	sc := newTestScreen(src, in)
	v := openPViewer(t, sc, Options{ViewOnly: true})
	waitStart(t, src)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	h := v.hello()
	if h.Input || h.InputError != "view-only session" {
		t.Fatalf("hello %+v", h)
	}
	v.send(Control{Type: InputButton, X: 0.5, Y: 0.5, Down: true})
	v.send(Control{Type: ctlType, Text: "x", ID: 1})
	var r Reply
	json.Unmarshal(v.next(frameReply).payload, &r)
	if r.Error == "" {
		t.Fatal("typing allowed in a view-only session")
	}
	if ev, typed, _ := in.snapshot(); len(ev) != 0 || len(typed) != 0 {
		t.Fatalf("input delivered: %+v %q", ev, typed)
	}

	// A screen without an input sink is view-only for everyone.
	sc2 := newTestScreen(newFakeSource(), nil)
	v2 := openPViewer(t, sc2, Options{})
	waitStart(t, sc2.cfg.Source.(*fakeSource))
	sc2.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	if h := v2.hello(); h.Input || h.InputError == "" {
		t.Fatalf("hello %+v", h)
	}

	// The host's owner switching input off reaches the open session.
	sc3 := newTestScreen(newFakeSource(), in)
	v3 := openPViewer(t, sc3, Options{})
	waitStart(t, sc3.cfg.Source.(*fakeSource))
	sc3.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	if !v3.hello().Input {
		t.Fatal("no input")
	}
	var sess *providedSession
	waitFor(t, "the session", func() bool {
		sc3.mu.Lock()
		defer sc3.mu.Unlock()
		for s := range sc3.subs {
			sess = s
		}
		return sess != nil
	})
	sess.setHostInput(false)
	src3 := sc3.cfg.Source.(*fakeSource)
	waitFor(t, "a key frame request", func() bool { _, _, keys, _ := src3.snapshot(); return keys > 0 })
	sc3.PushFrame(h264test.AnnexB(h264test.IDR(10)), 0)
	if h := v3.hello(); h.Input || h.InputError != hostInputOff {
		t.Fatalf("hello %+v", h)
	}
}

func TestProvidedRefusesAV1(t *testing.T) {
	sc := newTestScreen(newFakeSource(), nil)
	v := openPViewer(t, sc, Options{Decode: []string{"av1"}})
	var m errorMsg
	json.Unmarshal(v.next(frameError).payload, &m)
	if m.Error == "" {
		t.Fatal("no error")
	}
	// A probe says what the device offers.
	v2 := openPViewer(t, sc, Options{Probe: true})
	var info HostInfo
	json.Unmarshal(v2.next(frameInfo).payload, &info)
	if info.OS != "android" || info.Encoders[codecH264] != "test-mediacodec" || info.Input || info.ScreenHeight != 2400 {
		t.Fatalf("probe %+v", info)
	}
}

// A pviewer that stops acknowledging fills its window: the host drops
// deltas instead of queueing them, and once the backlog drains it asks
// the encoder for a key frame and goes on from there.
func TestProvidedCongestion(t *testing.T) {
	src := newFakeSource()
	sc := newTestScreen(src, nil)
	v := openPViewer(t, sc, Options{FPS: 10})
	waitStart(t, src)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	v.hello()
	v.video()
	// Window before the first ack: startWindow of video at 10 fps.
	want := int(10*startWindow.Seconds()+0.5) - 1
	for i := 0; i < want; i++ {
		sc.PushFrame(h264test.AnnexB(h264test.Delta(10)), 0)
		v.video()
	}
	_, _, keys0, _ := src.snapshot()
	for i := 0; i < 20; i++ {
		sc.PushFrame(h264test.AnnexB(h264test.Delta(10)), 0)
		time.Sleep(time.Millisecond)
	}
	select {
	case f := <-v.frames:
		if f.typ == frameVideo {
			t.Fatal("a frame was sent past the full window")
		}
	case <-time.After(200 * time.Millisecond):
	}
	// Acknowledge everything: the drop waits on a key frame, which is
	// asked for; deltas meanwhile are still dropped.
	v.send(Control{Type: ctlAck, Seq: uint32(want + 1)})
	waitFor(t, "a key frame request", func() bool { _, _, keys, _ := src.snapshot(); return keys > keys0 })
	sc.PushFrame(h264test.AnnexB(h264test.Delta(10)), 0)
	sc.PushFrame(h264test.AnnexB(h264test.IDR(10)), 0)
	hdr, _ := v.video()
	if !hdr.key || hdr.seq != uint32(want+2) {
		t.Fatalf("after the drop: %+v", hdr)
	}
	var st HostStats
	for st.Dropped == 0 {
		json.Unmarshal(v.next(frameStats).payload, &st)
	}
	if st.Dropped < 20 {
		t.Fatalf("stats %+v", st)
	}
}

// Two viewers share the encoder: the larger frame rate, the smaller
// bitrate; the source stops when both have left.
func TestProvidedTwoViewers(t *testing.T) {
	src := newFakeSource()
	sc := newTestScreen(src, nil)
	v1 := openPViewer(t, sc, Options{FPS: 30, Bitrate: 4_000_000})
	waitStart(t, src)
	v2 := openPViewer(t, sc, Options{FPS: 60, Bitrate: 2_000_000})
	if p := waitStart(t, src); p.fps != 60 {
		t.Fatalf("restarted with %+v", p)
	}
	waitFor(t, "the bitrate", func() bool {
		starts, _, _, b := src.snapshot()
		return starts[len(starts)-1].bitrate == 2_000_000 || len(b) > 0 && b[len(b)-1] == 2_000_000
	})
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	v1.hello()
	v2.hello()
	v1.conn.Close()
	time.Sleep(100 * time.Millisecond)
	if _, stops, _, _ := src.snapshot(); stops != 0 {
		t.Fatal("stopped with a pviewer left")
	}
	v2.conn.Close()
	waitFor(t, "the source to stop", func() bool { _, stops, _, _ := src.snapshot(); return stops == 1 })
	if sc.Viewers() != 0 {
		t.Fatal("viewers left")
	}
}

// A source that cannot start yet (its owner has not allowed screen
// capture) is retried, and the pviewer is told why it waits.
func TestProvidedSourceNotReady(t *testing.T) {
	src := newFakeSource()
	src.fail = errors.New("screen sharing has not been allowed on the device yet")
	sc := newTestScreen(src, nil)
	v := openPViewer(t, sc, Options{})
	var st HostStats
	for st.Paused == "" {
		json.Unmarshal(v.next(frameStats).payload, &st)
	}
	if st.Paused != src.fail.Error() {
		t.Fatalf("paused %q", st.Paused)
	}
	src.mu.Lock()
	src.fail = nil
	src.mu.Unlock()
	waitStart(t, src)
	sc.PushFrame(h264test.AnnexB(h264test.SPS(640, 1280), h264test.PPS(), h264test.IDR(10)), 0)
	v.hello()
	// Closing the screen ends the session with a reason.
	sc.Close()
	var m errorMsg
	json.Unmarshal(v.next(frameError).payload, &m)
	if m.Error != errScreenClosed.Error() {
		t.Fatalf("error %q", m.Error)
	}
}
