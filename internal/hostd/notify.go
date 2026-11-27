package hostd

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// markupEscaper escapes the characters notification markup gives meaning.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// notifyDesktop shows a desktop notification, best effort: notify-send
// where it is installed (Linux and BSD desktops), osascript on macOS.
// Elsewhere, or without a desktop session, nothing is shown and the owner
// relies on `remohop host approve` (or the terminal running `remohop host`). Text
// is passed as arguments, never through a shell or a script. A variable so
// tests can silence it.
var notifyDesktop = func(title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		exec.CommandContext(ctx, "osascript",
			"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run",
			title, body).Run()
	case "windows", "android", "ios":
	default:
		if p, err := exec.LookPath("notify-send"); err == nil {
			// Many notification daemons read the body as markup, which
			// could hide part of it.
			body = markupEscaper.Replace(body)
			exec.CommandContext(ctx, p, "--app-name=RemoHop", "--urgency=critical", "--", title, body).Run()
		}
	}
}
