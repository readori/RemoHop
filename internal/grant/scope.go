package grant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"weak"
)

// Scope is the file side of a grant as the host enforces it: the roots,
// each pinned to the directory it named when the grant was loaded.
//
// Pinning means two things. The root is opened once, as an os.Root, and
// the file service does its I/O through that handle, so resolving a path
// below the root can never leave it, whatever symbolic links, junctions or
// renamed directories it meets on the way (os.Root refuses to follow a link
// out of the root, atomically, on every platform). And the root's identity
// (device and inode, or volume and file ID) is remembered: when the root's
// path later names a different directory, because the root was moved,
// removed or replaced by a link, every operation under it is refused
// rather than following the path to its new place. Reissuing the grant
// pins the directory that is there now.
//
// A Scope is shared by every session on the same grant credential; it is
// safe for concurrent use.
type Scope struct {
	id                string
	readAll, writeAll bool
	roots             []*pinnedRoot
}

type pinnedRoot struct {
	path  string // as configured: absolute and clean
	write bool

	mu   sync.Mutex
	root *os.Root    // the pinned handle, if kept open (keepRoots)
	info fs.FileInfo // the root's identity when pinned; nil until then
}

// pinnedErr marks a root that does not exist (and was never pinned):
// nothing below it exists either, so operations fail with ENOENT, and the
// policy check lets them get that far.
type pinnedErr struct{ err error }

func (e *pinnedErr) Error() string { return e.err.Error() }
func (e *pinnedErr) Unwrap() error { return e.err }

// RootChangedError is the refusal for a root whose path no longer names
// the directory pinned when the grant was loaded.
type RootChangedError struct{ Root string }

func (e *RootChangedError) Error() string {
	return "the grant root " + e.Root + " is no longer the directory the grant was issued for (it was moved, removed or replaced); " +
		"files under it are refused until the grant is reissued"
}

// keepRoots says whether pinned roots stay open. On Windows an open
// directory cannot be renamed or deleted (os.OpenRoot does not share
// delete access), which would lock the owner's folders for as long as a
// grant exists; there the root is pinned by identity and opened for each
// operation, and the opened handle is checked against the pin.
var keepRoots = runtime.GOOS != "windows"

// handle returns the pinned root, pinning it if it was missing until now,
// after checking that its path still names the same directory. release
// must be called when the operation is done with it.
func (p *pinnedRoot) handle() (r *os.Root, release func(), err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	nop := func() {}
	if p.info == nil {
		r, err := os.OpenRoot(p.path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil, &pinnedErr{err}
			}
			return nil, nil, err
		}
		fi, err := r.Stat(".")
		if err != nil {
			r.Close()
			return nil, nil, err
		}
		p.info = fi
		if keepRoots {
			p.root = r
			return r, nop, nil
		}
		return r, func() { r.Close() }, nil
	}
	if p.root != nil {
		fi, err := os.Stat(p.path)
		if err != nil || !os.SameFile(fi, p.info) {
			return nil, nil, &RootChangedError{Root: p.path}
		}
		return p.root, nop, nil
	}
	r, err = os.OpenRoot(p.path)
	if err != nil {
		return nil, nil, &RootChangedError{Root: p.path}
	}
	if fi, err := r.Stat("."); err != nil || !os.SameFile(fi, p.info) {
		r.Close()
		return nil, nil, &RootChangedError{Root: p.path}
	}
	return r, func() { r.Close() }, nil
}

// newScope builds the scope of r, pinning the roots that exist now.
func newScope(r *Rules, id string) *Scope {
	s := &Scope{id: id, readAll: r.ReadAll, writeAll: r.WriteAll}
	add := func(p string, write bool) {
		for _, x := range s.roots {
			if x.path == p {
				x.write = x.write || write
				return
			}
		}
		s.roots = append(s.roots, &pinnedRoot{path: p, write: write})
	}
	for _, p := range r.ReadRoots {
		add(p, false)
	}
	for _, p := range r.WriteRoots {
		add(p, true)
	}
	for _, x := range s.roots {
		// Pin now; a missing root is pinned when it appears.
		if _, release, err := x.handle(); err == nil {
			release()
		}
	}
	return s
}

// scopes shares the Scope of one grant credential between every Rules
// parsed from it, so re-reading the grants file keeps the pins made when
// the grant was first loaded. Entries go away with the last Rules using
// them (and the os.Root handles are closed by their finalizers).
var scopes = struct {
	sync.Mutex
	m map[string]weak.Pointer[Scope]
}{m: map[string]weak.Pointer[Scope]{}}

func sharedScope(key string, r *Rules) *Scope {
	sum := sha256.Sum256([]byte(key))
	id := hex.EncodeToString(sum[:16])
	scopes.Lock()
	defer scopes.Unlock()
	if s := scopes.m[id].Value(); s != nil {
		return s
	}
	for k, w := range scopes.m {
		if w.Value() == nil {
			delete(scopes.m, k)
		}
	}
	s := newScope(r, id)
	scopes.m[id] = weak.Make(s)
	return s
}

