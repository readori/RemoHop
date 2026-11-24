package rfs

import (
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
	return &StatFS{
		Blocks: s.Blocks, Bfree: s.Bfree, Bavail: s.Bavail,
		Files: s.Files, Ffree: s.Ffree,
		Bsize: uint32(s.Bsize), Frsize: uint32(s.Frsize), NameLen: uint32(s.Namelen),
	}, nil
}

func rename(from, to string, flags int) error {
	if flags == 0 {
		return syscall.Rename(from, to)
	}
	var f uint
	if flags&RenameNoReplace != 0 {
		f |= unix.RENAME_NOREPLACE
	}
	if flags&RenameExchange != 0 {
		f |= unix.RENAME_EXCHANGE
	}
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, f)
}

// utimes sets the given times (nil = unchanged), on the link itself when
// noFollow is set.
func utimes(p string, at, mt *int64, noFollow bool) error {
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
	return unix.UtimesNanoAt(unix.AT_FDCWD, p, ts, flags)
}
