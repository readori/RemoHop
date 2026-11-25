//go:build windows

package service

// checkSystemExe is the Unix check for the systemd and launchd backends,
// which are compiled everywhere for `service print` and tests; the Windows
// service copies its binary to Program Files instead (windows.go).
func checkSystemExe(string, Options, string) error { return nil }
