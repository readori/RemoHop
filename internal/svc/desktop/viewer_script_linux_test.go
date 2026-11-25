package desktop

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

//go:embed testdata/viewer_script.js
var viewerCheckScript string

// runViewerCheck starts the viewer against the current Xvfb, runs one check
// of testdata/viewer_script.js in headless Chromium and decodes its result
// into out.
func runViewerCheck(t *testing.T, opts Options, check string, out any, env ...string) {
	t.Helper()
	requireBrowser(t)
	client, _ := peertest.Pair(t)
	v, err := newViewer(client, opts, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	defer v.close()
	dir := t.TempDir()
	script := filepath.Join(dir, "viewer_script.js")
	if err := os.WriteFile(script, []byte(viewerCheckScript), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "VIEWER_URL="+v.URL(), "CHECK="+check), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("browser script: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	for _, l := range strings.Split(stdout.String(), "\n") {
		j, ok := strings.CutPrefix(l, "RESULT ")
		if !ok {
			continue
		}
		var rep struct {
			Fatal  string          `json:"fatal"`
			Errors []string        `json:"errors"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(j), &rep); err != nil {
			t.Fatalf("report %q: %v", j, err)
		}
		if rep.Fatal != "" {
			t.Fatalf("browser check %s: %s", check, rep.Fatal)
		}
		if len(rep.Errors) > 0 {
			t.Errorf("page errors: %q", rep.Errors)
		}
		if err := json.Unmarshal(rep.Result, out); err != nil {
			t.Fatalf("result %s: %v", rep.Result, err)
		}
		return
	}
	t.Fatalf("no report from the browser script:\n%s\n%s", stdout.String(), stderr.String())
}

// What a browser shows is what the screen showed: the stream says which
// colour matrix and range it was converted with, and WebCodecs in Chromium
// decodes it that way. (An unlabelled BT.601 stream, as the host sent
// before, is decoded as BT.709: pure green came out as 0,216,2.) Tests that
// decode with ffmpeg cannot see this, because ffmpeg assumes BT.601 for an
// unlabelled stream just as its encoder side converts with it.
func TestViewerColorsXvfb(t *testing.T) {
	const w, h = 640, 480
	x := startXvfb(t, w, h)
	colors := []uint32{
		0xff0000, 0x00ff00, 0x0000ff, 0xffffff,
		0x000000, 0x808080, 0xffff00, 0x00ffff,
		0xff00ff, 0xc83c1e, 0x1e90ff, 0x101010,
		0xebebeb, 0x0078d7, 0xf0f0f0, 0x202020,
	}
	var rs []rect
	var points [][2]int
	for i, c := range colors {
		cx, cy := i%4*(w/4), i/4*(h/4)
		rs = append(rs, rect{cx, cy, w / 4, h / 4, c})
		points = append(points, [2]int{cx + w/8, cy + h/8})
	}
	paint(t, x, rs...)
	pts, _ := json.Marshal(points)
	ffmpeg, _ := ffmpegPath()
	cases := []struct{ name, enc string }{{"h264", "libx264"}, {"av1", "libsvtav1"}, {"av1-aom", "libaom-av1"}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !hasEncoder(t, ffmpeg, c.enc) {
				t.Skip(c.enc + " not available")
			}
			var res struct {
				Width, Height int
				Codec         string
				Px            [][3]int
			}
			runViewerCheck(t, Options{FPS: 15, Encoder: c.enc}, "colors", &res, "POINTS="+string(pts))
			if res.Width != w || len(res.Px) != len(colors) {
				t.Fatalf("result %+v", res)
			}
			worst := 0
			var report []string
			for i, c := range colors {
				want := [3]int{int(c >> 16), int(c >> 8 & 0xff), int(c & 0xff)}
				got := res.Px[i]
				d := 0
				for k := range 3 {
					d = max(d, abs(got[k]-want[k]))
				}
				worst = max(worst, d)
				report = append(report, fmt.Sprintf("%v->%v", want, got))
			}
			t.Logf("%s (%s): worst error %d levels: %s", c.enc, res.Codec, worst, strings.Join(report, " "))
			if worst > 4 {
				t.Errorf("colours in the browser are off by up to %d levels", worst)
			}
		})
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

type sentMsg struct {
	Type   string `json:"type"`
	Button int    `json:"button"`
	Down   bool   `json:"down"`
	Code   string `json:"code"`
}

func buttonsText(ms []sentMsg) string {
	var out []string
	for _, m := range ms {
		switch m.Type {
		case "button":
			out = append(out, fmt.Sprintf("%d %s", m.Button, map[bool]string{true: "down", false: "up"}[m.Down]))
		case "key":
			out = append(out, fmt.Sprintf("%s %s", m.Code, map[bool]string{true: "down", false: "up"}[m.Down]))
		default:
			out = append(out, m.Type)
		}
	}
	return strings.Join(out, ", ")
}

// Overlapping mouse buttons each reach the host (Pointer Events report the
// second button of a chord as a pointermove; the page used to send only
// "move" for it, losing the right press and leaving the left button stuck
// down), and a cancelled pointer lets go of its button.
func TestViewerChordXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	var res struct{ Chord, Cancel []sentMsg }
	runViewerCheck(t, Options{FPS: 15}, "chord", &res)
	if got, want := buttonsText(res.Chord), "0 down, 2 down, 0 up, 2 up"; got != want {
		t.Errorf("chord sent %q, want %q", got, want)
	}
	if got, want := buttonsText(res.Cancel), "0 down, 0 up"; got != want {
		t.Errorf("cancelled pointer sent %q, want %q", got, want)
	}
	root := xproto.Setup(x).DefaultScreen(x).Root
	p, err := xproto.QueryPointer(x, root).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if p.Mask&(xproto.KeyButMaskButton1|xproto.KeyButMaskButton2|xproto.KeyButMaskButton3) != 0 {
		t.Errorf("a button is still held on the host: mask %#x", p.Mask)
	}
}

// Held keys repeat on every host: the page forwards the browser's
// repeated keydowns (except for modifiers), which Windows and macOS hosts
// need because their injected keys never repeat by themselves.
func TestViewerKeyRepeatXvfb(t *testing.T) {
	startXvfb(t, 640, 480)
	var res []sentMsg
	runViewerCheck(t, Options{FPS: 15}, "repeat", &res)
	if got, want := buttonsText(res), "ArrowLeft down, ArrowLeft down, ArrowLeft down, ArrowLeft up, ShiftLeft down, ShiftLeft up"; got != want {
		t.Errorf("sent %q, want %q", got, want)
	}
}

// A decoder that fails on every frame after accepting its configuration
// (hardware decoders do) is moved to software, then the host is asked for
// a smaller size, and with none left the page says so; it used to retry
// forever with a black picture. Key frame requests made less than a second
// apart are both sent.
func TestViewerDecodeFailureXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	animate(t, x, 640, 480)
	type result struct {
		Stats struct {
			Decoded, Errors int
		}
		Toast, Msg string
		Sent       []struct {
			Type      string `json:"type"`
			MaxHeight int    `json:"max_height"`
		}
	}
	t.Run("hardware", func(t *testing.T) {
		var res result
		runViewerCheck(t, Options{FPS: 15}, "decodefail", &res, "FAIL=hw")
		t.Logf("%+v", res)
		if res.Stats.Decoded < 10 || !strings.Contains(res.Toast, "software") {
			t.Errorf("not recovered in software: %+v", res)
		}
	})
	t.Run("all", func(t *testing.T) {
		var res result
		runViewerCheck(t, Options{FPS: 15}, "decodefail", &res, "FAIL=all", "WAIT=10000")
		t.Logf("%+v", res)
		keys := 0
		for _, m := range res.Sent {
			if m.Type == "keyframe" {
				keys++
			}
		}
		if res.Msg == "" || !strings.Contains(res.Msg, "cannot decode") {
			t.Errorf("the page does not say it cannot decode: %+v", res)
		}
		if keys < 2 {
			t.Errorf("%d key frame requests sent", keys)
		}
	})
}
