//go:build linux || darwin

package mount

import (
	"fmt"
	"os"
	"path/filepath"
)

// davTarget validates a WebDAV mount target: an existing directory that is
// not already a mountpoint.
func davTarget(local string) (string, error) {
	dir, err := filepath.Abs(local)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not an existing directory", dir)
	}
	if isMounted(dir) {
		return "", fmt.Errorf("%s is already a mountpoint", dir)
	}
	return dir, nil
}

func davMounted(target string) bool { return isMounted(target) }
