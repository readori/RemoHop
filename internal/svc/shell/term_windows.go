package shell

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// sizeChanges polls on Windows, which has no SIGWINCH; watchSize re-reads
// the size on every tick and reports only actual changes.
func sizeChanges() (<-chan time.Time, func()) {
	t := time.NewTicker(250 * time.Millisecond)
	return t.C, t.Stop
}

func withHangup(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

// enableVTOutput makes the console interpret the escape sequences the
// remote terminal sends (colours, cursor movement), as a unix terminal
// would, and leaves line wrapping to them. The returned function restores
// the previous mode.
func enableVTOutput(out *os.File) (restore func()) {
	h := windows.Handle(out.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return func() {}
	}
	if windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING|windows.DISABLE_NEWLINE_AUTO_RETURN) != nil {
		// Older consoles reject DISABLE_NEWLINE_AUTO_RETURN; VT alone will do.
		windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING)
	}
	return func() { windows.SetConsoleMode(h, mode) }
}
