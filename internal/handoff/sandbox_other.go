//go:build !linux

package handoff

// sandboxDir is "" outside Linux: Snap and Flatpak browsers are a Linux
// matter, and browsers elsewhere can read RemoHop's run directory.
func sandboxDir(string) string { return "" }
