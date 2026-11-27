//go:build windows

package desktop

import "golang.org/x/sys/windows"

// processElevated reports whether the app runs with administrator rights
// (the built-in Administrator account, or started "as administrator").
func processElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }
