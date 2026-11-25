//go:build !windows

package dpiaware

// Enable does nothing outside Windows.
func Enable() string { return "" }
