package desktop

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

//go:embed testdata/viewer_ui.js
var viewerUIScript string

// browserReport is what testdata/viewer_ui.js prints.
type browserReport struct {
	Fatal  string   `json:"fatal"`
	Errors []string `json:"errors"`
	Video  struct {
		Codec         string `json:"codec"`
		Width, Height int
		All           bool
		Decoded       int
	} `json:"video"`
	Menu struct {
		Hidden  bool
		Value   string
		Options []struct{ Value, Text string }
	} `json:"menu"`
	Switched map[string]struct {
		Width  int
		Select string
	} `json:"switched"`
	Audio            map[string]any `json:"audio"`
	AudioOff         bool           `json:"audioOff"`
	Muted            bool           `json:"muted"`
	Unmuted          bool           `json:"unmuted"`
	SkewMS           float64        `json:"skewMs"`
	AudioToast       string         `json:"audioToast"`
	DropOverlayShown bool           `json:"dropOverlayShown"`
	DropOverlayHide  bool           `json:"dropOverlayHidden"`
	Uploads          []string       `json:"uploads"`
	Flips            struct {
		Sent, Seen     int
		Mean, P50, P95 float64
		Max            float64
	} `json:"flips"`
	ViewerLatency float64 `json:"viewerLatency"`
	FPS           float64 `json:"fps"`
}

// requireBrowser skips unless Node with Playwright and a Chromium are here.
func requireBrowser(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	if err := exec.Command("node", "-e", "require('playwright')").Run(); err != nil {
		t.Skip("the playwright module is not available to node (NODE_PATH)")
	}
}

// runViewerUI starts the viewer against the current Xvfb and drives its page
// in headless Chromium.
func runViewerUI(t *testing.T, x *xgb.Conn, audio bool, codecs []string) browserReport {
	t.Helper()
	requireBrowser(t)
	client, _ := peertest.Pair(t)
	v, err := newViewer(client, Options{FPS: 30}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	defer v.close()

	// The flash server: paints a 100x100 patch in the middle of the screen
	// black or white and says when.
	root := xproto.Setup(x).DefaultScreen(x).Root
	gc, _ := xproto.NewGcontextId(x)
	xproto.CreateGC(x, gc, xproto.Drawable(root), 0, nil)
	var mu sync.Mutex
	white := false
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		white = !white
		color := uint32(0)
		if white {
			color = 0xffffff
		}
		xproto.ChangeGC(x, gc, xproto.GcForeground, []uint32{color})
		err := xproto.PolyFillRectangleChecked(x, xproto.Drawable(root), gc, []xproto.Rectangle{{X: 350, Y: 100, Width: 100, Height: 100}}).Check()
		now := time.Now().UnixMicro()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		fmt.Fprintf(w, `{"t":%d}`, now)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	dir := t.TempDir()
	script := filepath.Join(dir, "viewer_ui.js")
	if err := os.WriteFile(script, []byte(viewerUIScript), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "VIEWER_URL="+v.URL(), "FLIP_URL=http://"+ln.Addr().String()+"/", "AUDIO="+map[bool]string{true: "1", false: "0"}[audio])
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("browser script: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var rep browserReport
	for _, l := range strings.Split(stdout.String(), "\n") {
		if j, ok := strings.CutPrefix(l, "RESULT "); ok {
			t.Logf("raw report: %s", j)
			if err := json.Unmarshal([]byte(j), &rep); err != nil {
				t.Fatalf("report %q: %v", j, err)
			}
			return rep
		}
	}
	t.Fatalf("no report from the browser script:\n%s\n%s", stdout.String(), stderr.String())
	return rep
}

// The viewer page in a real browser: video decodes, the monitor menu
// switches monitors live (and back to all of them), audio plays in time
// with the video and can be muted, dropped and picked files reach the
// host's Downloads folder, and a flash on the remote screen shows up on the
// canvas.
func TestViewerPageXvfb(t *testing.T) {
	x := twoMonitors(t)
	startPulse(t)
	playTone(t, "440")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	rep := runViewerUI(t, x, true, nil)
	t.Logf("report: %+v", rep)
	if rep.Fatal != "" {
		t.Fatalf("browser script failed: %s", rep.Fatal)
	}
	if len(rep.Errors) > 0 {
		t.Errorf("console errors: %q", rep.Errors)
	}
	if rep.Video.Width != 800 || rep.Video.Height != 300 || !rep.Video.All || rep.Video.Decoded < 20 {
		t.Errorf("video %+v", rep.Video)
	}

	// Monitor menu: all monitors first and selected, then each monitor.
	if rep.Menu.Hidden || rep.Menu.Value != "-1" || len(rep.Menu.Options) < 3 || rep.Menu.Options[0].Value != "-1" || rep.Menu.Options[0].Text != "All monitors" {
		t.Errorf("menu %+v", rep.Menu)
	}
	if s := rep.Switched["monitor2"]; s.Width != 400 {
		t.Errorf("switching to monitor 2: %+v", s)
	}
	if s := rep.Switched["all"]; s.Width != 800 || s.Select != "-1" {
		t.Errorf("switching back to all monitors: %+v", s)
	}

	// Audio: decoded, played (non-silent, context running), muted without
	// stopping, and in sync with the video within a jitter buffer.
	if n, _ := rep.Audio["decoded"].(float64); n < 40 {
		t.Errorf("audio decoded only %v packets: %+v", n, rep.Audio)
	}
	if rms, _ := rep.Audio["rms"].(float64); rms < 0.05 {
		t.Errorf("audio is silent in the page: rms %v", rms)
	}
	if st, _ := rep.Audio["ctxState"].(string); st != "running" {
		t.Errorf("audio context is %q", st)
	}
	if u, _ := rep.Audio["underruns"].(float64); u > 5 {
		t.Errorf("%v audio underruns", u)
	}
	if !rep.Muted || !rep.Unmuted || !rep.AudioOff {
		t.Errorf("mute/unmute/off: %v %v %v", rep.Muted, rep.Unmuted, rep.AudioOff)
	}
	if rep.SkewMS < -5 || rep.SkewMS > 250 {
		t.Errorf("audio is %.0f ms behind the video's display latency", rep.SkewMS)
	}

	// Uploads: three files arrive, byte for byte, in Downloads.
	if !rep.DropOverlayShown || !rep.DropOverlayHide {
		t.Errorf("drop overlay shown %v, hidden after the drop %v", rep.DropOverlayShown, rep.DropOverlayHide)
	}
	dl := filepath.Join(home, "Downloads")
	if b, err := os.ReadFile(filepath.Join(dl, "dropped.bin")); err != nil || len(b) != 3<<20 || b[0] != 'A' || b[len(b)-1] != 'A' {
		t.Errorf("dropped.bin: %d bytes, %v", len(b), err)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "hello.txt")); string(b) != "hello from the browser\n" {
		t.Errorf("hello.txt: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "picked.txt")); string(b) != "picked with the button\n" {
		t.Errorf("picked.txt: %q", b)
	}
	if len(rep.Uploads) < 3 || !strings.Contains(strings.Join(rep.Uploads, "|"), "Saved "+dl+"/dropped.bin") {
		t.Errorf("upload rows: %q", rep.Uploads)
	}

	// Latency from a change on the remote screen to the drawn canvas.
	if rep.Flips.Seen < 30 || rep.Flips.P50 <= 0 || rep.Flips.P50 > 400 {
		t.Errorf("flash latency %+v", rep.Flips)
	}
	t.Logf("remote change -> canvas (headless Chromium, %s): mean %.0f ms, p50 %.0f, p95 %.0f, max %.0f over %d of %d flashes; the page's own capture->display figure %.0f ms; %.0f fps",
		rep.Video.Codec, rep.Flips.Mean, rep.Flips.P50, rep.Flips.P95, rep.Flips.Max, rep.Flips.Seen, rep.Flips.Sent, rep.ViewerLatency, rep.FPS)
}

// Without audio on the host, the page says why instead of staying silent.
func TestViewerPageAudioUnavailableXvfb(t *testing.T) {
	x := twoMonitors(t)
	old := setAudioSources(func(context.Context, string) ([]audioSource, string) {
		return nil, "this Mac has no loopback audio device. macOS cannot record the system output by itself: install BlackHole"
	})
	t.Cleanup(func() { setAudioSources(old) })
	t.Setenv("HOME", t.TempDir())
	rep := runViewerUI(t, x, false, nil)
	if rep.Fatal != "" {
		t.Fatalf("browser script failed: %s", rep.Fatal)
	}
	if !strings.Contains(rep.AudioToast, "BlackHole") || !strings.Contains(rep.AudioToast, "Audio") {
		t.Errorf("toast %q", rep.AudioToast)
	}
}
