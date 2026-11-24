//go:build !windows

package config

import "os"

// MakePrivate restricts dir to the current user where that takes more than
// creating it with mode 0700 and its files with mode 0600, which is what
// every writer in this module does. It is a no-op here; see the Windows
// implementation for why it exists.
func MakePrivate(dir string) error { return nil }

// IsPrivate reports whether path is inaccessible to group and others.
func IsPrivate(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().Perm()&0o077 == 0, nil
}
