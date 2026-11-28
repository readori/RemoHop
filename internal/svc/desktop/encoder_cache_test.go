package desktop

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// countingFFmpeg is a fake ffmpeg that lists listed, encodes with works
// only, and writes a line to the returned log for every run.
func countingFFmpeg(t *testing.T, dir string, listed, works []string) (ffmpeg, runs string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	runs = filepath.Join(dir, "runs")
	var list strings.Builder
	for _, e := range listed {
		list.WriteString(" V....D " + e + " some encoder\n")
	}
	script := "#!/bin/sh\necho run >> '" + runs + "'\n" +
		"case \"$*\" in *-encoders*) cat <<'LIST'\nEncoders:\n ------\n" + list.String() + "LIST\nexit 0;; esac\n" +
		"enc=; prev=\nfor a in \"$@\"; do [ \"$prev\" = -c:v ] && enc=$a; prev=$a; done\n" +
		"case \" " + strings.Join(works, " ") + " \" in *\" $enc \"*) ;; *) echo \"$enc: cannot initialise\" >&2; exit 1;; esac\n" +
		"exit 0\n"
	ffmpeg = filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpeg, runs
}

func countRuns(runs string) int {
	b, _ := os.ReadFile(runs)
	return strings.Count(string(b), "run")
}

// ageCaches makes everything learnt about encoders look d older.
func ageCaches(d time.Duration) {
	probeMu.Lock()
	for k, l := range listCache {
		l.at = l.at.Add(-d)
		listCache[k] = l
	}
	for k, p := range probeCache {
		p.at = p.at.Add(-d)
		probeCache[k] = p
	}
	probeMu.Unlock()
}

// A session uses what earlier ones found, failed probes included, however
// old, while ffmpeg is the same binary; failures are tried again in the
// background, and a different binary is probed afresh.
func TestEncoderCacheFollowsBinary(t *testing.T) {
	dir := t.TempDir()
	ffmpeg, runs := countingFFmpeg(t, dir, []string{"h264_nvenc", "libx264"}, []string{"libx264"})
	ctx := context.Background()
	req := encRequest{codec: codecH264}
	if e, err := selectEncoder(ctx, ffmpeg, req); err != nil || e.name != "libx264" {
		t.Fatalf("first choice %v %v", e, err)
	}
	first := countRuns(runs)
	if first != 3 { // the listing, h264_nvenc, libx264
		t.Fatalf("%d runs of ffmpeg for the first session", first)
	}
	ageCaches(3 * failedProbeLife)
	if e, err := selectEncoder(ctx, ffmpeg, req); err != nil || e.name != "libx264" {
		t.Fatalf("second choice %v %v", e, err)
	}
	if n := countRuns(runs); n != first {
		t.Fatalf("a later session ran ffmpeg %d more times", n-first)
	}

	// After a session has started, the old failure is tried again.
	defer func(old time.Duration) { refreshDelay = old }(refreshDelay)
	refreshDelay = 0
	for refreshing.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	refreshProbes(ffmpeg)
	deadline := time.Now().Add(10 * time.Second)
	for refreshing.Load() || countRuns(runs) != first+1 {
		if time.Now().After(deadline) {
			t.Fatalf("background refresh: %d runs, want 1", countRuns(runs)-first)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A new ffmpeg (here: one whose NVENC works) is listed and probed
	// again.
	os.Remove(runs)
	ffmpeg, runs = countingFFmpeg(t, dir, []string{"h264_nvenc", "libx264"}, []string{"h264_nvenc", "libx264"})
	if e, err := selectEncoder(ctx, ffmpeg, req); err != nil || e.name != "h264_nvenc" {
		t.Fatalf("after ffmpeg changed: %v %v", e, err)
	}
	if n := countRuns(runs); n != 2 { // the listing, h264_nvenc
		t.Fatalf("%d runs of the new ffmpeg", n)
	}
}
