package mobile

import (
	"encoding/json"
	"math"
	"runtime"
	"runtime/debug"
	"testing"
)

func TestMemoryLimitAndStats(t *testing.T) {
	oldLimit := debug.SetMemoryLimit(-1)
	oldGC := debug.SetGCPercent(100)
	oldProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() {
		debug.SetMemoryLimit(oldLimit)
		debug.SetGCPercent(oldGC)
		runtime.GOMAXPROCS(oldProcs)
	})

	SetMemoryLimit(512<<20, 50)
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Fatalf("limit = %d", got)
	}
	if got := debug.SetGCPercent(50); got != 50 {
		t.Fatalf("GOGC = %d", got)
	}
	SetMemoryLimit(0, 0) // leaves both alone
	if got := debug.SetMemoryLimit(-1); got != 512<<20 {
		t.Fatalf("limit changed to %d", got)
	}
	if got := debug.SetGCPercent(50); got != 50 {
		t.Fatalf("GOGC changed to %d", got)
	}

	if prev := SetMaxProcs(2); prev != oldProcs {
		t.Fatalf("previous GOMAXPROCS = %d, want %d", prev, oldProcs)
	}
	if got := SetMaxProcs(0); got != 2 {
		t.Fatalf("GOMAXPROCS = %d", got)
	}

	FreeOSMemory()
	var st map[string]uint64
	if err := json.Unmarshal([]byte(MemoryStatsJSON()), &st); err != nil {
		t.Fatal(err)
	}
	if st["limit"] != 512<<20 || st["total"] == 0 || st["heap"] == 0 || st["goroutines"] == 0 {
		t.Fatalf("stats = %v", st)
	}
	if st["heap"] > st["total"] {
		t.Fatalf("heap %d > total %d", st["heap"], st["total"])
	}

	SetMemoryLimit(math.MaxInt64, 0)
	if err := json.Unmarshal([]byte(MemoryStatsJSON()), &st); err != nil {
		t.Fatal(err)
	}
	if st["limit"] != 0 {
		t.Fatalf("no limit reported as %d", st["limit"])
	}
}
