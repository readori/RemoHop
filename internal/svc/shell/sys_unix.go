//go:build !windows

package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// userShell is the shell commands run under: $REMOHOP_SHELL (a host-side
// override) or $SHELL when it names an executable, else /bin/sh.
func userShell() string {
	for _, sh := range []string{os.Getenv("REMOHOP_SHELL"), os.Getenv("SHELL")} {
		if !filepath.IsAbs(sh) {
			continue
		}
		if st, err := os.Stat(sh); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return sh
		}
	}
	return "/bin/sh"
}

// commandCmd runs command through the user's shell, like sshd does.
func commandCmd(command string) *exec.Cmd {
	return exec.Command(userShell(), "-c", command)
}

// loginShellCmd starts the user's shell as a login shell (argv[0] prefixed
// with "-"), which is how sshd and login(1) start interactive sessions so
// that profile files are read.
func loginShellCmd() *exec.Cmd {
	sh := userShell()
	cmd := exec.Command(sh)
	cmd.Args[0] = "-" + filepath.Base(sh)
	return cmd
}

// setGroup puts the command in its own process group so that it and
// everything it spawns can be signalled together.
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// setDetached puts a background job in its own session: no controlling
// terminal and no shared process group with the host, so it survives the
// host exiting or being hung up.
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killGroup SIGKILLs the process group led by pid.
func killGroup(pid int) {
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// hangupGroup sends SIGHUP to the process group led by pid, as a terminal
// hangup would.
func hangupGroup(pid int) {
	if pid > 0 {
		syscall.Kill(-pid, syscall.SIGHUP)
	}
}

// signalGroup sends sig to the process group led by pid.
func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	return syscall.Kill(-pid, sig)
}

// exitInfo converts a finished process's state into an exit code, using
// the shell convention 128+n for death by signal n.
func exitInfo(ps *os.ProcessState) (code, sig int, name string) {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		s := ws.Signal()
		return 128 + int(s), int(s), signalName(s)
	}
	return ps.ExitCode(), 0, ""
}

func signalName(s syscall.Signal) string {
	if n := unix.SignalName(s); n != "" {
		return strings.TrimPrefix(n, "SIG")
	}
	return strconv.Itoa(int(s))
}

// parseSignal accepts "TERM", "SIGTERM", "term" or "15".
func parseSignal(s string) (syscall.Signal, error) {
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 || n > 64 {
			return 0, fmt.Errorf("invalid signal %q", s)
		}
		return syscall.Signal(n), nil
	}
	name := strings.ToUpper(s)
	if !strings.HasPrefix(name, "SIG") {
		name = "SIG" + name
	}
	if sig := unix.SignalNum(name); sig != 0 {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", s)
}

// exitShowsSignal: a unix wait status says which signal killed a process.
const exitShowsSignal = true

// pipedEnv is extra environment for commands that run without a terminal;
// unix shells need none.
func pipedEnv() []string { return nil }
