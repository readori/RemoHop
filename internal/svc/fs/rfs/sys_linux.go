package rfs

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func statTimes(st *syscall.Stat_t) (atime, ctime int64) {
	return st.Atim.Nano(), st.Ctim.Nano()
}

func statFS(p string) (*StatFS, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(p, &s); err != nil {
		return nil, err
	}
	return statFSFrom(&s), nil
}

func fstatFS(f *os.File) (*StatFS, error) {
	var s unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &s); err != nil {
		return nil, err
	}
	return statFSFrom(&s), nil
}

func statFSFrom(s *unix.Statfs_t) *StatFS {
	return &StatFS{
		Blocks: s.Blocks, Bfree: s.Bfree, Bavail: s.Bavail,
		Files: s.Files, Ffree: s.Ffree,
		Bsize: uint32(s.Bsize), Frsize: uint32(s.Frsize), NameLen: uint32(s.Namelen),
	}
}

func rename(from, to string, flags int) error {
	if flags == 0 {
		return syscall.Rename(from, to)
	}
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, renameFlags(flags))
}

func renameFlags(flags int) uint {
	var f uint
	if flags&RenameNoReplace != 0 {
		f |= unix.RENAME_NOREPLACE
	}
	if flags&RenameExchange != 0 {
		f |= unix.RENAME_EXCHANGE
	}
	return f
}

// renameIn is rename with flags between two paths below root r: the parent
// directories are opened through r and the entries renamed by name in them
// (renameat2 does not follow the final components).
func renameIn(r *os.Root, from, to string, flags int) error {
	return inParents(r, from, to, func(fd1 int, b1 string, fd2 int, b2 string) error {
		return unix.Renameat2(fd1, b1, fd2, b2, renameFlags(flags))
	})
}

// inParents opens the parent directories of a and b through r and calls
// fn with their descriptors and the base names.
func inParents(r *os.Root, a, b string, fn func(fd1 int, b1 string, fd2 int, b2 string) error) error {
	d1, err := r.Open(filepath.Dir(a))
	if err != nil {
		return err
	}
	defer d1.Close()
	d2, err := r.Open(filepath.Dir(b))
	if err != nil {
		return err
	}
	defer d2.Close()
	return fn(int(d1.Fd()), filepath.Base(a), int(d2.Fd()), filepath.Base(b))
}

// utimes sets the given times (nil = unchanged), on the link itself when
// noFollow is set.
func utimes(p string, at, mt *int64, noFollow bool) error {
	return utimesAt(unix.AT_FDCWD, p, at, mt, noFollow)
}

// lutimesIn sets the times of the entry rel below root r without following
// it if it is a link.
func lutimesIn(r *os.Root, rel string, at, mt *int64) error {
	d, err := r.Open(filepath.Dir(rel))
	if err != nil {
		return err
	}
	defer d.Close()
	return utimesAt(int(d.Fd()), filepath.Base(rel), at, mt, true)
}

func utimesAt(dirfd int, p string, at, mt *int64, noFollow bool) error {
	ts := []unix.Timespec{{Nsec: unix.UTIME_OMIT}, {Nsec: unix.UTIME_OMIT}}
	if at != nil {
		ts[0] = unix.NsecToTimespec(*at)
	}
	if mt != nil {
		ts[1] = unix.NsecToTimespec(*mt)
	}
	flags := 0
	if noFollow {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	return unix.UtimesNanoAt(dirfd, p, ts, flags)
}
