package rfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// nativeErrorName maps Win32 error codes to the POSIX names clients are
// promised, where the common table (POSIX errno values) has no entry.
func nativeErrorName(err error, e syscall.Errno) string {
	switch e {
	case 3: // PATH_NOT_FOUND
		return pathNotFound(err)
	case 2, 161: // FILE_NOT_FOUND, BAD_PATHNAME
		return "ENOENT"
	case 5: // ACCESS_DENIED
		return "EACCES"
	case 6: // INVALID_HANDLE
		return "EBADF"
	case 17: // NOT_SAME_DEVICE
		return "EXDEV"
	case 19: // WRITE_PROTECT
		return "EROFS"
	case 32, 33: // SHARING_VIOLATION, LOCK_VIOLATION
		return "EBUSY"
	case 39, 112: // HANDLE_DISK_FULL, DISK_FULL
		return "ENOSPC"
	case 50: // NOT_SUPPORTED
		return "ENOTSUP"
	case 80, 183: // FILE_EXISTS, ALREADY_EXISTS
		return "EEXIST"
	case 123: // INVALID_NAME
		return "EINVAL"
	case 145: // DIR_NOT_EMPTY
		return "ENOTEMPTY"
	case 206: // FILENAME_EXCED_RANGE
		return "ENAMETOOLONG"
	case 267: // DIRECTORY ("the directory name is invalid")
		return "ENOTDIR"
	case 1314: // PRIVILEGE_NOT_HELD
		return "EPERM"
	}
	return ""
}

// pathNotFound tells apart the two things ERROR_PATH_NOT_FOUND means: a
// missing directory on the way (ENOENT, as everywhere) and a component
// that exists but is not a directory (ENOTDIR, as POSIX reports it). It
// looks at the nearest existing ancestor of the failing path; that is a
// few lstats on an error path, and if the tree changes meanwhile the
// answer is merely the other of two honest descriptions. Without a path
// to inspect it is ENOENT.
func pathNotFound(err error) string {
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Path == "" {
		return "ENOENT"
	}
	for p := filepath.Dir(pe.Path); ; p = filepath.Dir(p) {
		if fi, lerr := os.Stat(p); lerr == nil {
			if !fi.IsDir() {
				return "ENOTDIR"
			}
			return "ENOENT"
		}
		if p == filepath.Dir(p) {
			return "ENOENT"
		}
	}
}
