package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Controls an X11 host cannot offer, and why. Neither can be done properly:
// say so rather than pretend.
var platformUnsupported = map[string]string{
	ControlBlockInput: "not supported on X11 hosts: grabbing the keyboard and pointer would also swallow the viewer's own " +
		"injected input, and disabling the devices outlives a crashed host, leaving the machine without keyboard and mouse",
	ControlBlackout: "not supported on X11 hosts: whatever covers the physical screen is also what gets captured, " +
		"and switching the monitor off (DPMS) hides nothing from someone who switches it back on",
}

// lockScreen locks the session shown on the captured display. logind is
// asked first: it reaches the session from outside (a host daemon has no
// session D-Bus), and every major desktop's screen locker listens to it.
// xdg-screensaver is the fallback for sessions logind does not know.
func lockScreen(ctx context.Context, spec captureSpec) error {
	var tried []string
	if path, err := exec.LookPath("loginctl"); err == nil {
		if id := logindSession(ctx, path, spec); id != "" {
			out, err := exec.CommandContext(ctx, path, "lock-session", id).CombinedOutput()
			if err == nil {
				return nil
			}
			tried = append(tried, fmt.Sprintf("loginctl lock-session %s: %s", id, lastLine(string(out), err.Error())))
		} else {
			tried = append(tried, "loginctl: no graphical session for the captured display")
		}
	}
	if path, err := exec.LookPath("xdg-screensaver"); err == nil {
		cmd := exec.CommandContext(ctx, path, "lock")
		cmd.Env = spec.env
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		tried = append(tried, "xdg-screensaver lock: "+lastLine(string(out), err.Error()))
	}
	if len(tried) == 0 {
		return errors.New("cannot lock the screen: the host has neither loginctl nor xdg-screensaver")
	}
	return errors.New("cannot lock the screen: " + strings.Join(tried, "; "))
}

// logindSession finds the logind session that owns the captured X display:
// the one whose Display matches, else the only local X11 session.
func logindSession(ctx context.Context, loginctl string, spec captureSpec) string {
	out, err := exec.CommandContext(ctx, loginctl, "list-sessions", "--no-legend").Output()
	if err != nil {
		return ""
	}
	display := ""
	if spec.x11 != nil {
		display, _, _ = strings.Cut(spec.x11.Name, ".") // ":0.0" -> ":0"
	}
	var x11 []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		id := f[0]
		props, err := exec.CommandContext(ctx, loginctl, "show-session", id, "-p", "Display", "-p", "Type", "-p", "Remote").Output()
		if err != nil {
			continue
		}
		kv := map[string]string{}
		for _, l := range bytes.Split(props, []byte("\n")) {
			if k, v, ok := strings.Cut(string(l), "="); ok {
				kv[k] = strings.TrimSpace(v)
			}
		}
		if display != "" && kv["Display"] == display {
			return id
		}
		if kv["Type"] == "x11" && kv["Remote"] != "yes" {
			x11 = append(x11, id)
		}
	}
	if len(x11) == 1 {
		return x11[0]
	}
	return ""
}
