package mobile

// Memory limits for a small process. The iOS broadcast upload extension
// runs the host (hostd and the screen it is fed) under a hard limit of
// about 50 MB for the whole process, of which the Go heap may take only a
// part: the extension sets a soft limit and a lower GC target at start,
// lowers GOMAXPROCS (fewer per-P caches), returns freed memory when the
// system warns, and reports the runtime's figures in its diagnostics.
// The apps themselves do not call these.

import (
	"encoding/json"
	"math"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
)

// SetMemoryLimit sets the Go runtime's soft memory limit in bytes (0 or
// less leaves it unchanged; math.MaxInt64 removes it) and, if gcPercent is
// not 0, the GC target percentage (GOGC; negative turns the proportional
// GC off, leaving only the limit).
func SetMemoryLimit(limitBytes int64, gcPercent int) {
	if limitBytes > 0 {
		debug.SetMemoryLimit(limitBytes)
	}
	if gcPercent != 0 {
		debug.SetGCPercent(gcPercent)
	}
}

// SetMaxProcs sets GOMAXPROCS when n > 0 and returns the previous value.
func SetMaxProcs(n int) int {
	if n <= 0 {
		return runtime.GOMAXPROCS(0)
	}
	return runtime.GOMAXPROCS(n)
}

// FreeOSMemory runs a garbage collection and returns as much memory to the
// system as possible (on the system's memory warning).
func FreeOSMemory() { debug.FreeOSMemory() }

var memSamples = []string{
	"/memory/classes/total:bytes",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/released:bytes",
	"/gc/cycles/total:gc-cycles",
	"/sched/goroutines:goroutines",
}

// MemoryStatsJSON reports the Go runtime's memory as JSON, in bytes:
// "total" (everything the runtime mapped, minus what it released to the
// system), "heap" (heap objects), "gcs", "goroutines" and "limit" (the
// soft limit, 0 for none). It does not stop the world.
func MemoryStatsJSON() string {
	s := make([]metrics.Sample, len(memSamples))
	for i, n := range memSamples {
		s[i].Name = n
	}
	metrics.Read(s)
	v := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	total, released := v(0), v(2)
	if released <= total {
		total -= released
	}
	limit := debug.SetMemoryLimit(-1)
	if limit == math.MaxInt64 || limit < 0 {
		limit = 0
	}
	b, _ := json.Marshal(map[string]uint64{
		"total": total, "heap": v(1), "gcs": v(3), "goroutines": v(4), "limit": uint64(limit),
	})
	return string(b)
}
