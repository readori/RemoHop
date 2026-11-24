//go:build unix && !linux

package desktop

import "syscall"

// Only Linux has a parent-death signal; elsewhere ffmpeg is killed by the
// session teardown, and exits on its own when its stdout pipe breaks.
func setDeathSignal(*syscall.SysProcAttr) {}
