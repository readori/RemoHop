package rfs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// downloadsDir is the user's Downloads known folder (which the user may
// have moved), else %USERPROFILE%\Downloads.
func downloadsDir() (string, error) {
	if d, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}
