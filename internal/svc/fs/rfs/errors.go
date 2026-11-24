package rfs

import (
	"errors"
	"io/fs"
	"syscall"
)

// errnoNames maps the POSIX errors that cross the wire to their names. Names,
// not numbers, are sent because errno values differ between operating
// systems.
var errnoNames = map[syscall.Errno]string{
	syscall.EPERM:        "EPERM",
	syscall.ENOENT:       "ENOENT",
	syscall.EIO:          "EIO",
	syscall.ENXIO:        "ENXIO",
	syscall.E2BIG:        "E2BIG",
	syscall.EBADF:        "EBADF",
	syscall.EAGAIN:       "EAGAIN",
	syscall.ENOMEM:       "ENOMEM",
	syscall.EACCES:       "EACCES",
	syscall.EBUSY:        "EBUSY",
	syscall.EEXIST:       "EEXIST",
	syscall.EXDEV:        "EXDEV",
	syscall.ENODEV:       "ENODEV",
	syscall.ENOTDIR:      "ENOTDIR",
	syscall.EISDIR:       "EISDIR",
	syscall.EINVAL:       "EINVAL",
	syscall.ENFILE:       "ENFILE",
	syscall.EMFILE:       "EMFILE",
	syscall.ETXTBSY:      "ETXTBSY",
	syscall.EFBIG:        "EFBIG",
	syscall.ENOSPC:       "ENOSPC",
	syscall.ESPIPE:       "ESPIPE",
	syscall.EROFS:        "EROFS",
	syscall.EMLINK:       "EMLINK",
	syscall.ERANGE:       "ERANGE",
	syscall.ENAMETOOLONG: "ENAMETOOLONG",
	syscall.ENOSYS:       "ENOSYS",
	syscall.ENOTEMPTY:    "ENOTEMPTY",
	syscall.ELOOP:        "ELOOP",
	syscall.ENODATA:      "ENODATA",
	syscall.EOVERFLOW:    "EOVERFLOW",
	syscall.ENOTSUP:      "ENOTSUP",
	syscall.ESTALE:       "ESTALE",
	syscall.EDQUOT:       "EDQUOT",
	syscall.EINTR:        "EINTR",
	syscall.ETIMEDOUT:    "ETIMEDOUT",
	syscall.ECANCELED:    "ECANCELED",
}

var errnoByName = func() map[string]syscall.Errno {
	m := make(map[string]syscall.Errno, len(errnoNames))
	for e, n := range errnoNames {
		m[n] = e
	}
	// EOPNOTSUPP equals ENOTSUP on Linux but not on every platform.
	m["EOPNOTSUPP"] = syscall.ENOTSUP
	return m
}()

// errorCode turns a host-side error into its wire form.
// errNotDir is the host's own "not a directory". It is a distinct value
// rather than syscall.ENOTDIR because on Windows that constant is
// ERROR_PATH_NOT_FOUND, which also means "no such directory" and is told
// apart by looking at the path (see nativeErrorName).
var errNotDir = &hostError{"ENOTDIR"}

// hostError is an error the host code itself decided on, by POSIX name.
type hostError struct{ code string }

func (e *hostError) Error() string { return errnoText[e.code] }

func errorCode(err error) (code, msg string) {
	if msg, ok := isGuardError(err); ok {
		return "EACCES", msg
	}
	var he *hostError
	if errors.As(err, &he) {
		return he.code, ""
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		// The OS-specific names first: on Windows the syscall package
		// aliases some POSIX names to Win32 codes (ENOTDIR is
		// ERROR_PATH_NOT_FOUND), so the common table alone would
		// misname them.
		if n := nativeErrorName(err, en); n != "" {
			return n, ""
		}
		if n, ok := errnoNames[en]; ok {
			return n, ""
		}
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "ENOENT", ""
	case errors.Is(err, fs.ErrExist):
		return "EEXIST", ""
	case errors.Is(err, fs.ErrPermission):
		return "EACCES", ""
	case errors.Is(err, fs.ErrClosed):
		return "EBADF", ""
	case errors.Is(err, fs.ErrInvalid):
		return "EINVAL", ""
	}
	return "EIO", err.Error()
}

// Error is a POSIX error reported by the remote host. It unwraps to the
// matching local syscall.Errno (EIO for names this platform lacks), and
// matches fs.ErrNotExist, fs.ErrExist and fs.ErrPermission by name, so the
// usual errors.Is checks work on every client platform.
type Error struct {
	Code  string // POSIX name, e.g. "ENOENT"
	Errno syscall.Errno
	Msg   string // extra detail from the host, if any
}

func (e *Error) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	if m, ok := errnoText[e.Code]; ok {
		return m
	}
	return e.Errno.Error()
}

// errnoText is the wording of the common errors. The local
// syscall.Errno's own text is not used for them: it differs between
// client platforms (Windows prints Win32 messages), and a remote error
// should read the same wherever the client runs.
var errnoText = map[string]string{
	"EPERM":        "operation not permitted",
	"ENOENT":       "no such file or directory",
	"EIO":          "input/output error",
	"EBADF":        "bad file descriptor",
	"EACCES":       "permission denied",
	"EBUSY":        "device or resource busy",
	"EEXIST":       "file exists",
	"EXDEV":        "invalid cross-device link",
	"ENOTDIR":      "not a directory",
	"EISDIR":       "is a directory",
	"EINVAL":       "invalid argument",
	"EMFILE":       "too many open files",
	"EFBIG":        "file too large",
	"ENOSPC":       "no space left on device",
	"EROFS":        "read-only file system",
	"ENAMETOOLONG": "file name too long",
	"ENOSYS":       "function not implemented",
	"ENOTEMPTY":    "directory not empty",
	"ELOOP":        "too many levels of symbolic links",
	"ENOTSUP":      "operation not supported",
	"EDQUOT":       "disk quota exceeded",
	"E2BIG":        "argument list too long",
	"ETXTBSY":      "text file busy",
}

// Unwrap returns the local errno.
func (e *Error) Unwrap() error { return e.Errno }

// Is matches the io/fs sentinel errors by POSIX name.
func (e *Error) Is(target error) bool {
	switch target {
	case fs.ErrNotExist:
		return e.Code == "ENOENT"
	case fs.ErrExist:
		return e.Code == "EEXIST" || e.Code == "ENOTEMPTY"
	case fs.ErrPermission:
		return e.Code == "EACCES" || e.Code == "EPERM"
	case fs.ErrInvalid:
		return e.Code == "EINVAL"
	}
	return false
}

func newError(code, msg string) *Error {
	en, ok := errnoByName[code]
	if !ok {
		en = syscall.EIO
		if msg == "" {
			msg = code
		}
	}
	return &Error{Code: code, Errno: en, Msg: msg}
}

// remoteErr builds the client-side error for a failed operation.
func remoteErr(op, path, code, msg string) error {
	return &fs.PathError{Op: op, Path: path, Err: newError(code, msg)}
}

// ErrClosed is returned (wrapped) by every operation once the session or
// the control stream is gone. It unwraps to syscall.EIO.
var ErrClosed = &Error{Code: "EIO", Errno: syscall.EIO, Msg: "rfs: connection closed"}

// Errno extracts the errno an error should map to for a filesystem caller
// such as FUSE: the remote errno, or EIO for anything else.
func Errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Errno
	}
	var en syscall.Errno
	if errors.As(err, &en) {
		return en
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrPermission):
		return syscall.EACCES
	}
	return syscall.EIO
}
