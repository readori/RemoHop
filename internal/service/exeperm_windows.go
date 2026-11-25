//go:build windows

package service

// checkSystemExe is the Unix check for the systemd and launchd backends,
// which are compiled everywhere for `service print` and tests; the Windows
// service copies its binary to Program Files instead (windows.go).
func checkSystemExe(string, Options, string) error { return nil }

// untrustedExe has no Windows meaning (see checkSystemExe).
func untrustedExe(string, map[uint32]bool) string { return "" }
