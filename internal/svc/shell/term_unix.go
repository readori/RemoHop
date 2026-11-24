//go:build !windows

package shell

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// sizeChanges signals on every SIGWINCH.
func sizeChanges() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

// withHangup also ends ctx when the terminal hangs up (SIGHUP), so the
// terminal state is restored and the session detached cleanly rather than
// the process dying mid-way.
func withHangup(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGTERM)
}

// enableVTOutput is a no-op: unix terminals interpret escape sequences.
func enableVTOutput(*os.File) (restore func()) { return func() {} }
