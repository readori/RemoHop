//go:build !windows

package ffmpeg

// trustedCopy: only the Windows service, which runs as LocalSystem with a
// directory of its own, needs to check its copy (trust_windows.go). A
// macOS or Linux service started by RemoHop's desktop app runs as the
// user, with the user's directory.
func trustedCopy(string) bool { return true }
