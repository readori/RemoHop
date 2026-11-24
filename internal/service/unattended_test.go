package service

import (
	"runtime"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

// --unattended is a Windows-service option only, never a default.
func TestUnattendedFlag(t *testing.T) {
	if (Options{Role: RoleSignal, Unattended: true}).validate() == nil {
		t.Error("--unattended accepted for a rendezvous server")
	}
	if err := (Options{Unattended: true}).validate(); err != nil {
		t.Errorf("--unattended for the host: %v", err)
	}
	if runtime.GOOS != "windows" {
		b := &systemd{euid: 1000}
		code, _, e := runCLI(t, b, "print", "--exe", "/usr/bin/remohop", "--unattended")
		if code != cli.ExitUsage || !strings.Contains(e, "Windows") {
			t.Errorf("--unattended off Windows: %d %q", code, e)
		}
	}
	// The hidden helper subcommand refuses to run outside the service.
	code, _, e := runCLI(t, &systemd{euid: 1000}, "session-helper")
	if code != cli.ExitError || e == "" {
		t.Errorf("session-helper outside the service: %d %q", code, e)
	}
	if !strings.Contains(usage, "--unattended") || !strings.Contains(usage, "SoftwareSASGeneration") {
		t.Error("the usage does not document --unattended and the Ctrl+Alt+Del policy")
	}
}
