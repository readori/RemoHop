package desktop

import "time"

// Flow control. The host sends a frame and the viewer acknowledges it once
// decoded; what is unacknowledged is queued somewhere between the two (the
// network, a relay, the gateway, the browser's decoder). The host caps it
// so that a link slower than the bitrate costs dropped frames, not
// growing latency.
//
// The cap used to be a fixed 300 ms of video. In steady state a healthy
// path keeps fps x (ack round trip) frames in flight, so any path whose
// round trip plus decode took longer than 300 ms (a relay on another
// continent, a phone on a cellular link) hit the cap on every frame, which
// counted as congestion: measured with acks delayed 320 ms, 10 fps at 500
// kbit/s, 10.7 of 25 s frozen; with 450 ms, 6 fps and 20 s frozen, never
// recovering.
//
// Now the host measures the ack round trip itself (from sending a frame to
// its ack) and the window is that path's own floor plus a margin:
// max(window, floor + windowSlack), where the floor is the smallest round
// trip of the last rttMinSpan. A path that is merely long gets a window
// that fits it; a queue that builds up (round trips growing beyond the
// floor) still fills the window and is dropped. A full window counts as
// congestion (lower bitrate) only when acks have been coming back late
// against that floor (queueDelay), which is what a link narrower than the
// bitrate looks like; a single stall (a Wi-Fi roam, the phone app briefly
// in the background) leaves the round trips of the frames before it short,
// costs a skip to the next key frame and keeps the bitrate.
// And the host does not wait for the window to fill: round trips that stay
// queueDelay above the floor for queueHold lower the bitrate before any
// frame is dropped.
const (
	// windowSlack is how far past the path's floor acks may lag before
	// the host drops.
	windowSlack = 150 * time.Millisecond
	// maxWindow bounds the window on the longest paths.
	maxWindow = 2 * time.Second
	// startWindow is the window before the first round trip is measured:
	// the viewer is setting up its decoder and decoding the first, large
	// key frame, and the path's length is unknown.
	startWindow = time.Second
	// rttMinSpan is how far back the floor looks; a path that becomes
	// longer for good is followed after this long.
	rttMinSpan = 10 * time.Second
	// queueDelay is how far above the floor the smoothed round trip must be
	// to count as a queue building up (at least this, or three frame
	// periods); queueHold how long it must stay there before the host
	// lowers the bitrate without waiting for the window to fill.
	queueDelay = 100 * time.Millisecond
	queueHold  = 600 * time.Millisecond
	// sentRing is how many send times are kept to match acks against
	// (more than the largest window can hold).
	sentRing = 512
)

// ackClock measures the round trip from sending a frame to its ack.
type ackClock struct {
	sent [sentRing]struct {
		seq uint32
		at  time.Time
	}
	srtt   time.Duration // smoothed, 0 until the first sample
	last   time.Duration // latest sample
	lastAt time.Time
	// floor buckets: the smallest sample of each rttMinSpan/10.
	mins     [10]time.Duration
	minAt    [10]time.Time
	minIdx   int
	samples  int
	highFrom time.Time // since when srtt has been queueDelay above the floor
}

func (a *ackClock) onSend(seq uint32, at time.Time) {
	e := &a.sent[seq%sentRing]
	e.seq, e.at = seq, at
}

// onAck takes the ack of seq (which acknowledges everything before it too).
func (a *ackClock) onAck(seq uint32, now time.Time) {
	e := a.sent[seq%sentRing]
	if e.seq != seq || e.at.IsZero() {
		return
	}
	rtt := now.Sub(e.at)
	if rtt < 0 {
		return
	}
	a.samples++
	a.last, a.lastAt = rtt, now
	if a.srtt == 0 {
		a.srtt = rtt
	} else {
		a.srtt += (rtt - a.srtt) / 8
	}
	bucket := rttMinSpan / time.Duration(len(a.mins))
	cur := a.minIdx
	if a.minAt[cur].IsZero() || now.Sub(a.minAt[cur]) >= bucket {
		cur = (cur + 1) % len(a.mins)
		a.minIdx, a.mins[cur], a.minAt[cur] = cur, rtt, now
	} else if rtt < a.mins[cur] {
		a.mins[cur] = rtt
	}
}

// floor is the smallest round trip of the last rttMinSpan, 0 if none.
func (a *ackClock) floor(now time.Time) time.Duration {
	var m time.Duration
	for i, v := range a.mins {
		if a.minAt[i].IsZero() || now.Sub(a.minAt[i]) > rttMinSpan {
			continue
		}
		if m == 0 || v < m {
			m = v
		}
	}
	return m
}

// window is how much video may be unacknowledged.
func (a *ackClock) window(now time.Time) time.Duration {
	f := a.floor(now)
	if a.samples == 0 || f == 0 {
		return startWindow
	}
	return min(maxWindow, max(window, f+windowSlack))
}

// queueing reports whether acks come back late against the path's floor:
// a queue is building between host and viewer. period is the frame period.
func (a *ackClock) queueing(now time.Time, period time.Duration) bool {
	f := a.floor(now)
	if a.samples < 8 || f == 0 {
		return false
	}
	return a.srtt-f > max(queueDelay, 3*period)
}

// sustainedQueue reports whether queueing has held for queueHold, keeping
// track as it is called (on every tick). Without fresh acks (nothing is
// being sent, or a stall) it says nothing.
func (a *ackClock) sustainedQueue(now time.Time, period time.Duration) bool {
	if now.Sub(a.lastAt) > 250*time.Millisecond || !a.queueing(now, period) {
		a.highFrom = time.Time{}
		return false
	}
	if a.highFrom.IsZero() {
		a.highFrom = now
	}
	return now.Sub(a.highFrom) >= queueHold
}
