package remohopcore

import "github.com/readori/RemoHop/internal/mobile"

// Memory limits for the iOS broadcast upload extension (see
// internal/mobile/memory.go).

// SetMemoryLimit sets the Go soft memory limit (bytes, 0 = unchanged) and,
// if gcPercent is not 0, GOGC.
func SetMemoryLimit(limitBytes int64, gcPercent int) { mobile.SetMemoryLimit(limitBytes, gcPercent) }

// SetMaxProcs sets GOMAXPROCS when n > 0 and returns the previous value.
func SetMaxProcs(n int) int { return mobile.SetMaxProcs(n) }

// FreeOSMemory returns as much memory to the system as possible.
func FreeOSMemory() { mobile.FreeOSMemory() }

// MemoryStatsJSON reports the Go runtime's memory as JSON.
func MemoryStatsJSON() string { return mobile.MemoryStatsJSON() }
