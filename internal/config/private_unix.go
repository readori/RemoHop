//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

// MakePrivate makes sure dir is a safe place for this user's key material.
// Files in it are created 0600 by every writer in this module, but that
// does not stop someone who can write to the directory from replacing them
// (a rename needs only write access to the directory), so:
//
//   - a directory of this user's that others may write to is tightened to
//     the owner only (not a sticky directory such as /tmp, which is shared
//     by design);
//   - a directory that belongs to another user (other than root) is
//     refused;
//   - one that everybody may write to and that cannot be fixed is refused.
//
// Group write is accepted when it cannot be removed (the directory is
// root's: a volume shared with a container's group, say). A directory that
// others can only read is left as it is.
func MakePrivate(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	euid := os.Geteuid()
	if int(st.Uid) != euid && st.Uid != 0 {
		return fmt.Errorf("%s belongs to another user (uid %d); use a directory of your own (REMOHOP_CONFIG_DIR)", dir, st.Uid)
	}
	perm := fi.Mode().Perm()
	if perm&0o022 == 0 {
		return nil
	}
	if int(st.Uid) == euid && fi.Mode()&os.ModeSticky == 0 {
		if err := os.Chmod(dir, perm&0o700); err != nil {
			return fmt.Errorf("%s is writable by other users (mode %v) and cannot be made private: %w", dir, perm, err)
		}
		return nil
	}
	if perm&0o002 != 0 {
		return fmt.Errorf("%s is writable by every user (mode %v); use a private directory (REMOHOP_CONFIG_DIR) or chmod 700 it", dir, perm)
	}
	return nil
}

// IsPrivate reports whether path is inaccessible to group and others.
func IsPrivate(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.Mode().Perm()&0o077 == 0, nil
}
