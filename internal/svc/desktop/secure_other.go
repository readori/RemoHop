//go:build !windows

package desktop

// platformSecureDesktop says why the screen cannot be captured right now, when the
// platform knows ("" otherwise). Only Windows has a secure desktop.
func platformSecureDesktop() string { return "" }

// secureDesktop is platformSecureDesktop; tests replace it.
var secureDesktop = platformSecureDesktop
