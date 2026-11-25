package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// desktopPage is the desktop panel of index.html with desktop.js, a stub
// window.SD and a fake WebSocket that records what the page sends.
func desktopPage(t *testing.T) string {
	t.Helper()
	index, err := os.ReadFile("assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(index)
	i := strings.Index(s, `<div role="tabpanel" id="panel-desktop"`)
	j := strings.Index(s[i+1:], `<div role="tabpanel"`)
	if i < 0 || j < 0 {
		t.Fatal("no desktop panel in index.html")
	}
	panel := strings.Replace(s[i:i+1+j], " hidden>", ">", 1)
	return `<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="/style.css"></head><body>` + panel + `
<script>
window.__sent = []; window.__wss = [];
window.WebSocket = class { constructor(url) { this.url = url; this.readyState = 1; window.__wss.push(this); }
  send(d) { if (this === window.__wss[window.__wss.length - 1]) window.__sent.push(JSON.parse(d)); } close() { this.readyState = 3; } };
window.SD = {tabs: {}, t: k => k, ticket: async () => 'tk', errVar: e => String(e),
  wsURL: (p, q) => 'ws://gw/' + p + '?' + new URLSearchParams(q),
  say: (el, k, v) => { if (!el) return; el.textContent = !k ? '' : k === 'raw' ? v.s : k + (v ? ' ' + JSON.stringify(v) : ''); }};
</script><script src="/desktop.js"></script></body></html>`
}

// The web console's desktop tab in a real browser: view-only and the
// monitor menu do not carry over from one host to the next, overlapping
// mouse buttons and a cancelled pointer each reach the host, held keys
// repeat (modifiers excepted), the host's "screen unavailable" state shows,
// "Actual size" is sharp on a HiDPI screen, the quality menu reaches 1440p
// and 2160p, and an ended session keeps its technical detail under Details.
func TestDesktopTabBrowser(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	if err := exec.Command("node", "-e", "require('playwright')").Run(); err != nil {
		t.Skip("the playwright module is not available to node (NODE_PATH)")
	}
	page := desktopPage(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/desktop.js", "/style.css":
			http.ServeFile(w, r, filepath.Join("assets", r.URL.Path[1:]))
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, page)
		}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", filepath.Join("testdata", "desktop_js.js"))
	cmd.Env = append(os.Environ(), "PAGE_URL=http://"+ln.Addr().String()+"/")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("browser script: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	_, j, ok := strings.Cut(stdout.String(), "RESULT ")
	if !ok {
		t.Fatalf("no result:\n%s\n%s", stdout.String(), stderr.String())
	}
	type state struct {
		ViewOnly, ViewOnlyDisabled bool
		Monitors                   int
		MonitorsHidden             bool
		URL                        string
	}
	var res struct {
		Fatal                       string
		Errors                      []string
		Quality                     []string
		First, SecondBefore, Second state
		Input                       []struct {
			Type, Code string
			Button     int
			Down       bool
		}
		Paused struct {
			Hidden bool
			Text   string
		}
		Unpaused bool
		Actual   string
		Ended    struct{ Text, Details string }
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(j)), &res); err != nil {
		t.Fatalf("result %s: %v", j, err)
	}
	t.Logf("%+v", res)
	if res.Fatal != "" || len(res.Errors) > 0 {
		t.Fatalf("page: %s %q", res.Fatal, res.Errors)
	}
	if strings.Join(res.Quality, ",") != "0,2160,1440,1080,720,480" {
		t.Errorf("quality menu %v", res.Quality)
	}
	if f := res.First; !f.ViewOnly || !f.ViewOnlyDisabled || f.Monitors != 3 || f.MonitorsHidden {
		t.Errorf("host without input, two monitors: %+v", f)
	}
	if s := res.SecondBefore; s.ViewOnly || s.Monitors != 0 || !s.MonitorsHidden || !strings.Contains(s.URL, "view_only=0") {
		t.Errorf("next host, before its hello: %+v (view-only or the monitor menu carried over)", s)
	}
	if s := res.Second; s.ViewOnly || s.ViewOnlyDisabled || !s.MonitorsHidden {
		t.Errorf("next host with input, one monitor: %+v", s)
	}
	var got []string
	for _, m := range res.Input {
		what := fmt.Sprint(m.Button)
		if m.Type == "key" {
			what = m.Code
		}
		got = append(got, fmt.Sprintf("%s %v", what, m.Down))
	}
	want := "0 true, 2 true, 0 false, 2 false, 0 true, 0 false, Backspace true, Backspace true, Backspace false, ShiftLeft true, ShiftLeft false"
	if strings.Join(got, ", ") != want {
		t.Errorf("input sent\n%s\nwant\n%s", strings.Join(got, ", "), want)
	}
	if res.Paused.Hidden || !strings.Contains(res.Paused.Text, "secure screen") || !res.Unpaused {
		t.Errorf("paused banner %+v, hidden again %v", res.Paused, res.Unpaused)
	}
	if res.Actual != "320px" {
		t.Errorf("actual size of a 640-pixel stream at devicePixelRatio 2: %s, want 320px", res.Actual)
	}
	if strings.Contains(res.Ended.Text, "DISPLAY is not set") || res.Ended.Details != "DISPLAY is not set; X display :0: refused" {
		t.Errorf("ended: %+v", res.Ended)
	}
}
