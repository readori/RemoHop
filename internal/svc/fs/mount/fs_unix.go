//go:build linux || darwin

package mount

import (
	"context"
	"errors"
	"hash/maphash"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

const (
	// coalesceMax is how many sequential written bytes a handle collects
	// before sending them; maxWriteInflight bounds the sends in flight.
	coalesceMax      = rfs.MaxIO
	maxWriteInflight = 8
	// blksize is the st_blksize reported to applications (their preferred
	// I/O size).
	blksize = 128 << 10
)

// openMask keeps the open(2) flags that mean something to the host; FUSE
// also passes kernel-internal bits.
const openMask = syscall.O_ACCMODE | syscall.O_APPEND | syscall.O_CREAT | syscall.O_EXCL |
	syscall.O_TRUNC | syscall.O_SYNC

// mountFS is the state shared by every node of one mount.
type mountFS struct {
	root     string // absolute remote root
	rootDev  uint64
	seed     maphash.Seed // keys directory inode numbers
	client   atomic.Pointer[rfs.Client]
	uid, gid uint32 // local owner reported for the host user's files
	hostUID  uint32
	hostGID  uint32
	readOnly bool
}

func (m *mountFS) c() *rfs.Client { return m.client.Load() }

func (m *mountFS) join(rel string) string {
	switch {
	case rel == "":
		return m.root
	case strings.HasSuffix(m.root, "/"):
		return m.root + rel
	}
	return m.root + "/" + rel
}

// ino gives a file its inode number. Directories get a hash of their path,
// keyed with a per-mount random seed: inode numbers are how go-fuse
// recognises a node it already knows, and a directory that reported the
// same host identity as one of its ancestors (a hostile host, or a bind
// mount of an ancestor inside the tree) would otherwise be linked under its
// own descendant and make every path computation loop forever. Other files
// keep the host's (dev, ino), mixed so trees spanning several host
// filesystems do not collide and masked so the root device's numbers come
// through unchanged (git compares them; hard links share them). They cannot
// have children, so aliasing among them is harmless. The two kinds live in
// disjoint ranges (bit 62), and neither can produce the reserved ^0.
func (m *mountFS) ino(a *rfs.Attr, path string) uint64 {
	if a.Mode&syscall.S_IFMT == syscall.S_IFDIR || a.Ino == 0 {
		return m.pathIno(path)
	}
	swap := func(d uint64) uint64 { return d<<32 | d>>32 }
	if i := ((swap(a.Dev) ^ swap(m.rootDev)) ^ a.Ino) & (1<<62 - 1); i != 0 {
		return i
	}
	return 1
}

func (m *mountFS) pathIno(path string) uint64 {
	return maphash.String(m.seed, path)&(1<<62-1) | 1<<62
}

func (m *mountFS) fillAttr(a *rfs.Attr, path string, out *fuse.Attr) {
	out.Ino = m.ino(a, path)
	out.Size = uint64(a.Size)
	out.Blocks = uint64(a.Blocks)
	if out.Blocks == 0 && a.Size > 0 {
		out.Blocks = (uint64(a.Size) + 511) / 512
	}
	out.Mode = a.Mode
	out.Nlink = max(a.Nlink, 1)
	out.Blksize = blksize
	at, mt, ct := time.Unix(0, a.Atime), time.Unix(0, a.Mtime), time.Unix(0, a.Ctime)
	if a.Atime == 0 {
		at = mt
	}
	if a.Ctime == 0 {
		ct = mt
	}
	out.SetTimes(&at, &mt, &ct)
	out.Uid, out.Gid = a.Uid, a.Gid
	if a.Uid == m.hostUID {
		out.Uid = m.uid
	}
	if a.Gid == m.hostGID {
		out.Gid = m.gid
	}
}

// toErrno maps a client error to the errno FUSE returns.
func toErrno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	}
	return rfs.Errno(err)
}

func opCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, opTimeout)
}

// node is one file or directory. Nodes are path-based: the remote path is
// recomputed from the tree on every operation, so renames done through the
// mount need no bookkeeping.
type node struct {
	fs.Inode
	m *mountFS

	mu   sync.Mutex
	open map[*fileHandle]struct{}
	// seen is the (mtime, size) observed at the last open; an unchanged
	// file keeps its kernel page cache across opens.
	seen [2]int64
}

