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
	// And it leaves the registry (one hub per size seen piled up).
	for end := time.Now().Add(time.Second); ; time.Sleep(20 * time.Millisecond) {
		hubs.mu.Lock()
		_, still := hubs.m[hub.key]
		hubs.mu.Unlock()
		if !still {
			break
		}
		if time.Now().After(end) {
			t.Fatal("a stopped capture stays in the registry")
		}
	}

	// A capture that cannot start says why.
	_, _, err = attachCapture(key+"bad", ffmpeg, func(int) []string { return []string{"-f", "lavfi", "-i", "nosuchsource"} }, nil, w, h, 10)
	if err == nil || !strings.Contains(err.Error(), "screen capture failed") {
		t.Errorf("a failing capture: %v", err)
	}
}

// The hub's NV12 frames keep their colours through a session's encoder: the
// session labels them with what the hub converted them to, so scaling does
// not convert them again and the stream says full-range BT.709.
func TestCaptureHubColors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shared capture is used on macOS hosts; its pipes are tested on Unix")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	const w, h = 64, 48
	input := func(fps int) []string {
		return []string{"-re", "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x1e90ff:s=%dx%d:r=%d,format=bgr0", w, h, fps)}
	}
	key := fmt.Sprintf("test:%s:%d", t.Name(), time.Now().UnixNano())
	r, release, err := attachCapture(key, ffmpeg, input, nil, w, h, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer r.Close()
	args := append([]string{"-hide_banner", "-loglevel", "error"}, hubInput(w, h, 10)...)
	args = append(args, "-frames:v", "3", "-vf", yuvSource(hubColor)+",scale=w=32:h=24:flags=bilinear"+colorFull.scaleOpts()+",format=yuv420p",
		"-c:v", "libx264", "-qp", "0")
	args = append(args, colorFull.encArgs()...)
	out := t.TempDir() + "/hub.h264"
	args = append(args, "-f", "h264", out)
	cmd := exec.Command(ffmpeg, args...)
	cmd.Stdin = r
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("session ffmpeg: %v\n%s", err, b)
	}
	// ffmpeg's decoder honours a labelled stream (it is the unlabelled one
	// it guesses differently from browsers).
	rgb, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil || len(rgb) < 3 {
		t.Fatalf("decode: %v", err)
	}
	i := (12*32 + 16) * 3
	got := [3]int{int(rgb[i]), int(rgb[i+1]), int(rgb[i+2])}
	for k, want := range [3]int{0x1e, 0x90, 0xff} {
		if d := got[k] - want; d > 3 || d < -3 {
			t.Fatalf("colour through the hub %v, want 1e90ff", got)
		}
	}
	probe, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=color_range,color_space,color_primaries,color_transfer", "-of", "csv=p=0", out).Output()
	if p := strings.TrimSpace(string(probe)); p != "pc,bt709,bt709,bt709" {
		t.Errorf("stream colour description %q", p)
	}
}

// A reader slower than the capture gets its own frame rate on average, not
// the capture's rate divided by a whole number (with the capture at 60 fps,
// 26 to 50 fps all used to give 30).
func TestHubReaderRate(t *testing.T) {
	const capFPS = 60
	capPeriod := time.Second / capFPS
	for _, want := range []int{50, 40, 30, 26, 20, 17, 10} {
		s := &hubSub{fps: want}
		t0 := time.Unix(100, 0)
		got := 0
		for i := 0; i < capFPS*10; i++ {
			// The capture's frames come with a little jitter.
			jitter := time.Duration(i%3-1) * time.Millisecond
			if s.take(t0.Add(time.Duration(i)*capPeriod+jitter), capPeriod) {
				got++
			}
		}
		if got < want*10*97/100 || got > want*10*103/100 {
			t.Errorf("reader at %d fps got %d frames in 10 s of a %d fps capture", want, got, capFPS)
		}
	}
}
