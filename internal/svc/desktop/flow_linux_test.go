package desktop

import (
	"context"
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
