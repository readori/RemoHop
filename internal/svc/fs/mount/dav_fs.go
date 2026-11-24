package mount

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/webdav"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// davAttrTTL is how long attributes (and "does not exist") learned from the
// host are reused. One PROPFIND of a directory makes the WebDAV handler
// stat and open every entry; with the listing cached that costs the host
// one ReadDir instead of a round trip or two per entry. It also bounds
// how stale the view of remote changes can be, as the FUSE mount's kernel
// cache does.
const davAttrTTL = time.Second

// shadowMax bounds the in-memory shadow of desktop metadata files.
const shadowMax = 64 << 20

// davFS is a webdav.FileSystem over an rfs session.
//
// Desktop metadata that file managers scatter over every directory they
// look at (macOS AppleDouble "._name" files and .DS_Store, Windows
// desktop.ini and Thumbs.db) never reaches the host: those names live in
// an in-memory shadow for the lifetime of the mount, so browsing a remote
// source tree does not litter it, and the many lookups for them cost no
// round trip.
type davFS struct {
	root     string
	client   func() *rfs.Client
	readOnly bool

	shadow     webdav.FileSystem
	shadowUsed atomic.Int64

	mu    sync.Mutex
	attrs map[string]davAttr
}

type davAttr struct {
	fi  *davInfo // nil: known not to exist
	exp time.Time
}

func newDavFS(root string, client func() *rfs.Client, readOnly bool) *davFS {
	return &davFS{root: root, client: client, readOnly: readOnly, shadow: webdav.NewMemFS(), attrs: map[string]davAttr{}}
}

// isShadowName reports whether a path names desktop metadata.
func isShadowName(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		switch {
		case strings.HasPrefix(seg, "._"), seg == ".DS_Store", seg == ".Spotlight-V100", seg == ".Trashes",
			seg == ".fseventsd", seg == ".TemporaryItems", seg == ".metadata_never_index",
			strings.EqualFold(seg, "desktop.ini"), strings.EqualFold(seg, "Thumbs.db"):
			return true
		}
	}
	return false
}

// rpath maps a handler path ("/a/b", already cleaned) to the host path.
func (d *davFS) rpath(name string) (string, error) {
	name = path.Clean("/" + name)
	if strings.IndexByte(name, 0) >= 0 {
		return "", os.ErrInvalid
	}
	if name == "/" {
		return d.root, nil
	}
	return strings.TrimRight(d.root, "/") + name, nil
}

// davErr gives an rfs error the shape the WebDAV handler understands: it
// classifies errors with os.IsNotExist and friends, which look for a
// syscall.Errno directly under a PathError.
func davErr(op, name string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	en := rfs.Errno(err)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &os.PathError{Op: op, Path: name, Err: os.ErrNotExist}
	case errors.Is(err, fs.ErrExist):
		return &os.PathError{Op: op, Path: name, Err: os.ErrExist}
	case errors.Is(err, fs.ErrPermission):
		return &os.PathError{Op: op, Path: name, Err: os.ErrPermission}
	}
	return &os.PathError{Op: op, Path: name, Err: en}
}

func (d *davFS) cached(p string) (davAttr, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	a, ok := d.attrs[p]
	if !ok || time.Now().After(a.exp) {
		delete(d.attrs, p)
		return davAttr{}, false
	}
	return a, true
}

func (d *davFS) remember(p string, fi *davInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.attrs) > 100000 {
		d.attrs = map[string]davAttr{}
	}
	d.attrs[p] = davAttr{fi: fi, exp: time.Now().Add(davAttrTTL)}
}

// forget drops everything cached; called after any change made through
// the mount.
func (d *davFS) forget() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attrs = map[string]davAttr{}
}

func opContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, opTimeout)
}

// stat returns the attributes of a host path, following symlinks (WebDAV
// has no links: a link shows as what it points to).
func (d *davFS) stat(ctx context.Context, p string) (*davInfo, error) {
	if a, ok := d.cached(p); ok {
		if a.fi == nil {
			return nil, &os.PathError{Op: "stat", Path: p, Err: os.ErrNotExist}
		}
		return a.fi, nil
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	fi, err := d.client().Stat(ctx, p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			d.remember(p, nil)
		}
		return nil, davErr("stat", p, err)
	}
	di := newDavInfo(fi, path.Base(p))
	d.remember(p, di)
	return di, nil
}

