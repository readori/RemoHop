package desktop

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
	"github.com/readori/RemoHop/internal/x11disp"
)

func nextFrame(t *testing.T, ctx context.Context, s *Session) (*Frame, *Hello) {
	t.Helper()
	for {
		ev, err := s.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if ev.Frame != nil || ev.Hello != nil {
			return ev.Frame, ev.Hello
		}
	}
}

// TestPipelineXvfb runs the real host pipeline for each codec: geometry and
// codec string in the hello, a key frame first, frames in sequence, the
// stream recorded to MP4 and decoded by ffmpeg to the painted colours, and
// a key frame on request.
func TestPipelineXvfb(t *testing.T) {
	for _, tc := range []struct{ codec, prefix string }{{codecAV1, "av01.0."}, {codecH264, "avc1."}} {
		t.Run(tc.codec, func(t *testing.T) { testPipeline(t, tc.codec, tc.prefix) })
	}
}

func testPipeline(t *testing.T, codec, prefix string) {
	x := startXvfb(t, 640, 480)
	paint(t, x,
		rect{0, 0, 320, 480, 0xff0000},
		rect{320, 0, 320, 480, 0x0000ff},
		rect{280, 200, 80, 80, 0x00ff00},
	)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	sess, err := Open(ctx, client, Options{FPS: 30, Codec: codec})
	if err != nil {
		if strings.Contains(err.Error(), "no working") {
			t.Skipf("no %s encoder here: %v", codec, err)
		}
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	t.Logf("hello: %dx%d %s %s config %d bytes", h.Width, h.Height, h.Codec, h.Encoder, len(h.CodecConfig))
	if h.Width != 640 || h.Height != 480 {
		t.Fatalf("geometry %dx%d, want 640x480", h.Width, h.Height)
	}
	if !strings.HasPrefix(h.Codec, prefix) || !codecRE.MatchString(h.Codec) || h.Encoder == "" || h.FPS != 30 || len(h.CodecConfig) == 0 {
		t.Fatalf("bad hello %+v", h)
	}
	if h.IsH264() != (codec == codecH264) {
		t.Fatalf("asked for %s, got %s", codec, h.Codec)
	}
	if !h.Input {
		t.Fatalf("input unavailable: %s", h.InputError)
	}

	path := filepath.Join(t.TempDir(), "out.mp4")
	rec, err := NewRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.SetStream(h); err != nil {
		t.Fatal(err)
	}
	var frames []*Frame
	for len(frames) < 25 {
		f, hello := nextFrame(t, ctx, sess)
		if hello != nil {
			t.Fatalf("unexpected hello %+v", hello)
		}
		if err := rec.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	if !frames[0].Key {
		t.Fatal("first frame is not a key frame")
	}
	for i, f := range frames[1:] {
		if f.Key {
			t.Fatalf("frame %d is a key frame within the first GOP", i+1)
		}
		if f.Seq != frames[i].Seq+1 {
			t.Fatalf("sequence gap %d -> %d", frames[i].Seq, f.Seq)
		}
	}
	if frames[0].Capture.IsZero() {
		t.Fatal("no capture timestamps from x11grab")
	}
	lat := frames[len(frames)-1].Received.Sub(frames[len(frames)-1].Capture)
	t.Logf("capture->received latency of last frame: %v", lat)

	w, hh, rgb := decodeLast(t, path)
	if w != 640 || hh != 480 {
		t.Fatalf("decoded %dx%d", w, hh)
	}
	check := func(x, y int, want [3]int) {
		r, g, b := pixel(rgb, w, x, y)
		got := [3]int{r, g, b}
		for i := range got {
			if d := got[i] - want[i]; d > 40 || d < -40 {
				t.Errorf("pixel (%d,%d) = %v, want ~%v", x, y, got, want)
				return
			}
		}
	}
	check(100, 100, [3]int{255, 0, 0})
	check(540, 400, [3]int{0, 0, 255})
	check(290, 210, [3]int{0, 255, 0}) // off-centre: the cursor is drawn at (320,240)

	// A key frame request mid-GOP is served promptly by an encoder restart
	// (the next scheduled key frame is ~2 s away).
	asked := time.Now()
	if err := sess.RequestKeyframe(); err != nil {
		t.Fatal(err)
	}
	sawHello := false
	for {
		f, hello := nextFrame(t, ctx, sess)
		if hello != nil {
			sawHello = true
			continue
		}
		if f.Key {
			break
		}
		if time.Since(asked) > 1500*time.Millisecond {
			t.Fatal("no key frame within 1.5s of the request")
		}
	}
	t.Logf("key frame %v after request (new hello: %v)", time.Since(asked), sawHello)
}

func TestFlowControlXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// With manual acks and none sent, the host must stop after one window
	// of frames instead of queueing.
	sess, err := Open(ctx, client, Options{FPS: 30, ManualAck: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	limit := max(minWindowFrames, 30*int(window/time.Millisecond)/1000)
	var last *Frame
	n := 0
	deadline := time.After(1500 * time.Millisecond)
collect:
	for {
		select {
		case ev := <-sess.events:
			if ev.Frame != nil {
				n++
				last = ev.Frame
			}
		case <-deadline:
			break collect
		}
	}
	t.Logf("received %d unacknowledged frames (window %d)", n, limit)
	if n == 0 || n > limit {
		t.Fatalf("host sent %d frames without acks, window is %d", n, limit)
	}
	// Acknowledging resumes the stream from a key frame.
	if err := sess.Ack(last.Seq); err != nil {
		t.Fatal(err)
	}
	f, _ := nextFrame(t, ctx, sess)
	for f == nil {
		f, _ = nextFrame(t, ctx, sess)
	}
	if !f.Key {
		t.Fatalf("stream resumed with a delta frame (seq %d)", f.Seq)
	}
}

func TestSlowConsumerXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	// Keep the screen changing so frames are not trivially small.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(15 * time.Millisecond):
			}
			paintQuiet(x, i)
		}
	}()

	sess, err := Open(ctx, client, Options{FPS: 60})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	var ms runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms)
	before := ms.HeapInuse

	// A consumer that takes 150 ms per frame (~7 fps against 60).
	slowUntil := time.Now().Add(3 * time.Second)
	var maxInFlight int
	var stats *HostStats
	for time.Now().Before(slowUntil) {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Stats != nil {
			stats = ev.Stats
			maxInFlight = max(maxInFlight, ev.Stats.InFlight)
		}
		if ev.Frame != nil {
			time.Sleep(150 * time.Millisecond)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&ms)
	growth := int64(ms.HeapInuse) - int64(before)
	t.Logf("heap growth %d KiB, max in-flight %d, local drops %d, host stats %+v", growth>>10, maxInFlight, sess.LocalDrops(), stats)
	if growth > 64<<20 {
		t.Fatalf("heap grew by %d MiB under a slow consumer", growth>>20)
	}

	// Consuming at full speed again recovers: a key frame arrives and frames
	// are fresh (no stale backlog).
	var key bool
	var fresh int
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Stats != nil {
			stats = ev.Stats
			maxInFlight = max(maxInFlight, ev.Stats.InFlight)
		}
		f := ev.Frame
		if f == nil {
			continue
		}
		key = key || f.Key
		if key && f.Received.Sub(f.Capture) < 300*time.Millisecond {
			fresh++
		}
	}
	t.Logf("after recovery: key=%v fresh=%d host stats %+v", key, fresh, stats)
	if !key || fresh < 20 {
		t.Fatalf("did not recover: key=%v fresh frames=%d", key, fresh)
	}
	if stats == nil || stats.Dropped == 0 {
		t.Fatal("host dropped nothing although the consumer was far too slow")
	}
	if maxInFlight > 60*int(window/time.Millisecond)/1000+1 {
		t.Fatalf("in-flight %d exceeds the window", maxInFlight)
	}
}