// Scope returns the rules' file scope. Rules from Grant.Rules share the
// scope of their credential; other Rules get their own, pinned the first
// time it is asked for.
func (r *Rules) Scope() *Scope {
	r.scopeOnce.Do(func() {
		if r.scope == nil {
			b := make([]byte, 16)
			rand.Read(b)
			r.scope = newScope(r, hex.EncodeToString(b))
		}
	})
	return r.scope
}

// ID identifies the scope: the grant credential it was loaded for. A
// reissued grant has a different ID. The file service binds resumable
// uploads to it.
func (s *Scope) ID() string { return s.id }

// Place is where a file operation acts under a scope.
type Place struct {
	// Root is the pinned root the operation goes through, with Rel the
	// path below it ("." for the root itself). Root is nil when the
	// access is not confined (the grant allows it anywhere) or is only
	// looking at an ancestor of a root; the operation then uses Path.
	Root *os.Root
	Rel  string
	// Path is the host path, with its symbolic links resolved as the
	// operation would resolve them.
	Path string
	// RootPath is the root as configured ("" when Root is nil).
	RootPath string

	release func()
}

// Release ends the operation's use of Root. Files opened through Root
// stay open.
func (p Place) Release() {
	if p.release != nil {
		p.release()
	}
}

// Resolve maps host path p (absolute, as the file service resolved it)
// to the place an operation with access a acts on, or refuses it. follow
// says whether the operation follows a symbolic link at p itself (open,
// stat) or acts on the link (lstat, remove, rename, creating a link).
//
// The returned Rel has the links resolved as they are now; the operation
// must still go through Root, which keeps it inside the root if they
// change in between. The caller calls Release on the Place when done.
func (s *Scope) Resolve(p string, a Access, follow bool) (Place, error) {
	pl, err := s.resolve(p, a, follow)
	if err != nil {
		var pe *pinnedErr
		if errors.As(err, &pe) {
			return Place{}, pe.err
		}
	}
	return pl, err
}

func (s *Scope) unconfined(a Access) bool {
	return s.writeAll || (a != Write && s.readAll)
}

func (s *Scope) resolve(p string, a Access, follow bool) (Place, error) {
	if s.unconfined(a) {
		return Place{Path: p}, nil
	}
	var roots []*pinnedRoot
	for _, x := range s.roots {
		if a != Write || x.write {
			roots = append(roots, x)
		}
	}
	if len(roots) == 0 {
		if a == Write {
			return Place{}, errors.New("the grant allows no file changes")
		}
		return Place{}, errors.New("the grant allows no file access")
	}
	if !filepath.IsAbs(p) {
		return Place{}, fmt.Errorf("%s is not an absolute path", p)
	}
	if slices.Contains(strings.FieldsFunc(filepath.ToSlash(p), func(c rune) bool { return c == '/' }), "..") {
		return Place{}, errors.New("paths with .. are not allowed under this grant")
	}
	p = filepath.Clean(p)
	// An operation on a link acts on the link or on what it points to;
	// both must lie under the root, so that neither direction of
	// resolution leads out of it.
	followed, err := RealPath(p)
	if err != nil {
		return Place{}, err
	}
	parent, err := RealPath(filepath.Dir(p))
	if err != nil {
		return Place{}, err
	}
	nofollow := filepath.Join(parent, filepath.Base(p))
	if filepath.Dir(p) == p { // a volume root
		nofollow = followed
	}
	var best *pinnedRoot
	var bestReal string
	var reals []string
	for _, x := range roots {
		rr, err := RealPath(x.path)
		if err != nil {
			continue
		}
		reals = append(reals, rr)
		if within(followed, rr) && within(nofollow, rr) && (best == nil || len(rr) > len(bestReal)) {
			best, bestReal = x, rr
		}
	}
	if best != nil {
		target := nofollow
		if follow {
			target = followed
		}
		rel, err := filepath.Rel(bestReal, target)
		if err != nil {
			return Place{}, err
		}
		h, release, err := best.handle()
		if err != nil {
			return Place{}, err
		}
		return Place{Root: h, Rel: rel, Path: target, RootPath: best.path, release: release}, nil
	}
	if a == Meta {
		// Ancestors of a root may be looked at, not into.
		for _, rr := range reals {
			if within(rr, followed) && within(rr, nofollow) {
				return Place{Path: p}, nil
			}
		}
	}
	where := "readable"
	if a == Write {
		where = "writable"
	}
	shown := p
	if followed != p {
		shown = p + " (" + followed + ")"
	}
	var names []string
	for _, x := range roots {
		names = append(names, x.path)
	}
	return Place{}, fmt.Errorf("%s is outside the %s roots: %s", shown, where, strings.Join(names, ", "))
}

