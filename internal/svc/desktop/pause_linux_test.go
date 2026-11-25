package desktop

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// blockableFFmpeg puts an ffmpeg in front of the real one that exits at once,
// delivering nothing, while the returned file exists, the way ddagrab does
// while Windows shows the secure desktop.
func blockableFFmpeg(t *testing.T) (block func(on bool)) {
	t.Helper()
	real, err := ffmpegPath()
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "blocked")
	wrapper := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nif [ -e '" + flag + "' ]; then echo 'AcquireNextFrame failed' >&2; exit 1; fi\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOHOP_FFMPEG", wrapper)
	return func(on bool) {
		if on {
			os.WriteFile(flag, nil, 0o600)
		} else {
			os.Remove(flag)
		}
	}
}

// While the screen cannot be captured (Windows' secure desktop: a UAC
// prompt, the lock screen, Ctrl+Alt+Del) the session goes on: the host
// says why in its stats and the picture resumes when the screen is back.
// The session used to end with "screen capture/encoding failed" within a
// second or two, and to keep later sessions off the GPU chain for two
// minutes.
func TestSecureDesktopPausesXvfb(t *testing.T) {
	const w, h = 320, 240
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	block := blockableFFmpeg(t)
	var secure atomic.Bool
	old := secureDesktop
	secureDesktop = func() string {
		if secure.Load() {
			return "secure desktop shown"
		}
		return ""
	}
	t.Cleanup(func() { secureDesktop = old })
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	wait := func(what string, d time.Duration, ok func(Event) bool) {
		t.Helper()
		end := time.Now().Add(d)
		for time.Now().Before(end) {
			ev, err := sess.Next(ctx)
			if err != nil {
				t.Fatalf("waiting for %s: session ended: %v", what, err)
			}
			if ok(ev) {
				return
			}
		}
		t.Fatalf("no %s within %v", what, d)
	}
	wait("first frames", 10*time.Second, func(ev Event) bool { return ev.Frame != nil && ev.Frame.Seq > 10 })
	// The secure desktop comes up: the running capture loses the screen
	// (here: the next encoder cannot start) for longer than any blind retry
	// would wait.
	secure.Store(true)
	block(true)
	sess.Configure(Config{Bitrate: 900_000}) // any restart

	wait("a paused state in the stats", 10*time.Second, func(ev Event) bool { return ev.Stats != nil && ev.Stats.Paused == "secure desktop shown" })
	time.Sleep(12 * time.Second)
	secure.Store(false)
	block(false)
	wait("frames again", 15*time.Second, func(ev Event) bool { return ev.Frame != nil && ev.Frame.Key })
	wait("stats without the paused state", 5*time.Second, func(ev Event) bool { return ev.Stats != nil && ev.Stats.Paused == "" })
}

// A screen that cannot be captured for a while for no reason the platform
// can name is waited for a few times before the session gives up.
func TestBlindRetriesXvfb(t *testing.T) {
	const w, h = 320, 240
	x := startXvfb(t, w, h)
	animate(t, x, w, h)
	block := blockableFFmpeg(t)
	client, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess, err := Open(ctx, client, Options{FPS: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	for {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Frame != nil {
			break
		}
	}
	block(true)
	sess.Configure(Config{Bitrate: 900_000})
	time.AfterFunc(2*time.Second, func() { block(false) })
	end := time.Now().Add(15 * time.Second)
	keys := 0
	for time.Now().Before(end) {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("session ended: %v", err)
		}
		if ev.Frame != nil && ev.Frame.Key {
			keys++
		}
	}
	if keys == 0 {
		t.Fatal("no key frame after the screen came back")
	}
	// For good: the session ends, saying why.
	block(true)
	sess.Configure(Config{Bitrate: 800_000})
	end = time.Now().Add(40 * time.Second)
	for time.Now().Before(end) {
		if _, err := sess.Next(ctx); err != nil {
			t.Logf("ended: %v", err)
			return
		}
	}
	t.Fatal("a screen that never comes back kept the session open")
}
