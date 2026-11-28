package quick

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/ffmpeg"
)

// TestPageInBrowser runs the real page in headless Chromium through
// Playwright (testdata/page.test.js): rendering in both languages, every
// button, hostile names, and what another origin on the same machine can do
// to the UI (nothing). It is skipped where node or Playwright is missing.
//
// With REMOHOP_QUICK_SHOTS set to a directory it also writes the screenshots
// used in the documentation there (device scale factor 1).
func TestPageInBrowser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if out, err := exec.Command(node, "-e", "require('playwright')").CombinedOutput(); err != nil {
		t.Skipf("playwright is not available to node: %s", firstLine(string(out)))
	}

	f := newFake()
	s := startServer(t, f)
	request := Request{N: 7, Device: "helper-laptop", Identity: "3f9a:01c2:77de:b0a4:5e16", From: "203.0.113.5", Auth: "password", Left: 57}
	steps := map[string]func(*State){
		"reset": func(st *State) {
			*st = newFake().st
			f.calls, f.fail = nil, nil
		},
		"request": func(st *State) { st.Requests = []Request{request} },
		"hostile": func(st *State) {
			r := request
			r.N, r.Device = 8, "<img src=x onerror=alert(1)>"
			st.Requests = []Request{r}
		},
		"action": func(st *State) {
			st.Requests = []Request{{N: 9, Name: "ci-agent", Grant: "ci-agent", Action: "exec: git push", Identity: request.Identity, From: request.From, Auth: "token", Left: 50}}
		},
		"noffmpeg": func(st *State) {
			st.NoFFmpeg = true
			st.FFmpeg = ffmpeg.UIState{Missing: true, CanInstall: true, SizeMB: 119, Version: "9.1", Source: "gyan.dev (release essentials build)"}
		},
		"ffmpegverify": func(st *State) { st.FFmpeg.Stage, st.FFmpeg.Percent = "verifying", 100 },
		"ffmpegfail": func(st *State) {
			st.FFmpeg.Stage, st.FFmpeg.Percent, st.FFmpeg.Error = "", 0, "downloading ffmpeg 9.1: github.com: HTTP 404"
		},
		"ffmpegoff": func(st *State) {
			st.FFmpeg = ffmpeg.UIState{Missing: true, Disabled: "REMOHOP_FFMPEG_DOWNLOAD=off", SizeMB: 119, Version: "9.1", Source: "gyan.dev"}
		},
		"ffmpeg":     func(st *State) { st.NoFFmpeg, st.FFmpeg = false, ffmpeg.UIState{} },
		"offline":    func(st *State) { st.Online = false },
		"online":     func(st *State) { st.Online = true },
		"connecting": func(st *State) { st.Phase, st.ID, st.Online, st.Waited = PhaseConnecting, "", false, 20 },
		"setup": func(st *State) {
			*st = State{Phase: PhaseSetup, Options: DefaultOptions(), AskTimeout: 60, Version: "dev", Requests: []Request{}, Sessions: []SessionInfo{}}
		},
		"failconfigure": func(*State) {
			f.fail = &codedError{code: "bad_server", msg: `"bad address" is not a server address; it looks like support.example.com or support.example.com:21116`}
		},
		"okconfigure": func(*State) { f.fail = nil },
		"running": func(st *State) {
			*st = State{Phase: PhaseError, ErrorCode: ErrAlreadyRunning, Error: "already running (pid 4242)", OtherID: "987 654 321",
				Options: DefaultOptions(), Version: "dev", Requests: []Request{}, Sessions: []SessionInfo{}}
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/scenario", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("log") != "" {
			fmt.Fprint(w, f.log())
			return
		}
		change, ok := steps[r.URL.Query().Get("step")]
		if !ok {
			http.Error(w, "no such step", http.StatusNotFound)
			return
		}
		f.set(change)
	})
	mux.HandleFunc("/attack.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html><title>another origin</title><body>")
	})
	side := httptest.NewServer(mux)
	defer side.Close()

	shots := os.Getenv("REMOHOP_QUICK_SHOTS")
	if shots != "" {
		if err := os.MkdirAll(shots, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "testdata/page.test.js", s.URL(), side.URL+"/scenario", side.URL, shots)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PAGE TEST OK") {
		t.Fatalf("page test failed: %v\n%s", err, out)
	}

	// The last thing the script did was close a page: the process hears
	// about it (this is what stops sharing when the window is closed).
	p := waitPresence(t, s, func(p Presence) bool { return p.Viewers == 0 && p.Seen })
	if !p.Bye {
		t.Errorf("closing the page did not announce itself: %+v", p)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
