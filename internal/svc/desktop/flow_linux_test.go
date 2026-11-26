package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// ackedSession runs a session on an animated Xvfb whose acks the caller
// delays with ackAt (receive time and sequence number in, ack time out),
// and returns per-second counts of received frames and the last host stats.
func ackedSession(t *testing.T, fps int, d time.Duration, ackAt func(recv time.Time, seq uint32) time.Time) (perSec []int, st HostStats, hellos int) {
	t.Helper()
	const w, h = 640, 360
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), d+30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: fps, ManualAck: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	start := time.Now()
	perSec = make([]int, int(d/time.Second))
	var mu sync.Mutex
	for time.Since(start) < d {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case ev.Stats != nil:
			st = *ev.Stats
		case ev.Hello != nil:
			hellos++
		case ev.Frame != nil:
			if i := int(time.Since(start) / time.Second); i < len(perSec) {
				perSec[i]++
			}
			seq := ev.Frame.Seq
			mu.Lock()
			at := ackAt(ev.Frame.Received, seq)
			mu.Unlock()
			time.AfterFunc(time.Until(at), func() { sess.Ack(seq) })
		}
	}
	return perSec, st, hellos
}

// A path whose acks take 450 ms (a long relay, a slow cellular link) is
// not congested: the window follows the measured round trip and the
// session keeps its frame rate and bitrate, with no drops or restarts.
// With the fixed 300 ms window this ran at 6 fps and 500 kbit/s, frozen 20
// of 25 seconds.
func TestLongPathKeepsRateXvfb(t *testing.T) {
	perSec, st, _ := ackedSession(t, 30, 8*time.Second, func(recv time.Time, _ uint32) time.Time {
		return recv.Add(450 * time.Millisecond)
	})
	t.Logf("frames per second %v, host %+v", perSec, st)
	low := 0
	for _, n := range perSec[2:] {
		if n < 24 {
			low++
		}
	}
	if low > 1 || st.Restarts > 0 || st.Dropped > 0 || st.AckMS < 400 || st.WindowMS < 550 {
		t.Errorf("a long but healthy path was treated as congested: %v fps, host %+v", perSec, st)
	}
}

// A link narrower than the stream (the viewer takes frames at 12 per
// second while 30 come) builds a queue: acks come back later and later,
// and the host lowers its bitrate or frame rate instead of letting latency
// grow.
func TestQueueLowersRateXvfb(t *testing.T) {
	var next time.Time
	perSec, st, hellos := ackedSession(t, 30, 12*time.Second, func(recv time.Time, _ uint32) time.Time {
		at := recv.Add(30 * time.Millisecond)
		if next.After(at) {
			at = next
		}
		next = at.Add(time.Second / 12)
		return at
	})
	t.Logf("frames per second %v, hellos %d, host %+v", perSec, hellos, st)
	if hellos < 2 && st.Dropped == 0 {
		t.Errorf("a queue building up neither dropped frames nor lowered the rate: host %+v", st)
	}
	if st.InFlight > 30*int(maxWindow/time.Second) {
		t.Errorf("%d frames in flight", st.InFlight)
	}
}

