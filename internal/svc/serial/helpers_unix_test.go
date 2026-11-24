//go:build linux || darwin

package serial

import (
	"os/exec"
	"testing"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/wire"
)

func newSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep: %v", err)
	}
	go cmd.Wait() // reap, so a stopped sleeper stops looking alive
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd
}

func configRunDir() (string, error) { return config.SubDir("run") }

func readJSON(r interface{ Read([]byte) (int, error) }, v any) error { return wire.ReadJSON(r, v) }

// exclusiveUnsupported reports platforms where we cannot query TIOCEXCL.
func exclusiveUnsupported() bool { return !exclusiveQueryable }