func paintQuiet(c *xgb.Conn, i int) {
	root := xproto.Setup(c).DefaultScreen(c).Root
	gc, err := xproto.NewGcontextId(c)
	if err != nil {
		return
	}
	xproto.CreateGC(c, gc, xproto.Drawable(root), xproto.GcForeground, []uint32{uint32(i*0x10305) & 0xffffff})
	xproto.PolyFillRectangle(c, xproto.Drawable(root), gc, []xproto.Rectangle{{X: int16(i * 37 % 600), Y: int16(i * 53 % 440), Width: 40, Height: 40}})
	xproto.FreeGC(c, gc)
	xproto.GetInputFocus(c).Reply() // round trip: keep the request queue bounded
}

func TestConfigureXvfb(t *testing.T) {
	startXvfb(t, 640, 480)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 30, MaxHeight: 240})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if h := sess.Hello(); h.Width != 320 || h.Height != 240 {
		t.Fatalf("max-height 240 gave %dx%d", h.Width, h.Height)
	}
	// Mid-session: drop the height limit, halve the size, change rate.
	if err := sess.Configure(Config{MaxHeight: -1, Scale: 0.5, FPS: 20, Bitrate: 2_000_000}); err != nil {
		t.Fatal(err)
	}
	for {
		_, h := nextFrame(t, ctx, sess)
		if h == nil {
			continue
		}
		if h.Width != 320 || h.Height != 240 || h.FPS != 20 || h.Bitrate != 2_000_000 {
			t.Fatalf("after configure: %+v", h)
		}
		break
	}
	f, _ := nextFrame(t, ctx, sess)
	if f == nil || !f.Key {
		t.Fatal("new stream does not start with a key frame")
	}
	if err := sess.Configure(Config{Scale: 0.25}); err != nil {
		t.Fatal(err)
	}
	for {
		_, h := nextFrame(t, ctx, sess)
		if h != nil {
			if h.Width != 160 || h.Height != 120 {
				t.Fatalf("scale 0.25 gave %dx%d", h.Width, h.Height)
			}
			return
		}
	}
}

