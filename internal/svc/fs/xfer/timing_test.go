package xfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// Timing tests: they print how long copies take over a session with a
// 40 ms round trip, before and after the changes they cover, and assert
// nothing about time. They run only on request:
//
//	REMOHOP_BENCH_REPORT=1 go test ./internal/svc/fs/xfer -run Timing -v -count=1

func timingClient(t *testing.T) (*rfs.Client, string) {
	t.Helper()
	if os.Getenv("REMOHOP_BENCH_REPORT") == "" {
		t.Skip("set REMOHOP_BENCH_REPORT=1 to print timings")
	}
	home := t.TempDir()
	setHome(t, home)
	conn, _ := peertest.PairOpts(t, peer.HostServices, peertest.Options{Latency: 20 * time.Millisecond})
	c, err := rfs.NewClient(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	return c, home
}

// dirTree makes 15 directories of 20 directories, each holding 3 small
// files: 316 directories with the root.
func dirTree(t *testing.T) string {
	src := filepath.Join(t.TempDir(), "tree")
	for d := range 300 {
		dir := filepath.Join(src, fmt.Sprintf("d%02d", d/20), fmt.Sprintf("e%03d", d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f := range 3 {
			os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d", f)), make([]byte, 2048), 0o644)
		}
	}
	return src
}

// TestTimingDirFixes: every directory a copy creates gets its mode (and
// with -p its time) after its contents, one round trip each. They used to
// run one at a time; now each depth level runs at once.
func TestTimingDirFixes(t *testing.T) {
	c, home := timingClient(t)
	ctx := context.Background()
	src := dirTree(t)
	start := time.Now()
	res, err := Copy(ctx, c, []Path{{Path: src}}, Path{Remote: true, Path: filepath.Join(home, "up")}, CopyOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	after := time.Since(start)
	start = time.Now()
	if _, err := Copy(ctx, c, []Path{{Remote: true, Path: filepath.Join(home, "up")}}, Path{Path: filepath.Join(t.TempDir(), "down")},
		CopyOptions{Recursive: true}); err != nil {
		t.Fatal(err)
	}
	down := time.Since(start)
	// What the fixes alone cost one at a time, as they used to run.
	var dirs []string
	filepath.WalkDir(filepath.Join(home, "up"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	start = time.Now()
	for _, d := range dirs {
		if err := c.Chmod(ctx, d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	serial := time.Since(start)
	t.Logf("cp -r of %d directories, %d files at 40 ms round trip: upload %.2fs, download %.2fs", res.Dirs, res.Files, after.Seconds(), down.Seconds())
	t.Logf("the directory fixes alone, one at a time as before: %.2fs (so the upload took about %.2fs before)",
		serial.Seconds(), after.Seconds()+serial.Seconds())

	start = time.Now()
	if _, err := Sync(ctx, c, Path{Path: src}, Path{Remote: true, Path: filepath.Join(home, "synced")}, SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Logf("first sync of the same tree up: %.2fs", time.Since(start).Seconds())
}

// TestTimingSmallFiles: small files go 48 at a time (they used to go 8 at
// a time, like large ones).
func TestTimingSmallFiles(t *testing.T) {
	c, home := timingClient(t)
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "small")
	os.Mkdir(src, 0o755)
	for i := range 500 {
		os.WriteFile(filepath.Join(src, fmt.Sprintf("f%04d", i)), make([]byte, 4096), 0o644)
	}
	run := func(name string, small int) {
		defer func(old int) { smallParallel = old }(smallParallel)
		smallParallel = small
		up := filepath.Join(home, name)
		start := time.Now()
		if _, err := Copy(ctx, c, []Path{{Path: src}}, Path{Remote: true, Path: up}, CopyOptions{Recursive: true}); err != nil {
			t.Fatal(err)
		}
		u := time.Since(start)
		start = time.Now()
		if _, err := Copy(ctx, c, []Path{{Remote: true, Path: up}}, Path{Path: filepath.Join(t.TempDir(), name)}, CopyOptions{Recursive: true}); err != nil {
			t.Fatal(err)
		}
		t.Logf("500 files of 4 KB at 40 ms round trip, %s: upload %.2fs, download %.2fs", name, u.Seconds(), time.Since(start).Seconds())
	}
	run("before (8 at a time)", 0)
	run("after (48 at a time)", 48)
}

// TestDeepestFirst: every item comes after everything deeper.
func TestDeepestFirst(t *testing.T) {
	var items []int
	for i := range 200 {
		items = append(items, i%7)
	}
	var mu sync.Mutex
	var order []int
	deepestFirst(items, func(d int) int { return d }, 16, func(d int) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, d)
	})
	if len(order) != len(items) {
		t.Fatalf("%d of %d items", len(order), len(items))
	}
	for i := 1; i < len(order); i++ {
		if order[i] > order[i-1] {
			t.Fatalf("depth %d after depth %d", order[i], order[i-1])
		}
	}
}
