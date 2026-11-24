package quick

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// nativeAsk shows a system dialog with Deny (the default) and Allow, and
// reports the answer. It has no answer when the dialog cannot be shown,
// gives up after timeout, or ctx ends first (the dialog then goes away
// with its process).
//
// The text reaches AppleScript as an argument, never as script source: it
// contains a name chosen by the other side.
func nativeAsk(ctx context.Context, lang string, r Request, timeout time.Duration) (allow, ok bool) {
	allowLabel, denyLabel := msg(lang, "allow"), msg(lang, "deny")
	script := []string{
		"-e", "on run argv",
		"-e", "set r to display dialog (item 1 of argv) with title (item 2 of argv) buttons {item 3 of argv, item 4 of argv} " +
			"default button 1 with icon caution giving up after (item 5 of argv as integer)",
		"-e", "if gave up of r then return \"timeout\"",
		"-e", "return button returned of r",
		"-e", "end run",
		"--", askText(lang, r), msg(lang, "ask_title"), denyLabel, allowLabel, strconv.Itoa(int(timeout / time.Second)),
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", script...).Output()
	if err != nil || ctx.Err() != nil {
		return false, false
	}
	switch strings.TrimSpace(string(out)) {
	case allowLabel:
		return true, true
	case denyLabel:
		return false, true
	}
	return false, false
}

// nativeError tells the user that the program cannot go on; an app started
// from Finder has no terminal to print to.
func nativeError(lang, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "display alert (item 1 of argv) message (item 2 of argv) as critical giving up after 110",
		"-e", "end run",
		"--", msg(lang, "error_title"), text).Run()
}
