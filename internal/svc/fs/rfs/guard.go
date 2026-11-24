package rfs

import (
	"context"
	"errors"

	"github.com/readori/RemoHop/internal/grant"
)

// guardError is a refusal by the session's grant (or its owner's denial of
// an approval). It crosses the wire as EACCES with the refusal's text, so
// the client learns why, not just that it may not.
type guardError struct{ err error }

func (e *guardError) Error() string { return e.err.Error() }
func (e *guardError) Unwrap() error { return e.err }

// guarded asks the session's grant about an operation on host path p.
// Without a grant it allows everything: the host serves with its user's
// own privileges, as resolve says.
func guarded(ctx context.Context, g grant.Guard, op string, a grant.Access, p string) error {
	if g == nil {
		return nil
	}
	if err := g.File(ctx, op, a, p); err != nil {
		return &guardError{err}
	}
	return nil
}

// opAccess classifies a path operation for the grant. ok is false for
// operations that take no path or are checked where they are served.
func opAccess(op byte, r *request) (name string, a grant.Access, ok bool) {
	switch op {
	case opStat:
		return "stat", grant.Meta, true
	case opLstat:
		return "lstat", grant.Meta, true
	case opRealpath:
		return "realpath", grant.Meta, true
	case opStatFS:
		return "statfs", grant.Meta, true
	case opOpenDir:
		return "list", grant.Read, true
	case opReadFile:
		return "read", grant.Read, true
	case opHash:
		return "hash", grant.Read, true
	case opReadlink:
		return "readlink", grant.Read, true
	case opOpen:
		if r.Flags&(wWronly|wRdwr|wAppend|wCreate|wTrunc) != 0 {
			return "open-write", grant.Write, true
		}
		return "open", grant.Read, true
	case opMkdir:
		return "mkdir", grant.Write, true
	case opRemove:
		return "remove", grant.Write, true
	case opRename:
		return "rename", grant.Write, true
	case opLink:
		return "link", grant.Write, true
	case opChmod:
		return "chmod", grant.Write, true
	case opTruncate:
		return "truncate", grant.Write, true
	case opUtimes:
		return "utimes", grant.Write, true
	case opWriteFile:
		return "write", grant.Write, true
	}
	// opSymlink: Path is the link's target text, not a path acted on;
	// its Path2 is checked where it is served. opUserDir: checked there.
	return "", 0, false
}

func isGuardError(err error) (string, bool) {
	var ge *guardError
	if errors.As(err, &ge) {
		return ge.Error(), true
	}
	return "", false
}
