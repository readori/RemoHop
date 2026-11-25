//go:build linux

// Package xvfbtest ends the private X servers tests start. Killed with
// SIGKILL, Xvfb leaves its socket (/tmp/.X11-unix/X<n>) and lock file
// (/tmp/.X<n>-lock) behind: a long-lived machine collected hundreds, and
// tests that pick a free display number then ran out of numbers and
// skipped themselves.
package xvfbtest

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Stop ends the X server cmd runs for display (":<n>"): SIGTERM, so that it
// removes its socket and lock file itself, SIGKILL after three seconds, and
// then removes what it left. It waits for the process; exited, when not
// nil, is closed by whoever already waits for it instead.
func Stop(cmd *exec.Cmd, display string, exited <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if exited == nil {
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		exited = done
	}
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		cmd.Process.Kill()
		<-exited
	}
	Cleanup(display, cmd.Process.Pid)
}

// Cleanup removes the socket and lock file of display that the server with
// process ID pid left behind. A display number is reused as soon as it is
// free, so nothing is removed that belongs to another server: the lock
// file only if it names pid, the socket only with it or, without a lock
// file, if nothing answers on it.
func Cleanup(display string, pid int) {
	n, err := strconv.Atoi(strings.TrimPrefix(display, ":"))
	if err != nil || n < 0 {
		return
	}
	lock := "/tmp/.X" + strconv.Itoa(n) + "-lock"
	sock := "/tmp/.X11-unix/X" + strconv.Itoa(n)
	if b, err := os.ReadFile(lock); err == nil {
		if owner, err := strconv.Atoi(strings.TrimSpace(string(b))); err != nil || owner != pid {
			return
		}
		os.Remove(lock)
		os.Remove(sock)
		return
	}
	if c, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err == nil {
		c.Close()
		return
	}
	os.Remove(sock)
}
