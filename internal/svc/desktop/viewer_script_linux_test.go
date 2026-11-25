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
