package desktop

import (
	"testing"
	"time"
)

// The window follows the path's floor; a stall on a path that was keeping
// up is not a queue, acks that grow against the floor are.
func TestAckClock(t *testing.T) {
	var a ackClock
	t0 := time.Unix(1000, 0)
	period := time.Second / 60
	if w := a.window(t0); w != startWindow {
		t.Fatalf("window before any ack %v", w)
	}
	// 450 ms round trips at 60 fps for 3 s.
	now := t0
	var seq uint32
	for i := 0; i < 180; i++ {
		seq++
		a.onSend(seq, now)
		a.onAck(seq, now.Add(450*time.Millisecond))
		now = now.Add(period)
	}
	now = now.Add(450 * time.Millisecond)
	if w := a.window(now); w != 600*time.Millisecond {
		t.Errorf("window on a 450 ms path %v, want 600 ms", w)
	}
	if a.queueing(now, period) {
		t.Error("a long, steady path counted as a queue")
	}
	// A stall: nothing is acked for a while. The round trips measured
	// before it were short, so it is not a queue.
	if a.queueing(now.Add(time.Second), period) || a.sustainedQueue(now.Add(time.Second), period) {
		t.Error("a stall counted as a queue")
	}
	// Round trips growing by 10 ms a frame: a queue.
	rtt := 450 * time.Millisecond
	for i := 0; i < 40; i++ {
		seq++
		a.onSend(seq, now)
		rtt += 10 * time.Millisecond
		a.onAck(seq, now.Add(rtt))
		now = now.Add(period)
	}
	if !a.queueing(now, period) {
		t.Errorf("growing round trips (srtt %v) not seen as a queue", a.srtt)
	}
	// Short paths keep the minimum window, and the cap holds on long ones.
	var b ackClock
	b.onSend(1, t0)
	b.onAck(1, t0.Add(20*time.Millisecond))
	if w := b.window(t0); w != window {
		t.Errorf("short path window %v", w)
	}
	b.onSend(2, t0)
	b.onAck(2, t0.Add(5*time.Second))
	var c ackClock
	c.onSend(1, t0)
	c.onAck(1, t0.Add(5*time.Second))
	if w := c.window(t0.Add(5 * time.Second)); w != maxWindow {
		t.Errorf("window on a 5 s path %v, want the cap", w)
	}
	// The floor forgets samples older than rttMinSpan.
	if f := b.floor(t0.Add(rttMinSpan + 6*time.Second)); f != 0 {
		t.Errorf("floor kept samples older than %v: %v", rttMinSpan, f)
	}
}

// On a link narrower than the target (it carries 80% of it), automatic
// bitrate settles just below what it carries instead of probing past it
// every 15-30 s: each probe was a restart, a key frame and a dropped burst.
func TestBitrateProbeHoldsBelowCeiling(t *testing.T) {
	t0 := time.Unix(10000, 0)
	h := &hostSession{opts: Options{FPS: 30, Scale: 1}, fps: 30, start: t0.Add(-time.Hour),
		spec: captureSpec{width: 1920, height: 1080}}
	target := h.targetBitrate()
	h.bitrate = target
	capacity := target * 8 / 10
	congestions, restarts := 0, 0
	var below time.Duration
	for now := t0; now.Before(t0.Add(10 * time.Minute)); now = now.Add(100 * time.Millisecond) {
		if h.bitrate > capacity && now.Sub(h.lastRestart) > 3*time.Second {
			congestions++
			h.onCongestion(now, true)
			h.scheduleRestart(now)
		}
		h.adapt(now)
		if !h.restartAt.IsZero() && !now.Before(h.restartAt) {
			restarts++
			h.lastRestart, h.restartAt = now, time.Time{}
		}
		if h.bitrate < capacity*7/10 {
			below += 100 * time.Millisecond
		}
	}
	t.Logf("10 minutes on a link carrying 80%% of the target: %d congestions, %d restarts, %v well below capacity, final bitrate %.0f%% of the target",
		congestions, restarts, below, 100*float64(h.bitrate)/float64(target))
	// Stepping by 1.25 every 15 s regardless: 23 congestions and 58
	// restarts in this simulation.
	if congestions > 6 {
		t.Errorf("%d congestions in 10 minutes", congestions)
	}
	if below > time.Minute {
		t.Errorf("spent %v below 70%% of what the link carries", below)
	}
}
