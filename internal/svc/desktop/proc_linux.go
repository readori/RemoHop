package desktop

import "syscall"

// setDeathSignal makes the kernel kill ffmpeg if the host process dies
// without cleaning up (crash, SIGKILL), so an encoder never outlives
// `remohop host`.
func setDeathSignal(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }
