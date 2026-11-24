package rfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/grant"
)

// loc is where an operation acts. For a session confined to grant roots
// it is a path below a pinned root, and every operation goes through that
// root's os.Root handle, so it cannot be led out of the root by links,
// junctions or directories swapped in after the grant's check. For full
// access (and accesses a grant allows anywhere) it is a plain host path:
// the host serves with its user's own privileges, as resolve says.
type loc struct {
	pl    grant.Place // pl.Root nil: plain path pl.Path
	scope *grant.Scope
	grant string // the grant's name, for refusals
}

func plainLoc(p string) loc { return loc{pl: grant.Place{Path: p}} }

func (l loc) confined() bool { return l.pl.Root != nil }

// release ends the use of the root handle (see grant.Place.Release).
func (l loc) release()     { l.pl.Release() }
func (l loc) path() string { return l.pl.Path }

// errGrantChanged refuses an operation of a session whose grant was
// reissued while it was connected: its credential no longer stands for
// the rules now in force.
var errGrantChanged = errors.New("the grant was reissued since this session started; reconnect with the new credential")

// scopeOf returns the file scope of a session's grant, nil for full
// access.
func scopeOf(g grant.Guard) (*grant.Scope, error) {
	if g == nil {
		return nil, nil
	}
	s, err := g.FileScope()
	if err != nil {
		return nil, &guardError{err}
	}
	return s, nil
}

// placeFor maps host path p to where an operation with access a acts, for
// a session with guard g (nil: full access) whose stream pinned scope
// pinned when it started (nil: none pinned). follow says whether a
// symbolic link at p itself is followed. The guard must already have
// allowed the operation; this decides where it happens.
func placeFor(g grant.Guard, pinned *grant.Scope, p string, a grant.Access, follow bool) (loc, error) {
	if g == nil {
		return plainLoc(p), nil
	}
	s, err := scopeOf(g)
	if err != nil {
		return loc{}, err
	}
	if pinned != nil && s != pinned {
		return loc{}, &guardError{errGrantChanged}
	}
	pl, err := s.Resolve(p, a, follow)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return loc{}, err // under a root that does not exist
		}
		return loc{}, &guardError{&grant.Refusal{Grant: g.Name(), What: a.String(), Why: err.Error()}}
	}
	return loc{pl: pl, scope: s, grant: g.Name()}, nil
}

// rootErr turns os.Root's refusal to leave the root into a refusal the
// client can read; other errors pass unchanged.
func rootErr(err error) error {
	if err != nil && isEscape(err) {
		return &guardError{errors.New("the path leads out of the grant root through a link (" + err.Error() + ")")}
	}
	return err
}

func isEscape(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e.Error() == "path escapes from parent" {
			return true
		}
	}
	return false
}

// notRoot refuses an operation that would remove, rename or replace the
// entry of a grant root itself: a session confined to a root cannot swap
// it for something else.
func (l loc) notRoot() error {
	if !l.confined() {
		return nil
	}
	refuse := func(root string) error {
		return &guardError{&grant.Refusal{Grant: l.grant, What: "removing, renaming or replacing the grant root " + root}}
	}
	if l.pl.Rel == "." {
		return refuse(l.pl.RootPath)
	}
	if fi, err := l.pl.Root.Lstat(l.pl.Rel); err == nil {
		if r, ok := l.scope.IsRoot(fi); ok {
			return refuse(r)
		}
	}
	return nil
}

func (l loc) stat() (fs.FileInfo, error) {
	if l.confined() {
		fi, err := l.pl.Root.Stat(l.pl.Rel)
		return fi, rootErr(err)
	}
	return os.Stat(l.pl.Path)
}

func (l loc) lstat() (fs.FileInfo, error) {
	if l.confined() {
		fi, err := l.pl.Root.Lstat(l.pl.Rel)
		return fi, rootErr(err)
	}
	return os.Lstat(l.pl.Path)
}

func (l loc) open(flag int, perm fs.FileMode) (*os.File, error) {
	if l.confined() {
		f, err := l.pl.Root.OpenFile(l.pl.Rel, flag, perm)
		return f, rootErr(err)
	}
	return os.OpenFile(l.pl.Path, flag, perm)
}

func (l loc) readlink() (string, error) {
	if l.confined() {
		t, err := l.pl.Root.Readlink(l.pl.Rel)
		return t, rootErr(err)
	}
	return os.Readlink(l.pl.Path)
}

func (l loc) mkdir(m fs.FileMode, all bool) error {
	if l.confined() {
		if all {
			return rootErr(l.pl.Root.MkdirAll(l.pl.Rel, m))
		}
		return rootErr(l.pl.Root.Mkdir(l.pl.Rel, m))
	}
	if all {
		return os.MkdirAll(l.pl.Path, m)
	}
	return os.Mkdir(l.pl.Path, m)
}

func (l loc) remove(kind string) error {
	if !l.confined() {
		switch kind {
		case removeAny:
			return os.Remove(l.pl.Path)
		case removeFile:
			return unlink(l.pl.Path)
		case removeDir:
			return rmdir(l.pl.Path)
		case removeAll:
			return os.RemoveAll(l.pl.Path)
		}
		return syscall.EINVAL
	}
	if err := l.notRoot(); err != nil {
		return err
	}
	r, rel := l.pl.Root, l.pl.Rel
	switch kind {
	case removeAny:
		return rootErr(r.Remove(rel))
	case removeFile, removeDir:
		fi, err := r.Lstat(rel)
		if err != nil {
			return rootErr(err)
		}
		if kind == removeFile && fi.IsDir() {
			return &fs.PathError{Op: "unlink", Path: rel, Err: syscall.EISDIR}
		}
		if kind == removeDir && !fi.IsDir() {
			return errNotDir
		}
		return rootErr(r.Remove(rel))
	case removeAll:
		return rootErr(r.RemoveAll(rel))
	}
	return syscall.EINVAL
}

