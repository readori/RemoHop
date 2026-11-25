//go:build !windows && !darwin

package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/config"
)

// Linux and other freedesktop systems: ~/.config/autostart/remohop.desktop
// (XDG Autostart; every desktop's session settings list it).
func autostartFile() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "autostart", "remohop.desktop"), nil
}

// desktopQuote quotes an Exec argument as the Desktop Entry spec asks.
func desktopQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", "$", `\$`, "%", "%%")
	return `"` + r.Replace(s) + `"`
}

func autostartEntry(exe string) string {
	args := []string{desktopQuote(exe)}
	args = append(args, autostartArgs...)
	return "[Desktop Entry]\nType=Application\nName=RemoHop\nExec=" + strings.Join(args, " ") +
		"\nIcon=remohop\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
}

func autostartOn(exe string) (bool, bool) {
	p, err := autostartFile()
	if err != nil {
		return false, false
	}
	b, err := os.ReadFile(p)
	return err == nil && string(b) == autostartEntry(exe), true
}

func writeAutostart(exe string, on bool) error {
	p, err := autostartFile()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return config.WriteFileAtomic(p, []byte(autostartEntry(exe)), 0o644)
}
