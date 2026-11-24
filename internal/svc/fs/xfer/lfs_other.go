//go:build !unix

package xfer

import (
	"io/fs"
	"os"
	"syscall"
	"time"
)

// lchmod changes the mode of p, leaving a symlink (which it would follow)
// alone.
func lchmod(p string, perm fs.FileMode) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	return os.Chmod(p, perm)
}

// lchtimes sets the modification time of p, leaving a symlink alone.
func lchtimes(p string, mtime time.Time) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	return os.Chtimes(p, mtime, mtime)
}

// openPartialLocal: resumable downloads need O_NOFOLLOW and file locks,
// which this platform lacks here; transfers simply do not resume.
func openPartialLocal(string) (*os.File, error) { return nil, syscall.ENOTSUP }
