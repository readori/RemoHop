package desktop

import (
	"context"
	"fmt"
	"os/exec"
)

// Controls a macOS host cannot offer, and why. Typing works once the host
// has the Accessibility permission (without it every input control is
// reported with the permission error instead).
var platformUnsupported = map[string]string{
	ControlCtrlAltDel:  "macOS has no Ctrl+Alt+Del; send Force Quit (cmd+option+Esc) as keys instead",
	ControlBlockInput:  "not implemented on macOS hosts",
	ControlBlackout:    "not implemented on macOS hosts",
	ControlAllMonitors: allMonitorsMac,
}

// allMonitorsMac says why macOS has no all-monitors view: AVFoundation
// captures one display per device, and a second capture running next to the
// first never delivers a frame (the reason for the shared capture, see
// capture_hub.go), so one picture of several displays would have to be
// composed from separate captures, which has not been built or tested.
const allMonitorsMac = "not available on macOS hosts: AVFoundation captures one display at a time; choose a single monitor"

// lockScreen puts the displays to sleep, which locks the session when the
// user's settings require a password immediately after sleep (the default
// on current macOS); there is no supported command that locks
// unconditionally. Not verified on real hardware.
func lockScreen(ctx context.Context, _ captureSpec) error {
	if out, err := exec.CommandContext(ctx, "pmset", "displaysleepnow").CombinedOutput(); err != nil {
		return fmt.Errorf("pmset displaysleepnow: %s", lastLine(string(out), err.Error()))
	}
	return nil
}
