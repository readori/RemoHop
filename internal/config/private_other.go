//go:build !unix && !windows

package config

import "os"

// MakePrivate is a no-op where there are neither Unix permission bits nor
// Windows access lists to set.
func MakePrivate(dir string) error { return nil }

// IsPrivate reports whether path is inaccessible to group and others.
func IsPrivate(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().Perm()&0o077 == 0, nil
}
