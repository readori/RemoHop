//go:build !darwin

package ffmpeg

// unquarantine has nothing to do outside macOS.
func unquarantine(string) error { return nil }
