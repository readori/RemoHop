//go:build unix

package rfs

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// attrFromInfo converts lstat/stat results to wire attributes.
func attrFromInfo(fi fs.FileInfo) Attr {
	a := Attr{
		Name:  fi.Name(),
		Mode:  ModeToUnix(fi.Mode()),
		Size:  fi.Size(),
		Mtime: fi.ModTime().UnixNano(),
		Nlink: 1,
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		a.Mode = uint32(st.Mode)
		a.Nlink = uint32(st.Nlink)
		a.Uid = st.Uid
		a.Gid = st.Gid
		a.Ino = uint64(st.Ino)
		a.Dev = uint64(st.Dev)
		a.Blocks = int64(st.Blocks)
		a.Atime, a.Ctime = statTimes(st)
	}
	return a
}

// canLock: partial files of resumable uploads are flock'ed.
const canLock = true

// nativeErrorName names OS-specific error numbers; POSIX systems have none
// beyond the common table.
func nativeErrorName(error, syscall.Errno) string { return "" }

func unlink(p string) error {
	if err := syscall.Unlink(p); err != nil {
		// POSIX lets unlink(2) fail with EPERM on a directory, and macOS
		// does; clients are promised EISDIR, as on Linux.
		if err == syscall.EPERM {
			if fi, lerr := os.Lstat(p); lerr == nil && fi.IsDir() {
				err = syscall.EISDIR
			}
		}
		return &fs.PathError{Op: "unlink", Path: p, Err: err}
	}
	return nil
}

func rmdir(p string) error {
	if err := syscall.Rmdir(p); err != nil {
		return &fs.PathError{Op: "rmdir", Path: p, Err: err}
	}
	return nil
}

// lockFile takes an exclusive, non-blocking lock on f for as long as it is
// open.
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return syscall.EBUSY
	}
	return err
}

func hostIDs() (uid, gid uint32) { return uint32(os.Getuid()), uint32(os.Getgid()) }

// processUmask is read once while the package initialises: umask(2) can only
// be read by setting it, which would race with files created concurrently
// later on.
var processUmask = func() fs.FileMode {
	m := syscall.Umask(0)
	syscall.Umask(m)
	return fs.FileMode(m) & fs.ModePerm
}()

// Umask is this process's file creation mask.
func Umask() fs.FileMode { return processUmask }