// keyframeRequests runs a session on an animated Xvfb at fps and asks for
// n key frames, each spacing (plus a varying fraction of a second) after
// the last one came, and a varying part of a frame period after a frame,
// as a viewer's requests fall. It returns, per request, the longest gap
// between frames from the request to its key frame and the wait for that
// key frame, and the last host stats.
func keyframeRequests(t *testing.T, fps, n int, spacing time.Duration) (gaps, waits []time.Duration, st HostStats) {
	t.Helper()
	const w, h = 640, 360
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(n)*(spacing+3*time.Second)+30*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: fps, Encoder: os.Getenv("REMOHOP_KEYFRAME_ENCODER")})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	start := time.Now()
	var last, trigger time.Time
	var maxGap time.Duration
	// The request goes out a varying part of a frame period after the
	// frame that triggers it; sentAt is when.
	var mu sync.Mutex
	var sentAt time.Time
	period := time.Second / time.Duration(fps)
	nextReq := start.Add(4 * time.Second)
	for len(waits) < n {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("after %d key frames: %v", len(waits), err)
		}
		if ev.Stats != nil {
			st = *ev.Stats
		}
		f := ev.Frame
		if f == nil {
			continue
		}
		mu.Lock()
		requested := sentAt
		mu.Unlock()
		if !trigger.IsZero() && !last.IsZero() {
			maxGap = max(maxGap, f.Received.Sub(last))
			if f.Key && !requested.IsZero() && f.Received.After(requested) {
				gaps = append(gaps, maxGap)
				waits = append(waits, f.Received.Sub(requested))
				trigger = time.Time{}
				nextReq = f.Received.Add(spacing + time.Duration(len(waits)*137%400)*time.Millisecond)
			}
		}
		if trigger.IsZero() && f.Received.After(nextReq) {
			trigger, maxGap = f.Received, 0
			mu.Lock()
			sentAt = time.Time{}
			mu.Unlock()
			time.AfterFunc(period*time.Duration(len(waits)*7919%1000)/1000, func() {
				mu.Lock()
				sentAt = time.Now()
				mu.Unlock()
				sess.RequestKeyframe()
			})
		}
		last = f.Received
	}
	// The stats that cover the last request.
	for deadline := time.Now().Add(1500 * time.Millisecond); time.Now().Before(deadline); {
		ev, err := sess.Next(ctx)
		if err != nil {
			break
		}
		if ev.Stats != nil {
			st = *ev.Stats
			break
		}
	}
	return gaps, waits, st
}

// quantiles is the median and the 95th percentile of ds.
func quantiles(ds []time.Duration) (median, p95 time.Duration) {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2], s[min(len(s)-1, (len(s)*95+99)/100-1)]
}

// A key frame on demand comes from the running encoder, about a frame
// after the request, with no restart (keyforce.go). Where ffmpeg cannot
// force one, the replacement encoder starts while the running one keeps
// streaming (make before break): that costs no gap either, but 113-130 ms
// of waiting. Stopping first left the viewer 110-200 ms without a frame on
// every request.
func TestKeyframeWithoutGapXvfb(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	forced := keyForceWorks(context.Background(), ffmpeg)
	spacing := 2500 * time.Millisecond
	if forced {
		spacing = 700 * time.Millisecond
	}
	gaps, waits, st := keyframeRequests(t, 30, 6, spacing)
	med, p95 := quantiles(waits)
	t.Logf("longest gap around each requested key frame: %v; request to key frame: %v (median %v, p95 %v); host %+v", gaps, waits, med, p95, st)
	if forced && (st.Restarts > 0 || st.ForcedKeys < len(waits)-1) {
		t.Errorf("key frames on demand took %d restarts (%d forced)", st.Restarts, st.ForcedKeys)
	}
	worst := 0
	for _, g := range gaps {
		if g > 100*time.Millisecond {
			worst++
		}
	}
	if worst > 1 {
		t.Errorf("key frames on demand left gaps %v", gaps)
	}
}

// Where a second capture or encoder cannot start while the first runs (an
// ffmpeg here that refuses to run twice at once, as a GPU at its encoder
// session limit would), a key frame request still gets its key frame: the
// replacement fails, and the session restarts stopping the old encoder
// first.
func TestReplacementFailsFallsBackXvfb(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not installed")
	}
	real, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	const w, h = 320, 240
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nexec flock -n '" + dir + "/lock' '" + real + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOHOP_FFMPEG", wrapper)
	// What is tested is the restart; a forced key frame would need none.
	t.Setenv(forceKeyEnv, "0")
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	start := time.Now()
	keys, frames, asked := 0, 0, false
	for time.Since(start) < 8*time.Second {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("session ended: %v", err)
		}
		if f := ev.Frame; f != nil {
			frames++
			if asked && f.Key {
				keys++
			}
			if !asked && time.Since(start) > 2*time.Second {
				asked = true
				sess.RequestKeyframe()
			}
		}
	}
	t.Logf("%d frames, %d key frames after the request", frames, keys)
	if keys == 0 || frames < 150 {
		t.Errorf("after a failed replacement: %d frames, %d key frames", frames, keys)
	}
}