var (
	_ fs.NodeLookuper       = (*node)(nil)
	_ fs.NodeGetattrer      = (*node)(nil)
	_ fs.NodeSetattrer      = (*node)(nil)
	_ fs.NodeOpener         = (*node)(nil)
	_ fs.NodeCreater        = (*node)(nil)
	_ fs.NodeMkdirer        = (*node)(nil)
	_ fs.NodeRmdirer        = (*node)(nil)
	_ fs.NodeUnlinker       = (*node)(nil)
	_ fs.NodeRenamer        = (*node)(nil)
	_ fs.NodeSymlinker      = (*node)(nil)
	_ fs.NodeReadlinker     = (*node)(nil)
	_ fs.NodeLinker         = (*node)(nil)
	_ fs.NodeStatfser       = (*node)(nil)
	_ fs.NodeAccesser       = (*node)(nil)
	_ fs.NodeOpendirHandler = (*node)(nil)
	_ fs.FileReaddirenter   = (*dirHandle)(nil)
	_ fs.FileLookuper       = (*dirHandle)(nil)
	_ fs.FileSeekdirer      = (*dirHandle)(nil)
	_ fs.FileFsyncdirer     = (*dirHandle)(nil)
	_ fs.FileReader         = (*fileHandle)(nil)
	_ fs.FileWriter         = (*fileHandle)(nil)
	_ fs.FileFlusher        = (*fileHandle)(nil)
	_ fs.FileFsyncer        = (*fileHandle)(nil)
	_ fs.FileReleaser       = (*fileHandle)(nil)
	_ fs.FileGetattrer      = (*fileHandle)(nil)
)

// rel is the node's path relative to the mount root; ok is false for a
// node that has been unlinked (it is only reachable through open handles).
func (n *node) rel() (string, bool) {
	p := n.Path(n.Root())
	if strings.HasPrefix(p, ".go-fuse.") {
		return "", false
	}
	return p, true
}

func (n *node) rpath() (string, syscall.Errno) {
	rel, ok := n.rel()
	if !ok {
		return "", syscall.ENOENT
	}
	return n.m.join(rel), 0
}

func (n *node) childPath(name string) (string, syscall.Errno) {
	p, errno := n.rpath()
	if errno != 0 {
		return "", errno
	}
	if strings.HasSuffix(p, "/") {
		return p + name, 0
	}
	return p + "/" + name, 0
}

func (n *node) newChild(ctx context.Context, a *rfs.Attr, path string) *fs.Inode {
	return n.NewInode(ctx, &node{m: n.m}, fs.StableAttr{Mode: a.Mode & syscall.S_IFMT, Ino: n.m.ino(a, path)})
}

// entry looks up a child that an operation just created or changed and
// fills out.
func (n *node) entry(ctx context.Context, p string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	fi, err := n.m.c().Lstat(ctx, p)
	if err != nil {
		return nil, toErrno(err)
	}
	n.m.fillAttr(fi.Attr(), p, &out.Attr)
	return n.newChild(ctx, fi.Attr(), p), 0
}

// Access lets the host decide: permission checks happen there, with the
// host user's credentials, when the operation is attempted.
func (n *node) Access(ctx context.Context, mask uint32) syscall.Errno { return 0 }

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p, errno := n.childPath(name)
	if errno != 0 {
		return nil, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return n.entry(ctx, p, out)
}

func (n *node) handles() []*fileHandle {
	n.mu.Lock()
	defer n.mu.Unlock()
	hs := make([]*fileHandle, 0, len(n.open))
	for h := range n.open {
		hs = append(hs, h)
	}
	return hs
}

// drainAll pushes buffered writes of every open handle to the host, so
// attributes read from the host include them.
func (n *node) drainAll() {
	for _, h := range n.handles() {
		h.mu.Lock()
		h.drainLocked()
		h.mu.Unlock()
	}
}

func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	if h, ok := fh.(*fileHandle); ok {
		return h.Getattr(ctx, out)
	}
	n.drainAll()
	p, errno := n.rpath()
	if errno != 0 {
		return errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	fi, err := n.m.c().Lstat(ctx, p)
	if err != nil {
		return toErrno(err)
	}
	n.m.fillAttr(fi.Attr(), p, &out.Attr)
	return 0
}

