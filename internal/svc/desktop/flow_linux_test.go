package desktop

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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

// A key frame on demand costs no gap: the replacement encoder starts while
// the running one keeps streaming (make before break). Stopping first left
// the viewer 110-200 ms without a frame on every request.
func TestKeyframeWithoutGapXvfb(t *testing.T) {
	const w, h = 640, 360
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	start := time.Now()
	var last, requested time.Time
	var gaps []time.Duration
	var maxGap time.Duration
	nextReq := start.Add(4 * time.Second)
	for time.Since(start) < 16*time.Second {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f := ev.Frame
		if f == nil {
			continue
		}
		if !requested.IsZero() && !last.IsZero() {
			maxGap = max(maxGap, f.Received.Sub(last))
			if f.Key {
				gaps = append(gaps, maxGap)
				requested = time.Time{}
				nextReq = f.Received.Add(2500 * time.Millisecond)
			}
		}
		if requested.IsZero() && f.Received.After(nextReq) {
			requested, maxGap = f.Received, 0
			sess.RequestKeyframe()
		}
		last = f.Received
	}
	t.Logf("longest gap around each requested key frame: %v", gaps)
	if len(gaps) < 3 {
		t.Fatalf("only %d of the requested key frames came", len(gaps))
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
