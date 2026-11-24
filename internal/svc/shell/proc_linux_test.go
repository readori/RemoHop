package shell

import (
	"os/exec"
	"testing"
)

// waitNoReap must leave the process a zombie, keeping its PID (and process
// group ID) from being recycled until the caller reaps it.
func TestWaitNoReapLeavesZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 7")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if !waitNoReap(cmd.Process.Pid) {
		t.Fatal("waitNoReap failed")
	}
	if st, _, ok := procStat(cmd.Process.Pid); !ok || st != 'Z' {
		t.Fatalf("process state %q ok=%v, want zombie", st, ok)
	}
	cmd.Wait()
	if cmd.ProcessState.ExitCode() != 7 {
		t.Fatalf("exit code %d", cmd.ProcessState.ExitCode())
	}
}
