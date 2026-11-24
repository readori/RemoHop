package desktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
)

// The benchmark runs a headless viewer against a host: it opens a session,
// receives video (and optionally audio) for a while and reports frame rate,
// bitrate and latency. It measures from the host's capture clock to the
// moment a frame reaches this process, with the host's clock offset taken
// from the session's ping exchange, so on one machine (or any link with a
// symmetric delay) the numbers are true capture-to-receive latencies. What
// a browser adds on top (decoding and drawing) is the "latency" figure in
// the viewer's Stats overlay, which counts from the same capture time to the
// drawn frame.

// BenchOptions control Bench.
type BenchOptions struct {
	// Duration of the measured period (default 10 s); Warmup before it
	// (default 1.5 s) lets the clock-offset estimate settle and the
	// encoder's start-up key frame pass.
	Duration time.Duration
	Warmup   time.Duration
	// Audio also measures audio forwarding.
	Audio bool
}

// Latency summarises a set of latencies in milliseconds.
type Latency struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean_ms"`
	P50  float64 `json:"p50_ms"`
	P95  float64 `json:"p95_ms"`
	Max  float64 `json:"max_ms"`
}

func summarize(ms []float64) Latency {
	if len(ms) == 0 {
		return Latency{}
	}
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	var sum float64
	for _, v := range s {
		sum += v
	}
	at := func(q float64) float64 { return s[max(0, min(len(s)-1, int(math.Ceil(q*float64(len(s))))-1))] }
	return Latency{N: len(s), Mean: sum / float64(len(s)), P50: at(0.5), P95: at(0.95), Max: s[len(s)-1]}
}

