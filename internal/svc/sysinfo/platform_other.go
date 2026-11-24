//go:build !linux && !darwin && !windows

package sysinfo

// Platforms without a probe report only the portable fields set by collect
// (hostname, OS, architecture, cores, user, time).
func platform(i *Info) {}
