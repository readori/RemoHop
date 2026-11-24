// Package xfer implements file transfer between the local machine and a
// RemoHop host: `remohop <ID> cp` and `remohop <ID> sync`, as a library (Copy, Sync)
// with thin CLI wrappers.
//
// Downloads never trust the host: every name it returns is checked to be a
// single path component, duplicate names are ignored, directories are
// created and verified one level at a time (an existing symlink is never
// descended into), and files are installed by renaming a temporary file
// over the final name, which replaces rather than follows a symlink. So a
// malicious host cannot make a download write outside the destination.
package xfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// Path is one side of a transfer.
type Path struct {
	Remote bool
	Path   string
}

// ParsePath parses a command-line path: a leading colon means the remote
// host (":/var/log", ":src" relative to the remote home), anything else is
// local.
func ParsePath(s string) Path {
	if strings.HasPrefix(s, ":") {
		return Path{Remote: true, Path: s[1:]}
	}
	return Path{Path: s}
}

func (p Path) String() string {
	if p.Remote {
		return ":" + p.Path
	}
	return p.Path
}

// entry is a file's attributes, local or remote.
type entry struct {
	name   string
	mode   fs.FileMode
	size   int64
	mtime  time.Time
	target string // symlink target
}

func (e *entry) isDir() bool     { return e.mode.IsDir() }
func (e *entry) isRegular() bool { return e.mode.IsRegular() }
func (e *entry) isSymlink() bool { return e.mode&fs.ModeSymlink != 0 }

// sameType reports whether two entries are the same kind of file.
func sameType(a, b *entry) bool { return a.mode.Type() == b.mode.Type() }

// fsys abstracts the local and remote filesystems so walking and planning
// are written once.
type fsys interface {
	remote() bool
	join(dir, name string) string
	stat(ctx context.Context, p string) (*entry, error)
	lstat(ctx context.Context, p string) (*entry, error)
	readDir(ctx context.Context, p string) ([]*entry, error)
	mkdir(ctx context.Context, p string, perm fs.FileMode) error
	mkdirAll(ctx context.Context, p string, perm fs.FileMode) error
	symlink(ctx context.Context, target, p string) error
	chmod(ctx context.Context, p string, perm fs.FileMode) error
	chtimes(ctx context.Context, p string, mtime time.Time) error
	remove(ctx context.Context, p string) error // a non-directory
	rmdir(ctx context.Context, p string) error
	removeAll(ctx context.Context, p string) error
	hash(ctx context.Context, p string) (string, error)
}

// permBits are the mode bits a transfer preserves.
const permBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

type localFS struct{}

func (localFS) remote() bool { return false }

func (localFS) join(dir, name string) string { return filepath.Join(dir, name) }

func localEntry(fi fs.FileInfo, target string) *entry {
	return &entry{name: fi.Name(), mode: fi.Mode(), size: fi.Size(), mtime: fi.ModTime(), target: target}
}

func (localFS) stat(_ context.Context, p string) (*entry, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	return localEntry(fi, ""), nil
}

func (localFS) lstat(_ context.Context, p string) (*entry, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	var t string
	if fi.Mode()&fs.ModeSymlink != 0 {
		t, _ = os.Readlink(p)
	}
	return localEntry(fi, t), nil
}

func (l localFS) readDir(_ context.Context, p string) ([]*entry, error) {
	ents, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]*entry, 0, len(ents))
	for _, de := range ents {
		fi, err := de.Info()
		if err != nil {
			continue
		}
		var t string
		if fi.Mode()&fs.ModeSymlink != 0 {
			t, _ = os.Readlink(filepath.Join(p, de.Name()))
		}
		out = append(out, localEntry(fi, t))
	}
	return out, nil
}

func (localFS) mkdir(_ context.Context, p string, perm fs.FileMode) error { return os.Mkdir(p, perm) }
func (localFS) mkdirAll(_ context.Context, p string, perm fs.FileMode) error {
	return os.MkdirAll(p, perm)
}
func (localFS) symlink(_ context.Context, target, p string) error { return os.Symlink(target, p) }

