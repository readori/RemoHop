//go:build !unix && !windows

package rfs

import (
	"os"
	"path/filepath"
)

func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}