func (d *davFS) writable(op, name string) error {
	if d.readOnly {
		return &os.PathError{Op: op, Path: name, Err: os.ErrPermission}
	}
	return nil
}

// shadowParent makes the directories above a shadow file exist in the
// shadow filesystem.
func (d *davFS) shadowParent(ctx context.Context, name string) {
	dir := path.Dir(path.Clean("/" + name))
	if dir == "/" {
		return
	}
	cur := ""
	for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
		cur += "/" + seg
		d.shadow.Mkdir(ctx, cur, 0o755)
	}
}

func (d *davFS) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	if err := d.writable("mkdir", name); err != nil {
		return err
	}
	if isShadowName(name) {
		d.shadowParent(ctx, name)
		return d.shadow.Mkdir(ctx, name, perm)
	}
	p, err := d.rpath(name)
	if err != nil {
		return err
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	d.forget()
	return davErr("mkdir", name, d.client().Mkdir(ctx, p, 0o777))
}

func (d *davFS) RemoveAll(ctx context.Context, name string) error {
	if err := d.writable("remove", name); err != nil {
		return err
	}
	// Whatever shadow metadata sits at or below the name goes with it.
	d.shadow.RemoveAll(ctx, name)
	if isShadowName(name) {
		return nil
	}
	p, err := d.rpath(name)
	if err != nil {
		return err
	}
	if p == d.root {
		return &os.PathError{Op: "remove", Path: name, Err: os.ErrPermission}
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	d.forget()
	return davErr("remove", name, d.client().RemoveAll(ctx, p))
}

func (d *davFS) Rename(ctx context.Context, oldName, newName string) error {
	if err := d.writable("rename", oldName); err != nil {
		return err
	}
	so, sn := isShadowName(oldName), isShadowName(newName)
	switch {
	case so && sn:
		d.shadowParent(ctx, newName)
		return d.shadow.Rename(ctx, oldName, newName)
	case so != sn:
		// Metadata does not turn into a real file or back (editors that
		// save through a temporary name do not use these names).
		return &os.PathError{Op: "rename", Path: oldName, Err: os.ErrPermission}
	}
	op, err := d.rpath(oldName)
	if err != nil {
		return err
	}
	np, err := d.rpath(newName)
	if err != nil {
		return err
	}
	ctx, cancel := opContext(ctx)
	defer cancel()
	d.forget()
	if err := d.client().Rename(ctx, op, np); err != nil {
		return davErr("rename", oldName, err)
	}
	// Shadow metadata below a renamed directory follows it, best effort.
	d.shadowParent(ctx, newName+"/x")
	d.shadow.Rename(ctx, oldName, newName)
	return nil
}

func (d *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	if isShadowName(name) {
		return d.shadow.Stat(ctx, name)
	}
	p, err := d.rpath(name)
	if err != nil {
		return nil, err
	}
	fi, err := d.stat(ctx, p)
	if err != nil {
		return nil, err
	}
	return fi, nil
}

func (d *davFS) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if writing {
		if err := d.writable("open", name); err != nil {
			return nil, err
		}
	}
	if isShadowName(name) {
		if writing {
			d.shadowParent(ctx, name)
		}
		f, err := d.shadow.OpenFile(ctx, name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &shadowFile{File: f, d: d}, nil
	}
	p, err := d.rpath(name)
	if err != nil {
		return nil, err
	}
	// The handler stores a whole resource (PUT, COPY, or LOCK creating an
	// empty one) by opening with O_CREATE|O_TRUNC: that is a replacement,
	// streamed to the host, which installs the file atomically when it is
	// closed. It also opens existing files read-write merely to look at
	// them (PROPPATCH); such an open must never replace anything, so
	// without O_TRUNC an existing file is opened for reading.
	replace := flag&os.O_TRUNC != 0
	if writing && !replace {
		if _, err := d.stat(ctx, p); err != nil {
			replace = flag&os.O_CREATE != 0 && errors.Is(err, fs.ErrNotExist)
		}
	}
	if replace {
		if fi, err := d.stat(ctx, p); err == nil && fi.IsDir() {
			return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EISDIR}
		}
		if flag&os.O_EXCL != 0 {
			if _, err := d.stat(ctx, p); err == nil {
				return nil, &os.PathError{Op: "open", Path: name, Err: os.ErrExist}
			}
		}
		// A missing parent is reported now rather than at close.
		if _, err := d.stat(ctx, path.Dir(p)); err != nil {
			return nil, err
		}
		return newDavUpload(d, p, name), nil
	}
	fi, err := d.stat(ctx, p)
	if err != nil {
		return nil, err
	}
	// Nothing is opened on the host yet: the handler opens every entry of
	// a listing just to describe it.
	return &davFile{d: d, p: p, fi: fi}, nil
}

