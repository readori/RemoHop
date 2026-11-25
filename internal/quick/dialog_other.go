//go:build !windows && !darwin

package quick

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// nativeAsk asks in a desktop dialog with Deny as the safe answer, using
// the first tool there is: zenity (GNOME and most GTK desktops), kdialog
// (KDE), then xmessage (any X11 session, also through XWayland). Where none
// works, the page is the only place to answer, so a desktop notification
// (notify-send) at least says that someone is asking.
func nativeAsk(ctx context.Context, lang string, r Request, timeout time.Duration) (allow, ok bool) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false, false
	}
	text, title := askText(lang, r), msg(lang, "ask_title")
	for _, ask := range []func(context.Context, string, string, string, time.Duration) (bool, bool, bool){askZenity, askKDialog, askXMessage} {
		allow, ok, tried := ask(ctx, lang, title, text, timeout)
		if ctx.Err() != nil {
			return false, false
		}
		if ok || tried {
			return allow, ok
		}
	}
	notify(title, text)
	return false, false
}

// Each ask returns the answer, whether there was one, and whether the tool
// ran at all (a tool that ran and timed out means: no answer, do not try
// the next one).

// askZenity: exit status 0 is Allow, 1 is Deny or the window being closed,
// 5 is the timeout. --no-markup: the text contains a name chosen by the
// other side.
func askZenity(ctx context.Context, lang, title, text string, timeout time.Duration) (bool, bool, bool) {
	zenity, err := exec.LookPath("zenity")
	if err != nil {
		return false, false, false
	}
	err = exec.CommandContext(ctx, zenity, "--question", "--no-markup", "--default-cancel", "--width=420",
		"--title="+title, "--text="+text,
		"--ok-label="+msg(lang, "allow"), "--cancel-label="+msg(lang, "deny"),
		"--timeout="+strconv.Itoa(int(timeout/time.Second))).Run()
	return exitAnswer(err)
}

// askKDialog: a warning with Allow and Deny (Deny, the "no" button, is the
// default of a warning). kdialog has no timeout of its own.
func askKDialog(ctx context.Context, lang, title, text string, timeout time.Duration) (bool, bool, bool) {
	kd, err := exec.LookPath("kdialog")
	if err != nil {
		return false, false, false
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = exec.CommandContext(tctx, kd, "--title", title, "--yes-label", msg(lang, "allow"), "--no-label", msg(lang, "deny"),
		"--warningyesno", text).Run()
	if tctx.Err() != nil {
		return false, false, true
	}
	return exitAnswer(err)
}

// askXMessage: plain X11, present wherever X is. A button's value is the
// exit status; the timeout exits with 0, which is neither button.
func askXMessage(ctx context.Context, lang, title, text string, timeout time.Duration) (bool, bool, bool) {
	xm, err := exec.LookPath("xmessage")
	if err != nil || os.Getenv("DISPLAY") == "" {
		return false, false, false
	}
	deny, allow := msg(lang, "deny"), msg(lang, "allow")
	err = exec.CommandContext(ctx, xm, "-center", "-title", title, "-buttons", deny+":10,"+allow+":11", "-default", deny,
		"-timeout", strconv.Itoa(int(timeout/time.Second)), text).Run()
	if ee, isExit := errors.AsType[*exec.ExitError](err); isExit {
		switch ee.ExitCode() {
		case 11:
			return true, true, true
		case 10:
			return false, true, true
		case 1:
			return false, false, false // could not open the display: try what is left
		}
	}
	return false, false, err == nil // 0: timed out
}

// exitAnswer reads the usual convention: 0 yes, 1 no; anything else (the
// tool could not show its window) is no answer, and the next tool may try.
func exitAnswer(err error) (allow, ok, tried bool) {
	if err == nil {
		return true, true, true
	}
	if ee, isExit := errors.AsType[*exec.ExitError](err); isExit {
		switch ee.ExitCode() {
		case 1:
			return false, true, true
		case 5: // zenity's timeout
			return false, false, true
		}
	}
	return false, false, false
}

// notify draws attention to the page when no dialog can be shown.
func notify(title, text string) {
	ns, err := exec.LookPath("notify-send")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exec.CommandContext(ctx, ns, "--urgency=critical", "--app-name=RemoHop", "--", title, text).Run()
}

// nativeError shows the text in a dialog where a tool for one is installed;
// it is always printed to the terminal as well.
func nativeError(lang, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	title := msg(lang, "error_title")
	if zenity, err := exec.LookPath("zenity"); err == nil {
		exec.CommandContext(ctx, zenity, "--error", "--no-markup", "--width=420", "--title="+title, "--text="+text).Run()
	} else if kd, err := exec.LookPath("kdialog"); err == nil {
		exec.CommandContext(ctx, kd, "--title", title, "--error", text).Run()
	} else if xm, err := exec.LookPath("xmessage"); err == nil && os.Getenv("DISPLAY") != "" {
		exec.CommandContext(ctx, xm, "-center", "-title", title, text).Run()
	}
}
