//go:build !linux && !windows

package shell

import (
	"errors"
	"syscall"
)

// procStart has no portable source outside Linux; jobs fall back to a PID
// liveness check.
func procStart(pid int) uint64 { return 0 }

// procAlive reports whether pid exists. Without /proc a reused PID cannot be
// told apart from the original job.
func procAlive(pid int, _ uint64) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