// davInfo is a host file as the WebDAV handler sees it. It answers the
// handler's content type and ETag questions itself; by default the handler
// would open and read each file to sniff its type.
type davInfo struct {
	name  string
	size  int64
	mode  fs.FileMode
	mtime time.Time
}

func newDavInfo(fi *rfs.FileInfo, name string) *davInfo {
	return &davInfo{name: name, size: fi.Size(), mode: fi.Mode(), mtime: fi.ModTime()}
}

func (i *davInfo) Name() string       { return i.name }
func (i *davInfo) Size() int64        { return i.size }
func (i *davInfo) Mode() fs.FileMode  { return i.mode }
func (i *davInfo) ModTime() time.Time { return i.mtime }
func (i *davInfo) IsDir() bool        { return i.mode.IsDir() }
func (i *davInfo) Sys() any           { return nil }

// ContentType implements webdav.ContentTyper.
func (i *davInfo) ContentType(context.Context) (string, error) {
	if t := mime.TypeByExtension(path.Ext(i.name)); t != "" {
		return t, nil
	}
	return "application/octet-stream", nil
}

// davFile is a host file or directory opened for reading. The host handle
// is opened on first use.
type davFile struct {
	d  *davFS
	p  string
	fi *davInfo

	mu     sync.Mutex
	f      *rfs.File
	pos    int64
	ra     readahead
	listed bool
	ents   []os.FileInfo
}

func (f *davFile) Stat() (os.FileInfo, error) { return f.fi, nil }

func (f *davFile) Write([]byte) (int, error) {
	return 0, &os.PathError{Op: "write", Path: f.p, Err: os.ErrPermission}
}

func (f *davFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.f != nil {
		f.ra.invalidate()
		err := f.f.Close()
		f.f = nil
		return davErr("close", f.p, err)
	}
	return nil
}

func (f *davFile) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += f.pos
	case io.SeekEnd:
		offset += f.fi.size
	default:
		return 0, os.ErrInvalid
	}
	if offset < 0 {
		return 0, os.ErrInvalid
	}
	f.pos = offset
	return offset, nil
}