// An encoder that does not code a forced key frame (here an ffmpeg that
// drops -force_key_frames, standing in for one that ignores it) still
// gets the viewer its key frames: the session notices the one it asked for
// did not come and restarts the encoder for it, and for every later one.
func TestForcedKeyIgnoredFallsBackXvfb(t *testing.T) {
	real, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if !keyForceWorks(context.Background(), real) {
		t.Skip("this ffmpeg cannot force key frames on demand")
	}
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nfor a; do shift; if [ \"$skip\" ]; then skip=; continue; fi\n" +
		"if [ \"$a\" = -force_key_frames ] && [ \"$KF_PROBE\" != 1 ]; then skip=1; continue; fi; set -- \"$@\" \"$a\"; done\n" +
		"exec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOHOP_FFMPEG", wrapper)
	// The capability probe keeps the option: what is tested is an encoder
	// that ignores it in a session.
	t.Setenv("KF_PROBE", "1")
	if !keyForceWorks(context.Background(), wrapper) {
		t.Fatal("the wrapped ffmpeg failed the probe")
	}
	t.Setenv("KF_PROBE", "0")
	gaps, waits, st := keyframeRequests(t, 30, 3, 2500*time.Millisecond)
	t.Logf("gaps %v, request to key frame %v, host %+v", gaps, waits, st)
	if st.Restarts < 3 {
		t.Errorf("%d restarts for 3 key frames the encoder would not force", st.Restarts)
	}
	if _, refused := kfRefused.Load(kfRefusedKey(wrapper, "libx264")); !refused {
		t.Error("the encoder was not recorded as ignoring forced key frames")
	}
}

// TestKeyframeReportXvfb measures the wait from a key frame request to the
// key frame, with key frames forced on the running encoder and with
// restarts, at 30 and 60 fps (REMOHOP_KEYFRAME_REPORT=1; the number of
// requests per case in REMOHOP_KEYFRAME_N, default 25; another encoder than
// the default in REMOHOP_KEYFRAME_ENCODER).
func TestKeyframeReportXvfb(t *testing.T) {
	if os.Getenv("REMOHOP_KEYFRAME_REPORT") == "" {
		t.Skip("set REMOHOP_KEYFRAME_REPORT=1")
	}
	n := 25
	if v, err := strconv.Atoi(os.Getenv("REMOHOP_KEYFRAME_N")); err == nil && v > 0 {
		n = v
	}
	for _, fps := range []int{30, 60} {
		for _, mode := range []string{"forced", "restart"} {
			t.Run(fmt.Sprintf("%s-%dfps", mode, fps), func(t *testing.T) {
				spacing := 700 * time.Millisecond
				if mode == "restart" {
					t.Setenv(forceKeyEnv, "0")
					spacing = 2500 * time.Millisecond
				}
				// The encoders' CPU: they are the children reaped by the
				// time the session is over (Xvfb is reaped after).
				var before, after syscall.Rusage
				syscall.Getrusage(syscall.RUSAGE_CHILDREN, &before)
				began := time.Now()
				gaps, waits, st := keyframeRequests(t, fps, n, spacing)
				wall := time.Since(began)
				time.Sleep(500 * time.Millisecond) // the last encoder is reaped
				syscall.Getrusage(syscall.RUSAGE_CHILDREN, &after)
				cpu := time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano())
				med, p95 := quantiles(waits)
				gmed, gp95 := quantiles(gaps)
				t.Logf("RESULT %s %d fps, %d requests: request to key frame median %v p95 %v max %v; longest frame gap median %v p95 %v; restarts %d, forced %d; encoder CPU %.1f%% of a core",
					mode, fps, len(waits), med.Round(100*time.Microsecond), p95.Round(100*time.Microsecond), slices.Max(waits).Round(100*time.Microsecond),
					gmed.Round(100*time.Microsecond), gp95.Round(100*time.Microsecond), st.Restarts, st.ForcedKeys, 100*cpu.Seconds()/wall.Seconds())
			})
		}
	}
}
