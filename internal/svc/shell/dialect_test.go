package shell

import (
	"testing"

	"github.com/readori/RemoHop/internal/grant"
)

func TestDialectOf(t *testing.T) {
	for sh, want := range map[string]grant.Dialect{
		"/bin/sh": grant.DialectPOSIX, "/usr/bin/bash": grant.DialectPOSIX, "/bin/zsh": grant.DialectPOSIX,
		"/usr/bin/fish": grant.DialectPOSIX, "/usr/bin/nu": grant.DialectUnknown, "/bin/tcsh": grant.DialectUnknown,
		"cmd.exe": grant.DialectCmd, "CMD.EXE": grant.DialectCmd, "pwsh.exe": grant.DialectPowerShell,
		"powershell.exe": grant.DialectPowerShell, "bash.exe": grant.DialectPOSIX,
	} {
		if got := dialectOf(sh); got != want {
			t.Errorf("dialectOf(%q) = %d, want %d", sh, got, want)
		}
	}
	// The grant check asks this package about the host's actual shell.
	if d, sh := grant.HostShell(); sh != userShell() || d != dialectOf(sh) {
		t.Errorf("grant.HostShell() = %d %q, userShell %q", d, sh, userShell())
	}
}
