//go:build !windows

package desktop

func processElevated() bool { return false }
