//go:build !windows && !darwin

package quick

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// nativeAsk asks with zenity where it is installed (most GNOME desktops);
// elsewhere the page is the only place to answer. Exit status 0 is Allow,
// 1 is Deny or the window being closed, 5 is the timeout.
func nativeAsk(ctx context.Context, lang string, r Request, timeout time.Duration) (allow, ok bool) {
	zenity, err := exec.LookPath("zenity")
	if err != nil {
		return false, false
	}
	// --no-markup: the text contains a name chosen by the other side.
	err = exec.CommandContext(ctx, zenity, "--question", "--no-markup", "--default-cancel", "--width=420",
		"--title="+msg(lang, "ask_title"), "--text="+askText(lang, r),
		"--ok-label="+msg(lang, "allow"), "--cancel-label="+msg(lang, "deny"),
		"--timeout="+strconv.Itoa(int(timeout/time.Second))).Run()
	if ctx.Err() != nil {
		return false, false
	}
	if err == nil {
		return true, true
	}
	if ee, isExit := errors.AsType[*exec.ExitError](err); isExit && ee.ExitCode() == 1 {
		return false, true
	}
	return false, false
}

// nativeError shows the text in a dialog if zenity is there; it is always
// printed to the terminal as well.
func nativeError(lang, text string) {
	if zenity, err := exec.LookPath("zenity"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		exec.CommandContext(ctx, zenity, "--error", "--no-markup", "--width=420", "--title="+msg(lang, "error_title"), "--text="+text).Run()
	}
}
