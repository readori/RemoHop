//go:build windows

package config

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

// tryLock attempts to take the lock without waiting. busy reports that
// somebody else holds it, or that the lock file could not be opened for a
// reason that passes by itself (a sharing violation, or "access denied"
// from a file an older version is in the middle of deleting).
func tryLock(lockPath string) (unlock func(), busy bool, err error) {
	// os.OpenFile shares the file for reading and writing, so any number
	// of processes can hold a handle; exclusion comes from LockFileEx.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, true, err
		}
		return nil, false, err
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, true, nil
		}
		return nil, false, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
			f.Close()
		})
	}, false, nil
}
