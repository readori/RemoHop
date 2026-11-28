package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/svc/desktop"
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
  say: (el, k, v) => { if (!el) return; el.textContent = !k ? '' : k === 'raw' ? v.s : k + (v ? ' ' + JSON.stringify(v) : ''); },
  sayAttr: (el, a, k) => { if (el) el.setAttribute(a, k); }};
</script><script src="/i18n.js"></script><script src="/pointer.js"></script><script src="/audio.js"></script><script src="/desktop.js"></script></body></html>`
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
		case "/desktop.js", "/style.css", "/i18n.js":
			http.ServeFile(w, r, filepath.Join("assets", r.URL.Path[1:]))
		case "/pointer.js", "/audio.js":
			w.Header().Set("Content-Type", "text/javascript")
			if r.URL.Path == "/audio.js" {
				w.Write(desktop.AudioJS)
			} else {
				w.Write(desktop.PointerJS)
			}
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

		AskedCursor    bool
		PointerInside  pagePointer
		PointerOutside struct {
			Hidden                                     bool
			Left, Top, Width, CanvasLeft, CanvasTop, K float64
		}
		HiddenPointer struct {
			CSS, Toast   string
			LockDisabled bool
		}
		Locked struct {
			Locked          bool
			Req             []map[string]any
			Button, Pressed string
		}
		LockedSent []sentMsg
		Unlocked   bool
		Auto       struct {
			Locked  bool
			Stored  string
			Buttons int
		}
		ViewOnly struct {
			LockDisabled, Overlay bool
			CSS                   string
			Sent                  int
		}
		NoRelative struct {
			Disabled bool
			Title    string
		}
		Strings struct {
			Missing          []string
			Count            int
			ZhLock, ZhLocked string
		}

		AudioCodec                                    string
		AudioAsked                                    []map[string]any
		AudioOn, AudioMuted, AudioEnded, AudioRefused pageAudio
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
	checkPagePointer(t, res.AskedCursor, res.PointerInside, res.HiddenPointer.CSS, res.HiddenPointer.Toast, res.HiddenPointer.LockDisabled)
	if o := res.PointerOutside; o.Hidden || math.Abs(o.Left-(o.CanvasLeft+(640-4)*o.K)) > 1.5 || math.Abs(o.Top-(o.CanvasTop+(480-6)*o.K)) > 1.5 ||
		math.Abs(o.Width-16*o.K) > 1.5 {
		t.Errorf("pointer drawn while the mouse is outside, at host 640,480 with hotspot 4,6: %+v", o)
	}
	if l := res.Locked; !l.Locked || len(l.Req) == 0 || l.Req[0]["unadjustedMovement"] != true || l.Button != "d.punlock" || l.Pressed != "true" {
		t.Errorf("Ctrl+Alt+Shift+M did not lock the mouse asking for raw movement: %+v", l)
	}
	var sent []string
	for _, m := range res.LockedSent {
		sent = append(sent, m.String())
	}
	// The modifiers reach the host (they go down before M does), M does not.
	mods := "key ControlLeft true, key AltLeft true, key ShiftLeft true, key ShiftLeft false, key AltLeft false, key ControlLeft false"
	if got, want := strings.Join(sent, ", "), mods+", move_by 3,-2, button 0 true rel, button 0 false rel, "+mods; got != want || !res.Unlocked {
		t.Errorf("sent while locked (unlocked again: %v)\n%s\nwant\n%s", res.Unlocked, got, want)
	}
	if a := res.Auto; !a.Locked || a.Stored != "1" || a.Buttons != 0 {
		t.Errorf("auto lock: %+v", a)
	}
	if v := res.ViewOnly; !v.LockDisabled || !v.Overlay || v.CSS != "default" || v.Sent != 0 {
		t.Errorf("view only: %+v", v)
	}
	if n := res.NoRelative; !n.Disabled || n.Title != "d.plock.norel" {
		t.Errorf("host without relative input: %+v", n)
	}
	if s := res.Strings; len(s.Missing) > 0 || s.Count < 40 || s.ZhLock != "锁定鼠标" || !strings.Contains(s.ZhLocked, "Esc") {
		t.Errorf("strings: %+v", s)
	}
	checkPageAudio(t, res.AudioCodec, res.AudioAsked, res.AudioOn, res.AudioMuted, res.AudioEnded, res.AudioRefused)
	if strings.Contains(res.Ended.Text, "DISPLAY is not set") || res.Ended.Details != "DISPLAY is not set; X display :0: refused" {
		t.Errorf("ended: %+v", res.Ended)
	}
}

// pagePointer is window.remohopDesktop.cursor().
type pagePointer struct {
	On      bool
	CSS     string
	Decoded int
	Inside  bool
	Overlay *struct{ X, Y float64 }
}

// sentMsg is an input message the page sent.
type sentMsg struct {
	Type, Code string
	Button     int
	Down, Rel  bool
	DX, DY     float64
}

func (m sentMsg) String() string {
	switch m.Type {
	case "key":
		return fmt.Sprintf("key %s %v", m.Code, m.Down)
	case "move_by":
		return fmt.Sprintf("move_by %g,%g", m.DX, m.DY)
	case "button":
		s := fmt.Sprintf("button %d %v", m.Button, m.Down)
		if m.Rel {
			s += " rel"
		}
		return s
	}
	return m.Type
}

// checkPagePointer: the page asked for the pointer, shows the host's shape
// as a sharp CSS cursor over the picture (devicePixelRatio 2), no overlay
// while the mouse is there, none at all while the host hides it, and
// offers the mouse lock then.
func checkPagePointer(t *testing.T, asked bool, in pagePointer, hiddenCSS, toast string, lockDisabled bool) {
	t.Helper()
	if !asked {
		t.Error("the page did not ask to draw the pointer (cursor=1)")
	}
	if !in.On || in.Decoded != 1 || !in.Inside || in.Overlay != nil || !strings.HasPrefix(in.CSS, "image-set(url(\"data:image/png") {
		t.Errorf("pointer over the picture: %+v", in)
	}
	if hiddenCSS != "none" || toast != "d.hidden" || lockDisabled {
		t.Errorf("hidden remote pointer: css %q, toast %q, lock disabled %v", hiddenCSS, toast, lockDisabled)
	}
}

// pageAudio is the Audio and Mute buttons' state.
type pageAudio struct {
	On, Mute, Muted bool
	Pressed, Toast  string
}

// checkPageAudio: Audio asks the gateway for the sound, its answer turns
// the sound on and shows Mute, which mutes; an ended stream and a refusal
// turn it off and say why.
func checkPageAudio(t *testing.T, codec string, asked []map[string]any, on, muted, ended, refused pageAudio) {
	t.Helper()
	if codec != "ok" {
		// Without an Opus decoder the page says so instead of asking.
		if len(asked) != 0 || on.Toast != "d.audio."+codec {
			t.Errorf("audio without a decoder (%s): asked %v, %+v", codec, asked, on)
		}
		return
	}
	if len(asked) != 1 || asked[0]["on"] != true {
		t.Errorf("audio asked: %v", asked)
	}
	if !on.On || on.Pressed != "true" || !on.Mute || on.Muted {
		t.Errorf("audio on: %+v", on)
	}
	if !muted.Muted || !muted.On {
		t.Errorf("muted: %+v", muted)
	}
	if ended.On || ended.Mute || ended.Pressed != "false" || !strings.HasPrefix(ended.Toast, "d.audio.ended") || !strings.Contains(ended.Toast, "stopped capturing") {
		t.Errorf("audio ended: %+v", ended)
	}
	if refused.On || refused.Pressed != "false" || !strings.HasPrefix(refused.Toast, "d.audio.failed") || !strings.Contains(refused.Toast, "switched off") {
		t.Errorf("audio refused: %+v", refused)
	}
}
