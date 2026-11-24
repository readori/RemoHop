//go:build !linux && !windows && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package shell

// waitNoReap is not available here; callers reap instead and stop
// signalling the process group from then on.
func waitNoReap(int) bool { return false }
