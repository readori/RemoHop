package shell

import (
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/grant"
)

// A grant's command allow-list judges a command line by the host shell's
// quoting rules; tell it which shell that is.
func init() {
	grant.HostShell = func() (grant.Dialect, string) {
		sh := userShell()
		return dialectOf(sh), sh
	}
}

// posixShells are the shells whose quoting matches what the allow-list
// expects of POSIX sh for the simple commands it admits (fish included:
// its single and double quotes group the same way, and its substitutions
// use characters the allow-list refuses).
var posixShells = map[string]bool{
	"sh": true, "bash": true, "rbash": true, "dash": true, "zsh": true, "ksh": true, "ksh93": true,
	"mksh": true, "lksh": true, "pdksh": true, "oksh": true, "loksh": true, "ash": true, "busybox": true,
	"yash": true, "posh": true, "fish": true,
}

// dialectOf classifies a shell by its file name.
func dialectOf(sh string) grant.Dialect {
	switch name := strings.TrimSuffix(strings.ToLower(filepath.Base(sh)), ".exe"); {
	case name == "pwsh" || name == "powershell":
		return grant.DialectPowerShell
	case name == "cmd":
		return grant.DialectCmd
	case posixShells[name]:
		return grant.DialectPOSIX
	}
	return grant.DialectUnknown
}
