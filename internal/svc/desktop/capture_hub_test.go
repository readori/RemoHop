package desktop

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// One capture (a synthetic source here) feeds two readers at their own
// rates, a faster reader raises the capture's rate without disturbing the
// first, and the capture stops when the last reader has left.
func TestCaptureHub(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shared capture is used on macOS hosts; its pipes are tested on Unix")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	const w, h = 320, 240
	var started []int
	input := func(fps int) []string {
		started = append(started, fps)
		return []string{"-re", "-f", "lavfi", "-i", fmt.Sprintf("testsrc=s=%dx%d:r=%d", w, h, fps)}
	}
	// The registry outlives a test run (-count): a fresh key gets a fresh
	// capture, with this run's input function.
	key := fmt.Sprintf("test:%s:%d", t.Name(), time.Now().UnixNano())
	// count reads whole frames from a reader's pipe until it is closed.
	count := func(r *os.File, n *int, done chan struct{}) {
		defer close(done)
		buf := make([]byte, w*h*3/2)
		for {
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			*n++
		}
	}
	slowR, slowRelease, err := attachCapture(key, ffmpeg, input, nil, w, h, 10)
	if err != nil {
		t.Fatal(err)
	}
	var slow, fast int
	slowDone, fastDone := make(chan struct{}), make(chan struct{})
	go count(slowR, &slow, slowDone)
	time.Sleep(time.Second)
	fastR, fastRelease, err := attachCapture(key, ffmpeg, input, nil, w, h, 30)
	if err != nil {
		t.Fatal(err)
	}
	go count(fastR, &fast, fastDone)
	time.Sleep(2 * time.Second)
	fastRelease()
	slowRelease()
	fastR.Close()
	slowR.Close()
	<-slowDone
	<-fastDone
	t.Logf("capture started at %v fps; 10 fps reader got %d frames in 3 s, 30 fps reader %d in 2 s", started, slow, fast)
	if fmt.Sprint(started) != "[10 30]" {
		t.Errorf("capture rates %v, want it started at 10 and again at 30", started)
	}
	// Loose bounds: the synthetic source delivers its first second in a
	// burst, and a loaded machine delivers late. What matters is that each
	// reader gets about its own rate and the slow one is held to it.
	if slow < 15 || slow > 45 {
		t.Errorf("the 10 fps reader got %d frames in 3 s", slow)
	}
	if fast < 35 || fast > 95 {
		t.Errorf("the 30 fps reader got %d frames in 2 s", fast)
	}
	var hub *captureHub
	hubs.mu.Lock()
	for k, v := range hubs.m {
		if strings.HasPrefix(k, key+"|") {
			hub = v
		}
	}
	hubs.mu.Unlock()
	if hub == nil {
		t.Fatal("no hub registered")
	}
	deadline := time.Now().Add(hubLinger + 3*time.Second)
	for {
		hub.mu.Lock()
		running, subs := hub.run != nil, len(hub.subs)
		hub.mu.Unlock()
		if !running && subs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capture still running=%v with %d readers after the last one left", running, subs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A capture that cannot start says why.
	_, _, err = attachCapture(key+"bad", ffmpeg, func(int) []string { return []string{"-f", "lavfi", "-i", "nosuchsource"} }, nil, w, h, 10)
	if err == nil || !strings.Contains(err.Error(), "screen capture failed") {
		t.Errorf("a failing capture: %v", err)
	}
}
