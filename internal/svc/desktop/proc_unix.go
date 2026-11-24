//go:build unix

package desktop

import (
	"os/exec"
	"syscall"
)

// startGroup starts cmd in its own process group so killGroup takes down
// ffmpeg together with anything it spawned.
func startGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	setDeathSignal(cmd.SysProcAttr)
	return cmd.Start()
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