// BenchResult is what Bench measured.
type BenchResult struct {
	Codec    string  `json:"codec"`
	Encoder  string  `json:"encoder"`
	Hardware bool    `json:"hardware"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Seconds  float64 `json:"seconds"`
	// RequestedFPS is the frame rate asked for; FPS what arrived.
	RequestedFPS int     `json:"requested_fps"`
	Frames       int     `json:"frames"`
	KeyFrames    int     `json:"key_frames"`
	FPS          float64 `json:"fps"`
	// BitrateMbps is the video payload received, in megabits per second.
	BitrateMbps float64 `json:"bitrate_mbps"`
	// Latency is capture on the host to receipt here.
	Latency Latency `json:"latency_capture_to_received"`
	// HostPipelineMS is the host's own capture-to-encoded delay (mean of
	// its reports), RTTMS the session's round-trip time.
	HostPipelineMS float64 `json:"host_pipeline_ms"`
	RTTMS          float64 `json:"rtt_ms"`
	HostDropped    uint64  `json:"host_dropped"`
	LocalDrops     uint64  `json:"local_drops"`
	// Restarts counts encoder restarts during the run (a stable run has
	// none beyond start-up).
	Restarts int          `json:"encoder_restarts"`
	Audio    *AudioResult `json:"audio,omitempty"`
}

// AudioResult is the audio half of a benchmark.
type AudioResult struct {
	Source      string  `json:"source"`
	Packets     int     `json:"packets"`
	BitrateKbps float64 `json:"bitrate_kbps"`
	Latency     Latency `json:"latency_capture_to_received"`
	Dropped     uint64  `json:"dropped"`
}

// Bench measures a session with the host on conn.
func Bench(ctx context.Context, conn *peer.Conn, opts Options, bo BenchOptions) (BenchResult, error) {
	if bo.Duration <= 0 {
		bo.Duration = 10 * time.Second
	}
	if bo.Warmup <= 0 {
		bo.Warmup = 1500 * time.Millisecond
	}
	opts.ManualAck = false
	if err := opts.normalize(); err != nil {
		return BenchResult{}, err
	}
	sess, err := Open(ctx, conn, opts)
	if err != nil {
		return BenchResult{}, err
	}
	defer sess.Close()
	var aud *AudioSession
	if bo.Audio {
		if aud, err = OpenAudio(ctx, conn, AudioOptions{}); err != nil {
			return BenchResult{}, err
		}
		defer aud.Close()
	}

	hello := sess.Hello()
	res := BenchResult{Codec: hello.Codec, Encoder: hello.Encoder, Hardware: hello.Hardware,
		Width: hello.Width, Height: hello.Height, RequestedFPS: opts.FPS}
	start := time.Now()
	measureFrom := start.Add(bo.Warmup)
	end := measureFrom.Add(bo.Duration)

	var (
		wg         sync.WaitGroup
		audMu      sync.Mutex
		audLat     []float64
		audPackets int
		audBytes   int
	)
	if aud != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				p, err := aud.Next(ctx)
				if err != nil {
					return
				}
				if p.Received.Before(measureFrom) {
					continue
				}
				if p.Received.After(end) {
					return
				}
				audMu.Lock()
				audPackets++
				audBytes += len(p.Data)
				audLat = append(audLat, float64(p.Received.Sub(sess.HostToLocal(p.Capture)).Microseconds())/1000)
				audMu.Unlock()
			}
		}()
	}

	var lat []float64
	var bytes int
	var pipeline []float64
	var lastStats *HostStats
	var firstStats *HostStats
	var sawFrame bool
	rctx, cancel := context.WithDeadline(ctx, end)
	defer cancel()
	for {
		ev, err := sess.Next(rctx)
		if err != nil {
			if rctx.Err() != nil && ctx.Err() == nil {
				break // the measured period is over
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("the session ended")
			}
			cancel()
			wg.Wait()
			return res, err
		}
		switch {
		case ev.Hello != nil:
			res.Width, res.Height, res.Codec, res.Encoder = ev.Hello.Width, ev.Hello.Height, ev.Hello.Codec, ev.Hello.Encoder
		case ev.Stats != nil:
			if time.Now().After(measureFrom) {
				if firstStats == nil {
					firstStats = ev.Stats
				}
				lastStats = ev.Stats
				if ev.Stats.PipelineMS > 0 {
					pipeline = append(pipeline, ev.Stats.PipelineMS)
				}
			}
		case ev.Frame != nil:
			f := ev.Frame
			sawFrame = true
			if f.Received.Before(measureFrom) {
				continue
			}
			res.Frames++
			bytes += len(f.Data)
			if f.Key {
				res.KeyFrames++
			}
			if !f.Capture.IsZero() {
				lat = append(lat, float64(f.Received.Sub(sess.HostToLocal(f.Capture)).Microseconds())/1000)
			}
		}
	}
	cancel()
	wg.Wait()
	if !sawFrame {
		return res, errors.New("the host sent no video")
	}
	res.Seconds = bo.Duration.Seconds()
	res.FPS = float64(res.Frames) / res.Seconds
	res.BitrateMbps = float64(bytes) * 8 / res.Seconds / 1e6
	res.Latency = summarize(lat)
	if len(pipeline) > 0 {
		var s float64
		for _, v := range pipeline {
			s += v
		}
		res.HostPipelineMS = s / float64(len(pipeline))
	}
	res.RTTMS = float64(sess.RTT().Microseconds()) / 1000
	if lastStats != nil {
		res.HostDropped = lastStats.Dropped
		res.Restarts = lastStats.Restarts
	}
	res.LocalDrops = sess.LocalDrops()
	if aud != nil {
		audMu.Lock()
		res.Audio = &AudioResult{Source: aud.Hello().Source, Packets: audPackets,
			BitrateKbps: float64(audBytes) * 8 / res.Seconds / 1e3, Latency: summarize(audLat), Dropped: aud.Dropped()}
		audMu.Unlock()
	}
	return res, nil
}

// String renders the result for people.
func (r BenchResult) String() string {
	var b strings.Builder
	hw := "CPU"
	if r.Hardware {
		hw = "GPU"
	}
	fmt.Fprintf(&b, "desktop benchmark: %dx%d, %s (%s, %s), %.0f s measured\n", r.Width, r.Height, r.Codec, r.Encoder, hw, r.Seconds)
	fmt.Fprintf(&b, "  frame rate     %.1f fps (requested %d), %d frames, %d key frames\n", r.FPS, r.RequestedFPS, r.Frames, r.KeyFrames)
	fmt.Fprintf(&b, "  video bitrate  %.2f Mbit/s\n", r.BitrateMbps)
	fmt.Fprintf(&b, "  latency        capture to received: %s\n", r.Latency)
	fmt.Fprintf(&b, "  host pipeline  capture to encoded %.1f ms; round trip %.2f ms\n", r.HostPipelineMS, r.RTTMS)
	fmt.Fprintf(&b, "  drops          %d on the host, %d locally; %d encoder restarts\n", r.HostDropped, r.LocalDrops, r.Restarts)
	if a := r.Audio; a != nil {
		fmt.Fprintf(&b, "  audio          %s: %d packets, %.0f kbit/s, capture to received: %s, %d dropped\n", a.Source, a.Packets, a.BitrateKbps, a.Latency, a.Dropped)
	}
	b.WriteString("  A browser adds decoding and drawing; the viewer's Stats overlay shows the full capture-to-display latency.\n")
	return b.String()
}

func (l Latency) String() string {
	if l.N == 0 {
		return "n/a (the host's capture device has no wall-clock timestamps)"
	}
	return fmt.Sprintf("mean %.1f ms, p50 %.1f, p95 %.1f, max %.1f (n=%d)", l.Mean, l.P50, l.P95, l.Max, l.N)
}
