package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// TestBrowserE2E drives the real viewer page in headless Chromium against an
// Xvfb-backed session: frames decode and draw, colours match, glass-to-glass
// latency is measured, and pointer/keyboard input from the page reaches the
// X server. It needs Node with Playwright, so it only runs when
// REMOHOP_BROWSER_E2E names the driver script (browser_e2e.js);
// REMOHOP_BROWSER_SIZE (e.g. 1920x1080), REMOHOP_BROWSER_FPS and
// REMOHOP_BROWSER_WARMUP (e.g. 20s) tune it.
func TestBrowserE2E(t *testing.T) {
	script := os.Getenv("REMOHOP_BROWSER_E2E")
	if script == "" {
		t.Skip("set REMOHOP_BROWSER_E2E=/path/to/browser_e2e.js to run the browser test")
	}
	w, h := 1280, 720
	if s := os.Getenv("REMOHOP_BROWSER_SIZE"); s != "" {
		fmt.Sscanf(s, "%dx%d", &w, &h)
	}
	fps := 60
	if s := os.Getenv("REMOHOP_BROWSER_FPS"); s != "" {
		fps, _ = strconv.Atoi(s)
	}
	x := startXvfb(t, w, h)
	events := eventWindow(t, x)
	paint(t, x, rect{0, 0, w, h, 0x808080}, rect{w * 3 / 4, 0, w / 4, h, 0x00c000})

	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	v, err := newViewer(client, Options{FPS: fps, Codec: os.Getenv("REMOHOP_BROWSER_CODEC")}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	defer v.close()

	cmd := exec.CommandContext(ctx, "node", script, v.URL())
	cmd.Env = append(os.Environ(), fmt.Sprintf("VIEW_W=%d", min(w, 1920)), fmt.Sprintf("VIEW_H=%d", min(h, 1080)))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()
	rd := bufio.NewReader(stdout)
	next := func(want string) map[string]any {
		t.Helper()
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				t.Fatalf("driver ended: %v", err)
			}
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			if m["event"] == "error" {
				t.Fatalf("driver: %v", m["error"])
			}
			if m["event"] == want {
				return m
			}
		}
	}
	say := func(s string) { io.WriteString(stdin, s+"\n") }

	ready := next("ready")
	t.Logf("WebCodecs AV1 support %v; the page decodes %v and got %v; decoded %v frames; configured %v",
		ready["support"], ready["decodable"], ready["codec"], ready["decoded"], ready["configured"])

	// REMOHOP_BROWSER_WARMUP gives rate adaptation time to settle first.
	if s := os.Getenv("REMOHOP_BROWSER_WARMUP"); s != "" {
		d, _ := time.ParseDuration(s)
		time.Sleep(d)
	}

	// Decoded pixels on the canvas match what is on the X screen.
	pts := [][2]int{{w / 4, h / 2}, {w * 7 / 8, h / 2}}
	b, _ := json.Marshal(pts)
	say(string(b))
	px := next("pixels")["samples"].([]any)
	wantRGB := [][3]float64{{128, 128, 128}, {0, 192, 0}}
	for i, s := range px {
		got := s.([]any)
		for c := 0; c < 3; c++ {
			if d := got[c].(float64) - wantRGB[i][c]; d > 40 || d < -40 {
				t.Errorf("canvas pixel %v = %v, want ~%v", pts[i], got, wantRGB[i])
				break
			}
		}
	}

	// Glass-to-glass: paint a square, see when the canvas shows it.
	next("watching")
	type mark struct {
		t time.Time
		v string
	}
	var marks []mark
	for i := 0; i < 12; i++ {
		col, v := uint32(0xff0000), "r"
		if i%2 == 1 {
			col, v = 0x0000ff, "b"
		}
		paint(t, x, rect{0, 0, 100, 100, col})
		marks = append(marks, mark{time.Now(), v})
		time.Sleep(400 * time.Millisecond)
	}
	say("done")
	meas := next("measured")
	var lats []float64
	trans := meas["transitions"].([]any)
	for _, m := range marks {
		for _, tr := range trans {
			o := tr.(map[string]any)
			ts := time.UnixMicro(int64(o["t"].(float64) * 1000))
			if o["v"] == m.v && ts.After(m.t) {
				lats = append(lats, float64(ts.Sub(m.t).Microseconds())/1000)
				break
			}
		}
	}
	if os.Getenv("REMOHOP_BROWSER_DEBUG") != "" {
		for _, m := range marks {
			t.Logf("mark %s at %d", m.v, m.t.Sub(marks[0].t).Milliseconds())
		}
		for _, tr := range trans {
			o := tr.(map[string]any)
			t.Logf("canvas %v at %d", o["v"], time.UnixMicro(int64(o["t"].(float64)*1000)).Sub(marks[0].t).Milliseconds())
		}
	}
	sort.Float64s(lats)
	st := meas["stats"].(map[string]any)
	t.Logf("browser: %.1f fps decoded, %.2f Mbit/s, page latency estimate %.0f ms, decode %.1f ms, skipped %v, errors %v, host %v",
		meas["fps"], meas["mbps"], st["latency"], st["decodeMs"], st["skipped"], st["errors"], st["host"])
	if len(lats) < 10 {
		t.Fatalf("only %d of %d colour changes seen on the canvas", len(lats), len(marks))
	}
	t.Logf("glass-to-glass (X paint -> canvas pixel) ms: min %.0f median %.0f max %.0f", lats[0], lats[len(lats)/2], lats[len(lats)-1])

	// Pointer and keyboard input from the page.
	in := next("input")
	root := xproto.Setup(x).DefaultScreen(x).Root
	wx := int16(in["fx"].(float64)*float64(w-1) + 0.5)
	wy := int16(in["fy"].(float64)*float64(h-1) + 0.5)
	bp := waitEvent(t, events, func(e xproto.ButtonPressEvent) bool { return true })
	if bp.Detail != 1 {
		t.Errorf("button %d", bp.Detail)
	}
	if d := int(bp.RootX-wx) + int(bp.RootY-wy); d > 4 || d < -4 {
		t.Errorf("click at %d,%d, want ~%d,%d", bp.RootX, bp.RootY, wx, wy)
	}
	kp := waitEvent(t, events, func(e xproto.KeyPressEvent) bool { return true })
	if sym := keysym(t, x, kp.Detail); sym != 'q' {
		t.Errorf("key press keysym %#x, want q", sym)
	}
	p, _ := xproto.QueryPointer(x, root).Reply()
	t.Logf("pointer at %d,%d after page input", p.RootX, p.RootY)

	// Controls: the Type box, the keys menu, view-only.
	say("ui")
	for _, want := range []xproto.Keysym{0xffe1, 'z', 0xe9, 0xffe9, 0xff09} { // Shift+z, é, Alt+Tab
		kp := waitEvent(t, events, func(e xproto.KeyPressEvent) bool { return true })
		if sym := keysym(t, x, kp.Detail); sym != want {
			t.Errorf("key press keysym %#x, want %#x", sym, want)
		}
	}
	ui := next("ui")
	t.Logf("controls: %v", ui)
	if ui["viewOnly"] != true || ui["blockDisabled"] != true || !strings.Contains(fmt.Sprint(ui["blockTitle"]), "not supported on X11") {
		t.Errorf("controls state %v", ui)
	}
	if p2, _ := xproto.QueryPointer(x, root).Reply(); p2.RootX != p.RootX || p2.RootY != p.RootY {
		t.Errorf("pointer moved to %d,%d in view-only mode", p2.RootX, p2.RootY)
	}
	say("bye")
	logs := next("logs")
	if l, _ := json.Marshal(logs["logs"]); strings.Contains(string(l), "pageerror") {
		t.Errorf("page errors: %s", l)
	}
}
