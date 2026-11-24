package rfs

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func statTimes(st *syscall.Stat_t) (atime, ctime int64) {
	return st.Atimespec.Nano(), st.Ctimespec.Nano()
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
		Bsize: s.Bsize, Frsize: s.Bsize, NameLen: 255,
	}
}

// rename maps the renameat2-style flags onto renamex_np(2), which is
// atomic for both.
func rename(from, to string, flags int) error {
	if flags == 0 {
		return syscall.Rename(from, to)
	}
	return unix.RenamexNp(from, to, renameFlags(flags))
}

func renameFlags(flags int) uint32 {
	var f uint32
	if flags&RenameNoReplace != 0 {
		f |= unix.RENAME_EXCL
	}
	if flags&RenameExchange != 0 {
		f |= unix.RENAME_SWAP
	}
	return f
}

// renameIn is rename with flags between two paths below root r: the parent
// directories are opened through r and the entries renamed by name in them
// (renameatx_np does not follow the final components).
func renameIn(r *os.Root, from, to string, flags int) error {
	d1, err := r.Open(filepath.Dir(from))
	if err != nil {
		return err
	}
	defer d1.Close()
	d2, err := r.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer d2.Close()
	return unix.RenameatxNp(int(d1.Fd()), filepath.Base(from), int(d2.Fd()), filepath.Base(to), renameFlags(flags))
}

// utimes sets the given times (nil = unchanged), on the link itself when
// noFollow is set. A time left unchanged is read back and set again
// rather than passed as UTIME_OMIT, which x/sys does not define here.
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
	flags := 0
	if noFollow {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	var st unix.Stat_t
	if at == nil || mt == nil {
		if err := unix.Fstatat(dirfd, p, &st, flags); err != nil {
			return err
		}
	}
	ts := []unix.Timespec{st.Atim, st.Mtim}
	if at != nil {
		ts[0] = unix.NsecToTimespec(*at)
	}
	if mt != nil {
		ts[1] = unix.NsecToTimespec(*mt)
	}
	return unix.UtimesNanoAt(dirfd, p, ts, flags)
}