func (n *node) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.m.readOnly {
		return syscall.EROFS
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	c := n.m.c()
	h, _ := fh.(*fileHandle)
	// Pending writes first: they would otherwise land after a truncate,
	// or bump an mtime just set (cp -p, touch -d).
	n.drainAll()
	p, errno := n.rpath()
	if errno != 0 && h == nil {
		return errno
	}
	if uid, ok := in.GetUID(); ok && uid != n.m.uid {
		return syscall.EPERM
	}
	if gid, ok := in.GetGID(); ok && gid != n.m.gid {
		return syscall.EPERM
	}
	if mode, ok := in.GetMode(); ok {
		if errno != 0 {
			return errno
		}
		if err := c.Chmod(ctx, p, rfs.ModeFromUnix(mode)); err != nil {
			return toErrno(err)
		}
	}
	if size, ok := in.GetSize(); ok {
		var err error
		if h != nil {
			err = h.f.Truncate(ctx, int64(size))
		} else {
			err = c.Truncate(ctx, p, int64(size))
		}
		for _, oh := range n.handles() {
			oh.ra.invalidate()
		}
		if err != nil {
			return toErrno(err)
		}
	}
	at, aok := in.GetATime()
	mt, mok := in.GetMTime()
	if aok || mok {
		if errno != 0 {
			return errno
		}
		if !aok {
			at = time.Time{}
		}
		if !mok {
			mt = time.Time{}
		}
		var err error
		if n.StableAttr().Mode == syscall.S_IFLNK {
			err = c.Lutimes(ctx, p, at, mt)
		} else {
			err = c.Utimes(ctx, p, at, mt)
		}
		if err != nil {
			return toErrno(err)
		}
	}
	if h != nil {
		return h.Getattr(ctx, out)
	}
	fi, err := c.Lstat(ctx, p)
	if err != nil {
		return toErrno(err)
	}
	n.m.fillAttr(fi.Attr(), p, &out.Attr)
	return 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.m.readOnly && flags&syscall.O_ACCMODE != syscall.O_RDONLY {
		return nil, 0, syscall.EROFS
	}
	p, errno := n.rpath()
	if errno != 0 {
		return nil, 0, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	f, err := n.m.c().OpenFile(ctx, p, int(flags&openMask), 0)
	if err != nil {
		return nil, 0, toErrno(err)
	}
	h := n.addHandle(f, flags)
	var fuseFlags uint32
	if fi := f.Info(); fi != nil {
		key := [2]int64{fi.Attr().Mtime, fi.Size()}
		n.mu.Lock()
		if key == n.seen && flags&syscall.O_TRUNC == 0 {
			fuseFlags |= fuse.FOPEN_KEEP_CACHE
		}
		n.seen = key
		n.mu.Unlock()
	}
	return h, fuseFlags, 0
}

func (n *node) addHandle(f *rfs.File, flags uint32) *fileHandle {
	h := &fileHandle{n: n, f: f, appendMode: flags&syscall.O_APPEND != 0, sem: make(chan struct{}, maxWriteInflight)}
	h.ra.window = raMin
	n.mu.Lock()
	if n.open == nil {
		n.open = map[*fileHandle]struct{}{}
	}
	n.open[h] = struct{}{}
	n.mu.Unlock()
	return h
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.m.readOnly {
		return nil, nil, 0, syscall.EROFS
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return nil, nil, 0, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	f, err := n.m.c().OpenFile(ctx, p, int(flags&openMask)|os.O_CREATE, rfs.ModeFromUnix(mode&0o7777))
	if err != nil {
		return nil, nil, 0, toErrno(err)
	}
	fi := f.Info()
	if fi == nil {
		if fi, err = f.Stat(ctx); err != nil {
			f.Close()
			return nil, nil, 0, toErrno(err)
		}
	}
	n.m.fillAttr(fi.Attr(), p, &out.Attr)
	child := n.newChild(ctx, fi.Attr(), p)
	cn := child.Operations().(*node)
	return child, cn.addHandle(f, flags), 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.m.readOnly {
		return nil, syscall.EROFS
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return nil, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	if err := n.m.c().Mkdir(ctx, p, rfs.ModeFromUnix(mode&0o7777)); err != nil {
		return nil, toErrno(err)
	}
	return n.entry(ctx, p, out)
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.m.readOnly {
		return syscall.EROFS
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return toErrno(n.m.c().Rmdir(ctx, p))
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.m.readOnly {
		return syscall.EROFS
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return toErrno(n.m.c().Unlink(ctx, p))
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.m.readOnly {
		return syscall.EROFS
	}
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	from, errno := n.childPath(name)
	if errno != 0 {
		return errno
	}
	to, errno := np.childPath(newName)
	if errno != 0 {
		return errno
	}
	rf, ok := renameFlags(flags)
	if !ok {
		return syscall.EINVAL
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return toErrno(n.m.c().RenameFlags(ctx, from, to, rf))
}

func (n *node) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.m.readOnly {
		return nil, syscall.EROFS
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return nil, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	if err := n.m.c().Symlink(ctx, target, p); err != nil {
		return nil, toErrno(err)
	}
	return n.entry(ctx, p, out)
}

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	p, errno := n.rpath()
	if errno != 0 {
		return nil, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	t, err := n.m.c().Readlink(ctx, p)
	if err != nil {
		return nil, toErrno(err)
	}
	return []byte(t), 0
}

func (n *node) Link(ctx context.Context, target fs.InodeEmbedder, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.m.readOnly {
		return nil, syscall.EROFS
	}
	tn, ok := target.(*node)
	if !ok {
		return nil, syscall.EXDEV
	}
	tp, errno := tn.rpath()
	if errno != 0 {
		return nil, errno
	}
	p, errno := n.childPath(name)
	if errno != 0 {
		return nil, errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	if err := n.m.c().Link(ctx, tp, p); err != nil {
		return nil, toErrno(err)
	}
	return n.entry(ctx, p, out)
}

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	ctx, cancel := opCtx(ctx)
	defer cancel()
	sf, err := n.m.c().StatFS(ctx, n.m.root)
	if err != nil {
		return toErrno(err)
	}
	out.Blocks, out.Bfree, out.Bavail = sf.Blocks, sf.Bfree, sf.Bavail
	out.Files, out.Ffree = sf.Files, sf.Ffree
	out.Bsize, out.Frsize, out.NameLen = sf.Bsize, sf.Frsize, sf.NameLen
	if out.Frsize == 0 {
		out.Frsize = out.Bsize
	}
	return 0
}

func (n *node) OpendirHandle(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	d := &dirHandle{n: n}
	if errno := d.load(ctx); errno != 0 {
		return nil, 0, errno
	}
	return d, 0, 0
}

// dirHandle is an open directory. The whole listing, with attributes, is
// fetched at opendir, so READDIRPLUS needs no further round trips: the
// kernel's per-entry lookups are answered from it (FileLookuper).
type dirHandle struct {
	n    *node
	path string
	ents []*rfs.FileInfo
	pos  int // 0 ".", 1 "..", then ents
}

func (d *dirHandle) load(ctx context.Context) syscall.Errno {
	p, errno := d.n.rpath()
	if errno != 0 {
		return errno
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	ents, err := d.n.m.c().ReadDir(ctx, p)
	if err != nil {
		return toErrno(err)
	}
	d.path, d.ents, d.pos = p, ents, 0
	return 0
}

func (d *dirHandle) entPath(fi *rfs.FileInfo) string {
	if strings.HasSuffix(d.path, "/") {
		return d.path + fi.Name()
	}
	return d.path + "/" + fi.Name()
}

func (d *dirHandle) Readdirent(ctx context.Context) (*fuse.DirEntry, syscall.Errno) {
	i := d.pos
	if i >= len(d.ents)+2 {
		return nil, 0
	}
	d.pos++
	de := &fuse.DirEntry{Off: uint64(i + 1)}
	switch i {
	case 0, 1:
		de.Name = "."
		if i == 1 {
			de.Name = ".."
		}
		de.Mode = syscall.S_IFDIR
		de.Ino = d.n.StableAttr().Ino
	default:
		fi := d.ents[i-2]
		a := fi.Attr()
		de.Name, de.Mode, de.Ino = fi.Name(), a.Mode&syscall.S_IFMT, d.n.m.ino(a, d.entPath(fi))
	}
	return de, 0
}

func (d *dirHandle) Seekdir(ctx context.Context, off uint64) syscall.Errno {
	if off == 0 {
		// rewinddir: start over with a fresh listing.
		return d.load(ctx)
	}
	if off > uint64(len(d.ents)+2) {
		return syscall.EINVAL
	}
	d.pos = int(off)
	return 0
}

func (d *dirHandle) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// The name is the last one Readdirent produced.
	if i := d.pos - 3; i >= 0 && i < len(d.ents) && d.ents[i].Name() == name {
		fi := d.ents[i]
		p := d.entPath(fi)
		d.n.m.fillAttr(fi.Attr(), p, &out.Attr)
		return d.n.newChild(ctx, fi.Attr(), p), 0
	}
	return d.n.Lookup(ctx, name, out)
}

func (d *dirHandle) Fsyncdir(ctx context.Context, flags uint32) syscall.Errno { return 0 }

// fileHandle is an open remote file. Sequential writes are collected into
// MaxIO-sized sends that run asynchronously (a few at a time, so a fast
// writer is limited by bandwidth rather than round trips); their errors
// surface at the next write, flush (close) or fsync. Sequential reads are
// served from a growing readahead window.
type fileHandle struct {
	n          *node
	f          *rfs.File
	appendMode bool

	mu       sync.Mutex
	wbuf     []byte
	woff     int64
	wend     int64 // end of the last accepted write
	inflight sync.WaitGroup
	sem      chan struct{}
	errMu    sync.Mutex
	werr     error
	wrote    bool

	ra readahead
}

func (h *fileHandle) setErr(err error) {
	h.errMu.Lock()
	defer h.errMu.Unlock()
	if h.werr == nil {
		h.werr = err
	}
}

func (h *fileHandle) err(clear bool) error {
	h.errMu.Lock()
	defer h.errMu.Unlock()
	err := h.werr
	if clear {
		h.werr = nil
	}
	return err
}

// startFlushLocked sends the buffered bytes asynchronously.
func (h *fileHandle) startFlushLocked() {
	if len(h.wbuf) == 0 {
		return
	}
	buf, off := h.wbuf, h.woff
	h.wbuf = nil
	if h.appendMode {
		// Appends must reach the host in order.
		h.inflight.Wait()
	}
	h.sem <- struct{}{}
	h.inflight.Add(1)
	go func() {
		defer h.inflight.Done()
		defer func() { <-h.sem }()
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		if _, err := h.f.WriteAt(ctx, buf, off); err != nil {
			h.setErr(err)
		}
	}()
}

// drainLocked sends everything buffered and waits for all sends.
func (h *fileHandle) drainLocked() {
	h.startFlushLocked()
	h.inflight.Wait()
}

func (h *fileHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if h.n.m.readOnly {
		return 0, syscall.EROFS
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.err(true); err != nil {
		return 0, toErrno(err)
	}
	h.ra.invalidate()
	h.wrote = true
	if off != h.wend || len(h.wbuf) == 0 {
		// Not a continuation: everything earlier must land first so
		// overlapping writes keep their order.
		if off != h.wend {
			h.drainLocked()
		}
		h.woff = off
	}
	h.wbuf = append(h.wbuf, data...)
	h.wend = off + int64(len(data))
	if len(h.wbuf) >= coalesceMax {
		h.startFlushLocked()
	}
	return uint32(len(data)), 0
}

func (h *fileHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	// Writes buffered on any handle of this file must be visible.
	h.n.drainAll()
	ctx, cancel := opCtx(ctx)
	defer cancel()
	n, err := h.ra.read(ctx, h.f, dest, off)
	if err != nil {
		return nil, toErrno(err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *fileHandle) Flush(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drainLocked()
	return toErrno(h.err(true))
}

func (h *fileHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.drainLocked()
	if err := h.err(true); err != nil {
		return toErrno(err)
	}
	ctx, cancel := opCtx(ctx)
	defer cancel()
	return toErrno(h.f.Sync(ctx))
}

func (h *fileHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	h.drainLocked()
	h.mu.Unlock()
	n := h.n
	n.mu.Lock()
	delete(n.open, h)
	if h.wrote {
		n.seen = [2]int64{}
	}
	n.mu.Unlock()
	h.ra.invalidate()
	return toErrno(h.f.Close())
}

func (h *fileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	h.mu.Lock()
	h.drainLocked()
	h.mu.Unlock()
	ctx, cancel := opCtx(ctx)
	defer cancel()
	fi, err := h.f.Stat(ctx)
	if err != nil {
		return toErrno(err)
	}
	p, _ := h.n.rpath()
	h.n.m.fillAttr(fi.Attr(), p, &out.Attr)
	return 0
}
