//go:build !linux && !darwin

package rfs

import (
	"os"
	"syscall"
	"time"
)

// rename emulates the Linux renameat2 flags: RENAME_NOREPLACE with a
// (racy) existence check, RENAME_EXCHANGE not at all.
func rename(from, to string, flags int) error {
	if flags&RenameExchange != 0 {
		return syscall.ENOTSUP
	}
	if flags&RenameNoReplace != 0 {
		if _, err := os.Lstat(to); err == nil {
			return syscall.EEXIST
		}
	}
	return os.Rename(from, to)
}

// renameIn emulates the flags as rename does, within root r.
func renameIn(r *os.Root, from, to string, flags int) error {
	if flags&RenameExchange != 0 {
		return syscall.ENOTSUP
	}
	if flags&RenameNoReplace != 0 {
		if _, err := r.Lstat(to); err == nil {
			return syscall.EEXIST
		}
	}
	return r.Rename(from, to)
}

// lutimesIn sets the times of rel below r. Like utimes it follows a
// link: there is no portable way to set a link's own times.
func lutimesIn(r *os.Root, rel string, at, mt *int64) error {
	var a, m time.Time
	if at != nil {
		a = time.Unix(0, *at)
	}
	if mt != nil {
		m = time.Unix(0, *mt)
	}
	return r.Chtimes(rel, a, m)
}

// utimes sets the given times (nil = unchanged). It follows symlinks:
// there is no portable way to set a link's own times.
func utimes(p string, at, mt *int64, _ bool) error {
	var a, m time.Time
	if at != nil {
		a = time.Unix(0, *at)
	}
	if mt != nil {
		m = time.Unix(0, *mt)
	}
	return os.Chtimes(p, a, m)
}