func (f *davFile) Read(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fi.IsDir() {
		return 0, &os.PathError{Op: "read", Path: f.p, Err: syscall.EISDIR}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if f.f == nil {
		h, err := f.d.client().Open(ctx, f.p)
		if err != nil {
			return 0, davErr("open", f.p, err)
		}
		f.f = h
		f.ra.window = raMin
		// Downloads are sequential from the first byte on.
		f.ra.seq, f.ra.nextOff = 2, f.pos
	}
	n, err := f.ra.read(ctx, f.f, b, f.pos)
	f.pos += int64(n)
	if err != nil {
		return n, davErr("read", f.p, err)
	}
	if n == 0 && len(b) > 0 {
		return 0, io.EOF
	}
	return n, nil
}

// Readdir lists the directory with one host ReadDir, and remembers every
// entry's attributes for the per-entry questions that follow.
func (f *davFile) Readdir(count int) ([]os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.fi.IsDir() {
		return nil, &os.PathError{Op: "readdir", Path: f.p, Err: syscall.ENOTDIR}
	}
	if !f.listed {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		c := f.d.client()
		ents, err := c.ReadDir(ctx, f.p)
		if err != nil {
			return nil, davErr("readdir", f.p, err)
		}
		base := strings.TrimRight(f.p, "/")
		for _, e := range ents {
			if isShadowName(e.Name()) {
				continue // stray metadata on the host stays out of sight
			}
			child := base + "/" + e.Name()
			var di *davInfo
			switch {
			case e.IsSymlink():
				// Show what the link points to; hide dangling links.
				t, err := c.Stat(ctx, child)
				if err != nil {
					continue
				}
				di = newDavInfo(t, e.Name())
			case e.IsDir() || e.IsRegular():
				di = newDavInfo(e, e.Name())
			default:
				continue // devices, sockets and pipes mean nothing here
			}
			f.d.remember(child, di)
			f.ents = append(f.ents, di)
		}
		f.listed = true
	}
	if count <= 0 {
		out := f.ents
		f.ents = nil
		return out, nil
	}
	if len(f.ents) == 0 {
		return nil, io.EOF
	}
	n := min(count, len(f.ents))
	out := f.ents[:n]
	f.ents = f.ents[n:]
	return out, nil
}

// davUpload is a file being stored: writes stream to the host on a bulk
// stream and the file is installed, atomically, by Close.
type davUpload struct {
	d    *davFS
	p    string
	name string

	pw     *io.PipeWriter
	done   chan struct{}
	fi     *rfs.FileInfo
	err    error
	closed bool
	n      int64
}

func newDavUpload(d *davFS, p, name string) *davUpload {
	pr, pw := io.Pipe()
	u := &davUpload{d: d, p: p, name: name, pw: pw, done: make(chan struct{})}
	go func() {
		defer close(u.done)
		// No deadline on the whole transfer: it is as long as the file.
		// A dead session ends it through the stream.
		u.fi, u.err = d.client().Upload(context.Background(), p, pr, rfs.PutOptions{})
		pr.CloseWithError(u.err) // unblock a writer if the upload failed
	}()
	return u
}

func (u *davUpload) Write(b []byte) (int, error) {
	n, err := u.pw.Write(b)
	u.n += int64(n)
	if err != nil {
		<-u.done
		if u.err != nil {
			return n, davErr("write", u.name, u.err)
		}
	}
	return n, err
}

func (u *davUpload) Close() error {
	if u.closed {
		return nil
	}
	u.closed = true
	u.pw.Close()
	<-u.done
	u.d.forget()
	return davErr("write", u.name, u.err)
}

func (u *davUpload) Read([]byte) (int, error) {
	return 0, &os.PathError{Op: "read", Path: u.name, Err: os.ErrPermission}
}

// Seek accepts only seeks that stay at the write position: an upload is
// one sequential stream.
func (u *davUpload) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		offset += u.n
	}
	if offset != u.n {
		return 0, &os.PathError{Op: "seek", Path: u.name, Err: os.ErrInvalid}
	}
	return u.n, nil
}

func (u *davUpload) Readdir(int) ([]os.FileInfo, error) {
	return nil, &os.PathError{Op: "readdir", Path: u.name, Err: syscall.ENOTDIR}
}

func (u *davUpload) Stat() (os.FileInfo, error) {
	if u.closed && u.err == nil && u.fi != nil {
		return newDavInfo(u.fi, path.Base(u.p)), nil
	}
	return &davInfo{name: path.Base(u.p), size: u.n, mode: 0o644, mtime: time.Now()}, nil
}

// shadowFile bounds what the in-memory shadow can grow to.
type shadowFile struct {
	webdav.File
	d *davFS
}

func (s *shadowFile) Write(b []byte) (int, error) {
	if s.d.shadowUsed.Add(int64(len(b))) > shadowMax {
		s.d.shadowUsed.Add(-int64(len(b)))
		return 0, syscall.ENOSPC
	}
	return s.File.Write(b)
}
