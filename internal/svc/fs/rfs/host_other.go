//go:build !unix

package rfs

import (
	"io/fs"
	"os"
	"syscall"
)

func attrFromInfo(fi fs.FileInfo) Attr {
	return Attr{
		Name:  fi.Name(),
		Mode:  ModeToUnix(fi.Mode()),
		Size:  fi.Size(),
		Mtime: fi.ModTime().UnixNano(),
		Nlink: 1,
	}
}

func unlink(p string) error {
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
		return &fs.PathError{Op: "unlink", Path: p, Err: syscall.EISDIR}
	}
	return os.Remove(p)
}

func rmdir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errNotDir
	}
	return os.Remove(p)
}

// canLock: there are no file locks here, so uploads are not resumable (the
// host answers ENOTSUP and clients upload normally).
const canLock = false

func lockFile(*os.File) error { return syscall.ENOTSUP }

func hostIDs() (uid, gid uint32) { return 0, 0 }

// Umask is this process's file creation mask; there is none here.
func Umask() fs.FileMode { return 0 }

func statFS(string) (*StatFS, error) { return nil, syscall.ENOSYS }