// sameRoot checks that two confined places can be renamed or linked into
// each other: os.Root works within one root. Places under different roots
// get EXDEV, which clients answer by copying.
func sameRoot(a, b loc) error {
	if a.confined() != b.confined() || a.pl.RootPath != b.pl.RootPath {
		return &os.LinkError{Op: "rename", Old: a.pl.Path, New: b.pl.Path, Err: syscall.EXDEV}
	}
	return nil
}

func renameLoc(from, to loc, flags int) error {
	if !from.confined() && !to.confined() {
		return rename(from.pl.Path, to.pl.Path, flags)
	}
	if err := sameRoot(from, to); err != nil {
		return err
	}
	if err := from.notRoot(); err != nil {
		return err
	}
	if err := to.notRoot(); err != nil {
		return err
	}
	if flags == 0 {
		return rootErr(from.pl.Root.Rename(from.pl.Rel, to.pl.Rel))
	}
	return rootErr(renameIn(from.pl.Root, from.pl.Rel, to.pl.Rel, flags))
}

func linkLoc(old, nw loc) error {
	if !old.confined() && !nw.confined() {
		return os.Link(old.pl.Path, nw.pl.Path)
	}
	if err := sameRoot(old, nw); err != nil {
		return err
	}
	if err := nw.notRoot(); err != nil {
		return err
	}
	return rootErr(old.pl.Root.Link(old.pl.Rel, nw.pl.Rel))
}

// symlink makes l a symbolic link to target. Under a grant the target must
// lead inside l's root (grant.Scope.CheckLink).
func (l loc) symlink(target string) error {
	if !l.confined() {
		return os.Symlink(target, l.pl.Path)
	}
	if err := l.notRoot(); err != nil {
		return err
	}
	if err := l.scope.CheckLink(l.pl, target); err != nil {
		return &guardError{&grant.Refusal{Grant: l.grant, What: "this symbolic link", Why: err.Error()}}
	}
	return rootErr(l.pl.Root.Symlink(target, l.pl.Rel))
}

func (l loc) chmod(m fs.FileMode) error {
	if l.confined() {
		return rootErr(l.pl.Root.Chmod(l.pl.Rel, m))
	}
	return os.Chmod(l.pl.Path, m)
}

func (l loc) truncate(size int64) error {
	if !l.confined() {
		return os.Truncate(l.pl.Path, size)
	}
	// Non-blocking, so that a FIFO does not wait for a reader; truncate(2)
	// would refuse it anyway.
	f, err := l.open(os.O_WRONLY|oNonblock, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return &fs.PathError{Op: "truncate", Path: l.pl.Rel, Err: syscall.EINVAL}
	}
	return f.Truncate(size)
}

func (l loc) utimes(at, mt *int64, noFollow bool) error {
	if !l.confined() {
		return utimes(l.pl.Path, at, mt, noFollow)
	}
	if noFollow {
		return rootErr(lutimesIn(l.pl.Root, l.pl.Rel, at, mt))
	}
	var a, m time.Time // zero: unchanged
	if at != nil {
		a = time.Unix(0, *at)
	}
	if mt != nil {
		m = time.Unix(0, *mt)
	}
	return rootErr(l.pl.Root.Chtimes(l.pl.Rel, a, m))
}

func (l loc) statFS() (*StatFS, error) {
	if !l.confined() {
		return statFS(l.pl.Path)
	}
	f, err := l.open(os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return fstatFS(f)
}

// join is the entry name in directory l.
func (l loc) join(name string) loc {
	j := l
	j.pl.Path = filepath.Join(l.pl.Path, name)
	if l.confined() {
		j.pl.Rel = filepath.Join(l.pl.Rel, name)
	}
	return j
}

// openDir opens the directory that holds l as an os.Root, for writing a
// file there by name: temporary and partial files are created, checked
// and renamed into place through it, never by path. It returns l's base
// name in that directory.
func (l loc) openDir() (*os.Root, string, error) {
	if l.confined() {
		if l.pl.Rel == "." {
			return nil, "", syscall.EISDIR
		}
		d, err := l.pl.Root.OpenRoot(filepath.Dir(l.pl.Rel))
		return d, filepath.Base(l.pl.Rel), rootErr(err)
	}
	dir, base := filepath.Split(l.pl.Path)
	if dir == "" {
		dir = "."
	}
	if base == "" {
		return nil, "", syscall.EISDIR
	}
	d, err := os.OpenRoot(dir)
	return d, base, err
}

// Partial files of resumable uploads are named by the host: a client
// gives a key (it identifies the source, so a changed source does not
// resume old data) and the host derives the name from the key, the
// destination's name and the session's grant credential. A partial file
// made under one grant is never resumed under another, nor after the grant
// is reissued, and a client cannot point a resume at a file of its own
// choosing.
const maxPartialKey = 64

func validPartialKey(k string) bool {
	if k == "" || len(k) > maxPartialKey {
		return false
	}
	for _, c := range k {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// partialFileName is the host's name for the partial file of destination
// base under the grant scope scopeID ("" for full access).
func partialFileName(scopeID, base, key string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("remohop-partial-v1\x00%s\x00%s\x00%s", scopeID, base, key)))
	return PartialPrefix(base) + hex.EncodeToString(h[:16])
}
