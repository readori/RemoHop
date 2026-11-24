package sysinfo

import (
	"math"
	"strconv"
	"strings"
)

// The parsers below turn the text of /etc/os-release, /proc/meminfo and the
// like into values. They are deliberately lenient: a malformed or unexpected
// line is skipped and the field stays at its zero value, so one odd file never
// makes the whole report fail. They must never panic on any input.

// parseOSRelease returns NAME and VERSION_ID from os-release(5) content,
// falling back to VERSION when VERSION_ID is absent (as on some rolling
// distributions). Only the surrounding quotes are removed from values.
func parseOSRelease(data string) (name, version string) {
	var fallback string
	for _, line := range strings.Split(data, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = unquote(val)
		switch strings.TrimSpace(key) {
		case "NAME":
			name = val
		case "VERSION_ID":
			version = val
		case "VERSION":
			fallback = val
		}
	}
	if version == "" {
		version = fallback
	}
	return name, version
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// parseMeminfo returns MemTotal and MemAvailable from /proc/meminfo, in bytes.
// The kernel reports these in kB (really KiB).
func parseMeminfo(data string) (total, available uint64) {
	for _, line := range strings.Split(data, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 || f[1] != "kB" {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil || v > math.MaxUint64/1024 {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemTotal":
			total = v * 1024
		case "MemAvailable":
			available = v * 1024
		}
	}
	return total, available
}

// parseLoadavg returns the 1, 5 and 15 minute load averages from
// /proc/loadavg. Values that are not finite and non-negative are treated as
// absent: encoding/json refuses NaN and Inf, which would break the reply.
func parseLoadavg(data string) (loads [3]float64) {
	f := strings.Fields(data)
	if len(f) < 3 {
		return loads
	}
	for i := range loads {
		loads[i] = parseNonNegative(f[i])
	}
	return loads
}

// parseUptime returns whole seconds from the first field of /proc/uptime.
func parseUptime(data string) int64 {
	f := strings.Fields(data)
	if len(f) == 0 {
		return 0
	}
	v := parseNonNegative(f[0])
	if v >= 1<<62 {
		return 0
	}
	return int64(v)
}

// parseCPUModel returns the first "model name" from /proc/cpuinfo. Architectures
// that do not report one (most ARM kernels) yield "".
func parseCPUModel(data string) string {
	for _, line := range strings.Split(data, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "model name" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func parseNonNegative(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
