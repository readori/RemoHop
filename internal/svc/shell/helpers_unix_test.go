//go:build !windows

package shell

import (
	"os"
	"path/filepath"
)

// setupTestEnv pins a plain POSIX shell and an empty home directory.
func setupTestEnv(dir string) {
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	os.Setenv("HOME", home)
	os.Setenv("SHELL", "/bin/sh")
	os.Unsetenv("REMOHOP_SHELL")
}
