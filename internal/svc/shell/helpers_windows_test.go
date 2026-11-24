package shell

import "os"

// setupTestEnv leaves the user profile alone (PowerShell needs a real one)
// and lets the host pick its default shell.
func setupTestEnv(string) {
	os.Unsetenv("REMOHOP_SHELL")
}
