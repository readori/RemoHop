//go:build !windows

package shell

import (
	"os/exec"
	"syscall"
	"testing"
)

// waitNoReap must leave the process unreaped, keeping its PID (and process
// group ID) from being recycled until the caller reaps it.
func TestWaitNoReap(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitNoReap(cmd.Process.Pid) {
		t.Fatal("waitNoReap failed")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("process already reaped: %v", err)
	}
	cmd.Wait()
	if cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("exit code %d", cmd.ProcessState.ExitCode())
	}
}
