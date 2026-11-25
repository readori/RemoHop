// Package nowindow starts helper programs (ffmpeg, sc.exe, net.exe, route,
// netsh) without a console window on Windows.
//
// The host runs without a console of its own (as a service, from the
// desktop app or from quick support), so Windows gives every console
// program it starts a new console window. On Windows 11 that is a Windows
// Terminal window, which also ignores the SW_HIDE that HideWindow asks for:
// the user sees a terminal titled ffmpeg pop up, and again each time the
// screen capture restarts. CREATE_NO_WINDOW starts the program with no
// console at all; its output still goes to the pipes the caller set.
package nowindow

import "os/exec"

// Set makes cmd start without a console window. It keeps whatever else
// cmd.SysProcAttr already asks for and does nothing outside Windows. It
// returns cmd, so it can wrap exec.Command.
func Set(cmd *exec.Cmd) *exec.Cmd {
	set(cmd)
	return cmd
}
