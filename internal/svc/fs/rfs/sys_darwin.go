package rfs

import (
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
	return &StatFS{
		Blocks: s.Blocks, Bfree: s.Bfree, Bavail: s.Bavail,
		Files: s.Files, Ffree: s.Ffree,
		Bsize: s.Bsize, Frsize: s.Bsize, NameLen: 255,
	}, nil
}

// rename maps the renameat2-style flags onto renamex_np(2), which is
// atomic for both.
func rename(from, to string, flags int) error {
	if flags == 0 {
		return syscall.Rename(from, to)
	}
	var f uint32
	if flags&RenameNoReplace != 0 {
		f |= unix.RENAME_EXCL
	}
	if flags&RenameExchange != 0 {
		f |= unix.RENAME_SWAP
	}
	return unix.RenamexNp(from, to, f)
}

// utimes sets the given times (nil = unchanged), on the link itself when
// noFollow is set. A time left unchanged is read back and set again
// rather than passed as UTIME_OMIT, which x/sys does not define here.
func utimes(p string, at, mt *int64, noFollow bool) error {
	flags := 0
	if noFollow {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	var st unix.Stat_t
	if at == nil || mt == nil {
		if err := unix.Fstatat(unix.AT_FDCWD, p, &st, flags); err != nil {
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
	return unix.UtimesNanoAt(unix.AT_FDCWD, p, ts, flags)
}
