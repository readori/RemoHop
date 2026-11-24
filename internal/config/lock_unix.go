//go:build unix

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock attempts to take the lock without waiting. busy reports that
// somebody else holds it.
func tryLock(lockPath string) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
			return nil, true, nil
		}
		return nil, false, err
	}
	// Closing the descriptor releases the lock.
	return func() { f.Close() }, false, nil
}
