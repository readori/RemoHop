//go:build unix

package xfer

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// lchmod changes the mode of p without following a symlink. A symlink has
// no mode of its own to set, so it is left alone.
func lchmod(p string, perm fs.FileMode) error {
	err := unix.Fchmodat(unix.AT_FDCWD, p, rfs.ModeToUnix(perm)&0o7777, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil || !(errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS)) {
		return wrapPath("chmod", p, err)
	}
	// No fchmodat2 (Linux < 6.6), or p is a symlink.
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil
	}
	return os.Chmod(p, perm)
}

// lchtimes sets the modification (and access) time of p itself, even if
// it is a symlink.
func lchtimes(p string, mtime time.Time) error {
	ts := unix.NsecToTimespec(mtime.UnixNano())
	return wrapPath("utimes", p, unix.UtimesNanoAt(unix.AT_FDCWD, p, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW))
}

func wrapPath(op, p string, err error) error {
	if err == nil {
		return nil
	}
	return &fs.PathError{Op: op, Path: p, Err: err}
}

// openPartialLocal opens or creates a partial download file without
// following a symlink, and locks it (EBUSY if another transfer holds it).
func openPartialLocal(p string) (*os.File, error) {
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, unix.EINVAL // a symlink sits there: do not resume
		}
		return nil, err
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, unix.EINVAL
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, unix.EBUSY
		}
		return nil, err
	}
	return f, nil
}
