//go:build !unix && !windows

package config

import (
	"errors"
	"os"
)

// tryLock on platforms without a file-locking call falls back to an
// exclusively created file, removed on release.
func tryLock(lockPath string) (unlock func(), busy bool, err error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, errors.Is(err, os.ErrExist), err
	}
	f.Close()
	return func() { os.Remove(lockPath) }, false, nil
}