// chmod and chtimes never follow a symlink: the entry at p may be a link a
// host just created, and following it would let the host change the mode
// or time of any file the user owns.
func (localFS) chmod(_ context.Context, p string, perm fs.FileMode) error {
	return lchmod(p, perm)
}
func (localFS) chtimes(_ context.Context, p string, mtime time.Time) error {
	return lchtimes(p, mtime)
}
func (localFS) remove(_ context.Context, p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return &fs.PathError{Op: "unlink", Path: p, Err: errors.New("is a directory")}
	}
	return os.Remove(p)
}
func (localFS) rmdir(_ context.Context, p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "rmdir", Path: p, Err: errors.New("not a directory")}
	}
	return os.Remove(p)
}
func (localFS) removeAll(_ context.Context, p string) error { return os.RemoveAll(p) }
func (localFS) hash(_ context.Context, p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type remoteFS struct{ c *rfs.Client }

func (remoteFS) remote() bool { return true }

// join appends a name to a remote path without cleaning it, so the host
// sees exactly the path the user gave plus validated components.
func (remoteFS) join(dir, name string) string {
	switch {
	case dir == "":
		return name
	case strings.HasSuffix(dir, "/"):
		return dir + name
	}
	return dir + "/" + name
}

func remoteEntry(fi *rfs.FileInfo) *entry {
	return &entry{name: fi.Name(), mode: fi.Mode(), size: fi.Size(), mtime: fi.ModTime(), target: fi.Target()}
}

func (r remoteFS) stat(ctx context.Context, p string) (*entry, error) {
	fi, err := r.c.Stat(ctx, p)
	if err != nil {
		return nil, err
	}
	return remoteEntry(fi), nil
}

func (r remoteFS) lstat(ctx context.Context, p string) (*entry, error) {
	fi, err := r.c.Lstat(ctx, p)
	if err != nil {
		return nil, err
	}
	return remoteEntry(fi), nil
}

func (r remoteFS) readDir(ctx context.Context, p string) ([]*entry, error) {
	fis, err := r.c.ReadDir(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]*entry, 0, len(fis))
	for _, fi := range fis {
		out = append(out, remoteEntry(fi))
	}
	return out, nil
}

func (r remoteFS) mkdir(ctx context.Context, p string, perm fs.FileMode) error {
	return r.c.Mkdir(ctx, p, perm)
}
func (r remoteFS) mkdirAll(ctx context.Context, p string, perm fs.FileMode) error {
	return r.c.MkdirAll(ctx, p, perm)
}
func (r remoteFS) symlink(ctx context.Context, target, p string) error {
	return r.c.Symlink(ctx, target, p)
}
func (r remoteFS) chmod(ctx context.Context, p string, perm fs.FileMode) error {
	return r.c.Chmod(ctx, p, perm)
}
func (r remoteFS) chtimes(ctx context.Context, p string, mtime time.Time) error {
	return r.c.Utimes(ctx, p, mtime, mtime)
}
func (r remoteFS) remove(ctx context.Context, p string) error    { return r.c.Unlink(ctx, p) }
func (r remoteFS) rmdir(ctx context.Context, p string) error     { return r.c.Rmdir(ctx, p) }
func (r remoteFS) removeAll(ctx context.Context, p string) error { return r.c.RemoveAll(ctx, p) }
func (r remoteFS) hash(ctx context.Context, p string) (string, error) {
	return r.c.Hash(ctx, p)
}

// safeName reports whether a name from a directory listing may be used as
// one component of a destination path on this machine (see rfs.ValidName,
// which also applies the Win32 rules on Windows).
func safeName(n string) bool { return rfs.ValidName(n) }

// dedupKey is the key under which names of one remote listing must be
// unique. Downloads to macOS and Windows land on case-insensitive
// filesystems, where "f" and "F" are one entry, so their names are folded;
// a colliding later entry is dropped. (Unicode normalisation collisions are
// not folded; they are harmless because nothing written by a transfer is
// ever followed through a symlink: files are renamed into place, links
// never replace directories, and chmod/utimes do not follow.)
func dedupKey(f fsys, n string) string {
	if f.remote() && (runtime.GOOS == "darwin" || runtime.GOOS == "windows") {
		return strings.ToLower(n)
	}
	return n
}

// baseName returns the last component of a path as the user wrote it, or ""
// when it has none ("/", ".", "..", "~", "").
func baseName(p Path) string {
	var b string
	if p.Remote {
		b = path.Base(strings.TrimRight(p.Path, "/"))
	} else {
		abs, err := filepath.Abs(p.Path)
		if err != nil {
			return ""
		}
		b = filepath.Base(abs)
	}
	if b == "~" || !safeName(b) {
		return ""
	}
	return b
}

// writeLocalAtomic installs a local file through a temporary file in the
// same directory and a rename, so an interrupted transfer never leaves a
// truncated file under the final name, and an existing symlink at dst is
// replaced rather than written through.
func writeLocalAtomic(dst string, perm fs.FileMode, mtime time.Time, fill func(io.Writer) error) (err error) {
	dir, base := filepath.Split(dst)
	if dir == "" {
		dir = "."
	}
	name := base
	if len(name) > 200 {
		name = name[:200]
	}
	tmp, err := os.CreateTemp(dir, "."+name+".remohop-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := fill(tmp); err != nil {
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(tmp.Name(), mtime, mtime); err != nil {
			return err
		}
	}
	err = os.Rename(tmp.Name(), dst)
	if err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		// Windows will not replace a read-only file.
		if os.Chmod(dst, 0o666) == nil {
			err = os.Rename(tmp.Name(), dst)
		}
	}
	return err
}