// IsRoot reports whether fi (from Lstat) is one of the scope's pinned
// roots. The file service refuses to remove, rename or replace a root
// entry: a session confined to a root cannot swap it for something else.
func (s *Scope) IsRoot(fi fs.FileInfo) (string, bool) {
	if fi == nil {
		return "", false
	}
	for _, x := range s.roots {
		x.mu.Lock()
		id := x.info
		x.mu.Unlock()
		if id != nil && os.SameFile(id, fi) {
			return x.path, true
		}
	}
	return "", false
}

// CheckLink decides whether a symbolic link with target text target may be
// created at link (a Place resolved without following, under a root). It
// must lead to a place inside the same root: the file service would not
// follow it anywhere else, and other software on the host should not find
// links out of a root that a confined session planted.
func (s *Scope) CheckLink(link Place, target string) error {
	if link.Root == nil {
		return nil
	}
	if target == "" {
		return errors.New("empty link target")
	}
	t := target
	if !filepath.IsAbs(t) {
		if filepath.VolumeName(t) != "" || os.IsPathSeparator(t[0]) {
			return fmt.Errorf("link target %q is relative to a drive or volume, which is not allowed under this grant", target)
		}
		// Not cleaned: ".." after a link component means the link
		// target's parent, as the OS will resolve it.
		t = filepath.Dir(link.Path) + string(filepath.Separator) + t
	}
	rt, err := realPath(t)
	if err != nil {
		return err
	}
	rr, err := RealPath(link.RootPath)
	if err != nil {
		return err
	}
	if !within(rt, rr) {
		return fmt.Errorf("a symbolic link made under this grant must point inside its root %s; %q leads to %s", link.RootPath, target, rt)
	}
	return nil
}

// CheckPath decides whether the rules allow access to host path p (an
// absolute path, as the file service resolved it). Symbolic links (and
// junctions) are resolved first, both for the path itself and for its
// parent directory (an operation on a link acts on one or the other), and
// both results must lie under an allowed root, so a link cannot lead out
// of a root; a link whose target does not exist yet is resolved to where
// the target would be created. Paths with ".." components are refused when
// roots apply. A root whose path no longer names the directory pinned when
// the grant was loaded refuses everything below it (see Scope).
//
// This is the policy check, made before the operation. The file service
// then does the operation through the root's pinned handle (see
// Scope.Resolve), so a link swapped in between cannot lead it out either.
func (r *Rules) CheckPath(p string, a Access) error {
	pl, err := r.Scope().resolve(p, a, true)
	pl.Release()
	var pe *pinnedErr
	if errors.As(err, &pe) {
		// A root that does not exist (yet): allowed, and whatever is
		// asked for below it will not be found.
		return nil
	}
	return err
}

// within reports whether p is root or below it. Both are clean absolute
// paths; on Windows filepath.Rel compares case-insensitively.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// maxLinks bounds the links RealPath follows, as SYMLOOP_MAX-like limits
// do in the kernels.
const maxLinks = 255

// RealPath resolves the symbolic links in p as far as p exists, and
// appends the components that do not exist yet unchanged. A link whose
// target does not exist is resolved too: what matters is where an open or
// create through it would land. On Windows junctions (mount points) are
// resolved like links.
func RealPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	return realPath(filepath.Clean(p))
}

// realPath is RealPath of an absolute path that may contain "." and ".."
// components; ".." is applied to the path as resolved so far, as the OS
// does.
func realPath(p string) (string, error) {
	sep := string(filepath.Separator)
	vol := filepath.VolumeName(p)
	resolved := vol + sep
	todo := splitPath(p[len(vol):])
	links := 0
	for len(todo) > 0 {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		fi, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return filepath.Join(append([]string{next}, todo...)...), nil
			}
			return "", err
		}
		if !isLink(fi) {
			resolved = next
			continue
		}
		t, err := os.Readlink(next)
		if err != nil {
			if fi.Mode()&fs.ModeSymlink == 0 {
				resolved = next // a reparse point that is not a link
				continue
			}
			return "", err
		}
		if links++; links > maxLinks {
			return "", &fs.PathError{Op: "realpath", Path: p, Err: syscall.ELOOP}
		}
		tv := filepath.VolumeName(t)
		switch {
		case filepath.IsAbs(t):
			resolved, t = tv+sep, t[len(tv):]
		case tv != "":
			return "", &fs.PathError{Op: "realpath", Path: next, Err: syscall.EINVAL} // "C:x"
		case t != "" && os.IsPathSeparator(t[0]):
			resolved = vol + sep // rooted on the current volume
		}
		todo = append(splitPath(t), todo...)
	}
	return resolved, nil
}

func isLink(fi fs.FileInfo) bool {
	if fi.Mode()&fs.ModeSymlink != 0 {
		return true
	}
	// Junctions (and other name surrogates) are irregular on Windows;
	// Readlink tells which can be followed.
	return runtime.GOOS == "windows" && fi.Mode()&fs.ModeIrregular != 0
}

func splitPath(p string) []string {
	return strings.FieldsFunc(p, func(c rune) bool { return c < 0x80 && os.IsPathSeparator(uint8(c)) })
}
