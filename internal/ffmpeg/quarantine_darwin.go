package ffmpeg

import (
	"errors"

	"golang.org/x/sys/unix"
)

// unquarantine removes macOS's download quarantine from the program just
// extracted and verified, so that Gatekeeper does not stop it from running
// in the background. Only this one file is touched. (Go's HTTP client does
// not set the attribute, but a copy of RemoHop that was itself quarantined
// can pass it on.)
func unquarantine(path string) error {
	err := unix.Removexattr(path, "com.apple.quarantine")
	if err == nil || errors.Is(err, unix.ENOATTR) {
		return nil
	}
	return err
}