func TestHostErrors(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	open := func(o Options) error {
		s, err := Open(ctx, client, o)
		if err == nil {
			s.Close()
		}
		return err
	}

	origPath := os.Getenv("PATH")
	t.Setenv("REMOHOP_FFMPEG", "")
	t.Setenv("PATH", t.TempDir())
	if err := open(Options{}); err == nil || !strings.Contains(err.Error(), "ffmpeg not found") || !strings.Contains(err.Error(), "apt install ffmpeg") {
		t.Fatalf("missing ffmpeg: %v", err)
	}
	t.Setenv("PATH", origPath)

	startXvfb(t, 320, 240)
	if err := open(Options{Encoder: "av1_amf"}); err == nil || !strings.Contains(err.Error(), "av1_amf") {
		t.Fatalf("unavailable encoder: %v", err)
	}
	if err := open(Options{Display: 9}); err == nil || !strings.Contains(err.Error(), "display 9 does not exist") {
		t.Fatalf("bad monitor: %v", err)
	}

	// A malformed request (bypassing the client's own validation) is
	// refused with a reason.
	st, err := client.Open(ctx, ServiceName, map[string]any{"fps": 100000})
	if err != nil {
		t.Fatal(err)
	}
	typ, body, err := wire.ReadFrame(st, nil)
	if err != nil || typ != frameError || !strings.Contains(string(body), "fps") {
		t.Fatalf("bad request answer: %c %s %v", typ, body, err)
	}
	st.Close()

	// An explicit but dead DISPLAY falls back to probing local sockets, so
	// hide the machine's real X servers.
	defer x11disp.SetSocketDir(t.TempDir())()
	t.Setenv("DISPLAY", ":4242")
	if err := open(Options{}); err == nil || !strings.Contains(err.Error(), "X display :4242") {
		t.Fatalf("bad display: %v", err)
	}
}

// A host daemon without DISPLAY must skip stale X sockets (lower numbered
// than the live server) and hand ffmpeg the live display in its environment.
func TestDiscoverSkipsStaleSocket(t *testing.T) {
	startXvfb(t, 320, 200)
	live := os.Getenv("DISPLAY") // startXvfb pointed it at the real server
	dir := t.TempDir()
	ln, err := net.Listen("unix", filepath.Join(dir, "X0"))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if err := os.Symlink("/tmp/.X11-unix/X"+strings.TrimPrefix(live, ":"), filepath.Join(dir, "X7")); err != nil {
		t.Fatal(err)
	}
	defer x11disp.SetSocketDir(dir)()
	t.Setenv("DISPLAY", "")

	spec, err := discoverCapture(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if spec.x11.Name != ":7" || spec.width != 320 {
		t.Fatalf("spec %+v", spec)
	}
	if !slices.Contains(spec.env, "DISPLAY=:7") || spec.input[len(spec.input)-1] != ":7" {
		t.Fatalf("ffmpeg env %v input %v", spec.env, spec.input)
	}
}

// On a host whose SVT-AV1 is one of the versions that lose screen updates
// the default is H.264 anyway (libx264, no SVT warning: SVT-AV1 is never
// tried). Forcing AV1 gets libaom (or SVT-AV1 at its slow preset where
// there is no libaom), and the hello says why.
func TestAffectedSVTFallsBackXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	real, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	// The real ffmpeg, with SVT-AV1's version banner rewritten to 2.3.0
	// and its exit status preserved.
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nst='" + dir + "'/status.$$\n" +
		"{ { '" + real + "' \"$@\" 2>&1 1>&3; echo $? >\"$st\"; } | sed -u 's/Encoder Lib v[0-9.]*/Encoder Lib v2.3.0/' >&2; } 3>&1\n" +
		"exit \"$(cat \"$st\")\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOHOP_FFMPEG", wrapper)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if hasEncoder(t, real, "libx264") {
		sess, err := Open(ctx, client, Options{FPS: 15})
		if err != nil {
			t.Fatal(err)
		}
		h := sess.Hello()
		sess.Close()
		if h.Encoder != "libx264" || strings.Contains(h.Warning, "SVT") {
			t.Fatalf("default: %s, warning %q", h.Encoder, h.Warning)
		}
	}

	sess, err := Open(ctx, client, Options{FPS: 15, Codec: codecAV1})
	if err != nil {
		if strings.Contains(err.Error(), "no working AV1 encoder") {
			t.Skipf("no AV1 encoder to fall back to: %v", err)
		}
		t.Fatal(err)
	}
	defer sess.Close()
	h := sess.Hello()
	switch h.Encoder {
	case "libaom-av1":
		if !strings.Contains(h.Warning, "SVT-AV1 2.3.0") || !strings.Contains(h.Warning, "libaom-av1") {
			t.Fatalf("warning %q", h.Warning)
		}
	case "libsvtav1": // an ffmpeg without libaom: the slow preset
		if !strings.Contains(h.Warning, "slow preset") {
			t.Fatalf("warning %q", h.Warning)
		}
	default:
		t.Fatalf("encoder %s", h.Encoder)
	}
	if f, _ := nextFrame(t, ctx, sess); f == nil || !f.Key {
		t.Fatal("no key frame from the fallback encoder")
	}
}

func hasEncoder(t *testing.T, ffmpeg, name string) bool {
	t.Helper()
	out, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	return err == nil && strings.Contains(string(out), " "+name+" ")
}
