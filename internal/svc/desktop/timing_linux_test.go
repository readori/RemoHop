package desktop

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// TestTimingFirstFrame prints how long a 1080p H.264 session takes from
// the request to its first frame, when encoder detection runs on the way
// (as it did on most sessions before its results were kept while ffmpeg
// stays the same binary) and when it does not. It asserts nothing about
// time and runs only on request:
//
//	REMOHOP_BENCH_REPORT=1 go test ./internal/svc/desktop -run TestTimingFirstFrame -v -count=1
func TestTimingFirstFrame(t *testing.T) {
	if os.Getenv("REMOHOP_BENCH_REPORT") == "" {
		t.Skip("set REMOHOP_BENCH_REPORT=1 to print timings")
	}
	x := startXvfb(t, 1920, 1080)
	animate(t, x, 1920, 1080)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	firstFrame := func() time.Duration {
		start := time.Now()
		s, err := Open(ctx, client, Options{FPS: 30, Codec: codecH264})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for {
			ev, err := s.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if ev.Frame != nil {
				return time.Since(start)
			}
		}
	}
	forget := func() {
		probeMu.Lock()
		clear(probeCache)
		clear(listCache)
		probeMu.Unlock()
		kfProbeMu.Lock()
		clear(kfProbeCache)
		kfProbeMu.Unlock()
	}
	for range 3 {
		forget()
		before := firstFrame()
		time.Sleep(300 * time.Millisecond)
		after := firstFrame()
		time.Sleep(300 * time.Millisecond)
		t.Logf("first frame of a 1080p H.264 session: %s with encoder detection (before), %s without (after)",
			before.Round(time.Millisecond), after.Round(time.Millisecond))
	}
}
