package desktop

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// animate keeps the screen changing the way video or scrolling does: every
// 16 ms a handful of rectangles of random colour are painted, covering
// roughly a fifth of the screen. It stops when the test ends.
func animate(t *testing.T, x *xgb.Conn, w, h int) {
	t.Helper()
	root := xproto.Setup(x).DefaultScreen(x).Root
	gc, err := xproto.NewGcontextId(x)
	if err != nil {
		t.Fatal(err)
	}
	xproto.CreateGC(x, gc, xproto.Drawable(root), 0, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rng := rand.New(rand.NewSource(1))
		tick := time.NewTicker(16 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			var rs []xproto.Rectangle
			for i := 0; i < 6; i++ {
				rw, rh := w/5+rng.Intn(w/10), h/4+rng.Intn(h/10)
				rs = append(rs, xproto.Rectangle{X: int16(rng.Intn(w - rw)), Y: int16(rng.Intn(h - rh)), Width: uint16(rw), Height: uint16(rh)})
				xproto.ChangeGC(x, gc, xproto.GcForeground, []uint32{uint32(rng.Intn(1 << 24))})
				xproto.PolyFillRectangle(x, xproto.Drawable(root), gc, rs[len(rs)-1:])
			}
		}
	}()
	t.Cleanup(func() { close(stop); wg.Wait() })
}

func TestBenchXvfb(t *testing.T) {
	x := startXvfb(t, 640, 360)
	animate(t, x, 640, 360)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Bench(ctx, client, Options{FPS: 30, Codec: codecH264}, BenchOptions{Duration: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + res.String())
	if res.Width != 640 || res.Height != 360 || res.Codec == "" || res.Encoder == "" {
		t.Fatalf("stream description %+v", res)
	}
	if res.FPS < 20 || res.FPS > 31 {
		t.Errorf("%.1f fps for a requested 30", res.FPS)
	}
	if res.BitrateMbps <= 0.05 {
		t.Errorf("bitrate %.3f Mbit/s", res.BitrateMbps)
	}
	if res.Latency.N < 60 || res.Latency.Mean <= 0 || res.Latency.Mean > 500 || res.Latency.P95 < res.Latency.P50 || res.Latency.Max < res.Latency.P95 {
		t.Errorf("latency %+v", res.Latency)
	}
	if res.KeyFrames > 2 {
		t.Errorf("%d key frames in 3 s: the encoder restarted", res.KeyFrames)
	}
}

func TestBenchWithAudio(t *testing.T) {
	syntheticAudio(t)
	x := startXvfb(t, 320, 240)
	animate(t, x, 320, 240)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Bench(ctx, client, Options{FPS: 15, Codec: codecH264}, BenchOptions{Duration: 2 * time.Second, Audio: true})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Audio
	if a == nil || a.Packets < 80 || a.Packets > 105 || a.BitrateKbps < 80 || a.BitrateKbps > 115 || a.Latency.N != a.Packets {
		t.Fatalf("audio: %+v", a)
	}
	if !strings.Contains(res.String(), "audio") {
		t.Fatal("the report does not mention audio")
	}
}

func TestDesktopCommandBench(t *testing.T) {
	x := startXvfb(t, 320, 240)
	animate(t, x, 320, 240)
	client, _ := peertest.Pair(t)
	var out, errb syncBuffer
	env := &cli.Env{Target: "test", Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }}
	code := cli.Lookup("desktop").Run(context.Background(), env, []string{"--bench", "--bench-duration", "2s", "--fps", "15", "--codec", "h264", "--json"})
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var res BenchResult
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil || res.Frames < 20 || res.Latency.N == 0 || res.Width != 320 {
		t.Fatalf("json %q: %v", out.String(), err)
	}
	out2, errb2 := syncBuffer{}, syncBuffer{}
	env2 := &cli.Env{Target: "test", Stdout: &out2, Stderr: &errb2, DialFunc: env.DialFunc}
	if c := cli.Lookup("desktop").Run(context.Background(), env2, []string{"--bench", "--bench-duration", "2s", "--fps", "15", "--codec", "h264"}); c != cli.ExitOK ||
		!strings.Contains(out2.String(), "capture to received") {
		t.Fatalf("text report (exit %d): %s %s", c, out2.String(), errb2.String())
	}
	// Bad combinations fail before connecting.
	for _, args := range [][]string{{"--bench", "--record", "x.mp4"}, {"--bench", "--bench-duration", "100ms"}, {"--display", "x"}, {"--display", "-3"}} {
		if c := cli.Lookup("desktop").Run(context.Background(), env, args); c != cli.ExitUsage {
			t.Errorf("%v: exit %d", args, c)
		}
	}
}

// REMOHOP_BENCH_REPORT=1 prints the numbers for docs/README: a 1280x720 Xvfb
// with the animation above, each configuration measured for 10 s over a
// loopback QUIC session between two processes' worth of code in one.
//
//	REMOHOP_BENCH_REPORT=1 go test ./internal/svc/desktop -run TestBenchReportXvfb -v -count=1
func TestBenchReportXvfb(t *testing.T) {
	if os.Getenv("REMOHOP_BENCH_REPORT") == "" {
		t.Skip("set REMOHOP_BENCH_REPORT=1 to print benchmark numbers")
	}
	x := startXvfb(t, 1280, 720)
	animate(t, x, 1280, 720)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name string
		o    Options
	}{
		{"h264 60 fps", Options{FPS: 60, Codec: codecH264}},
		{"h264 30 fps", Options{FPS: 30, Codec: codecH264}},
		{"av1 30 fps", Options{FPS: 30, Codec: codecAV1}},
	} {
		res, err := Bench(ctx, client, tc.o, BenchOptions{Duration: 10 * time.Second})
		if err != nil {
			t.Logf("%s: %v", tc.name, err)
			continue
		}
		b, _ := json.Marshal(res)
		t.Logf("%s\n%s%s", tc.name, res.String(), b)
	}
	t.Run("audio", func(t *testing.T) {
		startPulse(t)
		playTone(t, "440")
		res, err := Bench(ctx, client, Options{FPS: 30, Codec: codecH264}, BenchOptions{Duration: 10 * time.Second, Audio: true})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(res)
		t.Logf("h264 30 fps with PulseAudio audio\n%s%s", res.String(), b)
	})
}
