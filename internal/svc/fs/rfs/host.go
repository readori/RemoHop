package rfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

func init() {
	Register(peer.HostServices)
}

// Register adds the fs host services to reg. init registers them in
// peer.HostServices; tests use it to build custom registries.
func Register(reg *peer.Registry) {
	for name, h := range Handlers() {
		reg.Handle(name, h)
	}
}

// Handlers returns the fs host handlers by service name, for callers that
// wrap them (tests waiting for host-side work to finish, for example).
func Handlers() map[string]peer.Handler {
	return map[string]peer.Handler{ServiceControl: serveControl, ServiceGet: serveGet, ServicePut: servePut}
}

// resolve maps a client-supplied path to a host path. "~" and "~/..." mean
// the host user's home directory and relative paths are relative to it;
// nothing else is interpreted (no "~user", no environment variables, no
// lexical cleaning that could change what a ".." after a symlink means).
// The host serves with the user's own privileges: a client with full access
// may name any path that user may access. A session on a grant is confined
// to the grant's roots: the guard decides whether it may act on the path,
// and placeFor where the operation happens (below a pinned root, through
// its os.Root handle).
func resolve(p string) (string, error) {
	if strings.IndexByte(p, 0) >= 0 {
		return "", syscall.EINVAL
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = string(filepath.Separator)
	}
	switch {
	case p == "" || p == "~":
		return home, nil
	case strings.HasPrefix(p, "~/"):
		return home + p[1:], nil
	case filepath.IsAbs(p):
		return p, nil
	}
	return filepath.Join(home, p), nil
}

type hfile struct {
	f      *os.File
	dir    bool
	append bool
	at     string     // the host path a directory was opened at, for its entries' links
	mu     sync.Mutex // serialises directory reads
}

// hsession is the per-control-stream host state.
type hsession struct {
	mu      sync.Mutex
	handles map[uint64]*hfile
	next    uint64
	// guard is the session's grant, nil for full access; ctx is the
	// stream's, for approvals the guard may wait for. scope is the
	// grant's file scope as it was when the stream started: if the grant
	// is reissued meanwhile, the stream's operations are refused.
	guard grant.Guard
	scope *grant.Scope
	ctx   context.Context
}

// place maps p to where an operation acts (see placeFor).
func (s *hsession) place(p string, a grant.Access, follow bool) (loc, error) {
	return placeFor(s.guard, s.scope, p, a, follow)
}

func (s *hsession) add(hf *hfile) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.handles) >= maxHandles {
		return 0, syscall.EMFILE
	}
	s.next++
	s.handles[s.next] = hf
	return s.next, nil
}

func (s *hsession) get(h uint64) (*hfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hf := s.handles[h]
	if hf == nil {
		return nil, syscall.EBADF
	}
	return hf, nil
}

func (s *hsession) remove(h uint64) *hfile {
	s.mu.Lock()
	defer s.mu.Unlock()
	hf := s.handles[h]
	delete(s.handles, h)
	return hf
}

func (s *hsession) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, hf := range s.handles {
		hf.f.Close()
		delete(s.handles, h)
	}
}

func serveControl(ctx context.Context, c *peer.Conn, st peer.Stream, raw json.RawMessage) error {
	var or openReq
	if err := peer.DecodeReq(raw, &or); err != nil {
		return err
	}
	sess := &hsession{handles: map[uint64]*hfile{}, guard: grant.Of(c), ctx: ctx}
	defer sess.closeAll()
	if sess.guard != nil {
		var err error
		if sess.scope, err = scopeOf(sess.guard); err != nil {
			return err
		}
	}

	var (
		wmu sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, maxInflight)
	)
	defer wg.Wait()
	send := func(id uint32, rep *reply, data []byte) {
		typ := stOK
		if rep.Code != "" {
			typ = stErr
		}
		var meta []byte
		if !isEmpty(rep) {
			meta, _ = json.Marshal(rep)
		}
		buf := appendMsg(typ, id, meta, data)
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := st.Write(buf); err != nil {
			st.Reset()
		}
	}
	for {
		typ, payload, err := wire.ReadFrame(st, nil)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		id, meta, data, err := parseMsg(payload)
		if err != nil {
			st.Reset()
			return err
		}
		var req request
		if len(meta) > maxMeta || len(data) > MaxIO {
			send(id, &reply{Code: "E2BIG"}, nil)
			continue
		}
		if len(meta) > 0 {
			if err := json.Unmarshal(meta, &req); err != nil {
				send(id, &reply{Code: "EINVAL", Message: "bad request: " + err.Error()}, nil)
				continue
			}
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			rep, out := sess.do(typ, &req, data)
			send(id, rep, out)
		}()
	}
}

// isEmpty reports a reply without metadata (reads, plain successes), which
// is sent with no JSON at all.
func isEmpty(r *reply) bool {
	return r.Attr == nil && r.Entries == nil && r.Handle == 0 && !r.EOF && r.N == 0 &&
		r.Str == "" && !r.TooBig && r.StatFS == nil && r.Info == nil && r.Code == ""
}

func errReply(err error) *reply {
	code, msg := errorCode(err)
	return &reply{Code: code, Message: msg}
}

// do executes one request. It returns the reply metadata and raw data.
func (s *hsession) do(op byte, r *request, data []byte) (*reply, []byte) {
	rep, out, err := s.dispatch(op, r, data)
	if err != nil {
		return errReply(err), nil
	}
	if rep == nil {
		rep = &reply{}
	}
	return rep, out
}

func (s *hsession) dispatch(op byte, r *request, data []byte) (*reply, []byte, error) {
	// Handle-based operations first: they take no path.
	switch op {
	case opInfo:
		home, _ := resolve("~")
		uid, gid := hostIDs()
		return &reply{Info: &Info{Home: home, OS: runtime.GOOS, UID: uid, GID: gid, Umask: uint32(Umask())}}, nil, nil
	case opReadDir:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		return s.readDir(r.Handle, hf)
	case opClose:
		hf := s.remove(r.Handle)
		if hf == nil {
			return nil, nil, syscall.EBADF
		}
		return nil, nil, hf.f.Close()
	case opRead:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		if r.N < 0 || r.N > MaxIO || r.Off < 0 {
			return nil, nil, syscall.EINVAL
		}
		if hf.dir {
			return nil, nil, syscall.EISDIR // what read(2) says; not every OS does
		}
		buf := make([]byte, r.N)
		n, err := hf.f.ReadAt(buf, r.Off)
		if err != nil && err != io.EOF {
			return nil, nil, err
		}
		return &reply{EOF: n < r.N}, buf[:n], nil
	case opWrite:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		var n int
		if hf.append {
			// Go refuses WriteAt on O_APPEND files; the kernel
			// places each append write at the current end anyway.
			n, err = hf.f.Write(data)
		} else {
			if r.Off < 0 {
				return nil, nil, syscall.EINVAL
			}
			n, err = hf.f.WriteAt(data, r.Off)
		}
		if err != nil {
			return nil, nil, err
		}
		return &reply{N: n}, nil, nil
	case opFsync:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, hf.f.Sync()
	case opFstat:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		fi, err := hf.f.Stat()
		if err != nil {
			return nil, nil, err
		}
		a := attrFromInfo(fi)
		return &reply{Attr: &a}, nil, nil
	case opFtruncate:
		hf, err := s.get(r.Handle)
		if err != nil {
			return nil, nil, err
		}
		if r.Size < 0 {
			return nil, nil, syscall.EINVAL
		}
		return nil, nil, hf.f.Truncate(r.Size)
	}

	p, err := resolve(r.Path)
	if err != nil {
		return nil, nil, err
	}
	var p2 string
	switch op {
	case opRename, opLink, opSymlink:
		if p2, err = resolve(r.Path2); err != nil {
			return nil, nil, err
		}
	}
	if s.guard != nil {
		if name, a, ok := opAccess(op, r); ok {
			if err := guarded(s.ctx, s.guard, name, a, p); err != nil {
				return nil, nil, err
			}
		}
		switch op {
		case opRename, opLink, opSymlink:
			// The second path is created (or replaced) too.
			if err := guarded(s.ctx, s.guard, "create", grant.Write, p2); err != nil {
				return nil, nil, err
			}
		}
	}
	// The guard has allowed the operation on p; l is where it happens.
	// Under a grant that is below a pinned root, through its handle.
	var l loc
	if _, a, ok := opAccess(op, r); ok {
		if l, err = s.place(p, a, followsLink(op, r)); err != nil {
			return nil, nil, err
		}
		defer l.release()
	}
	switch op {
	case opStat, opLstat:
		var fi fs.FileInfo
		if op == opStat {
			fi, err = l.stat()
		} else {
			fi, err = l.lstat()
		}
		if err != nil {
			return nil, nil, err
		}
		a := attrFromInfo(fi)
		if op == opLstat && fi.Mode()&fs.ModeSymlink != 0 {
			a.Target, _ = l.readlink()
		}
		return &reply{Attr: &a}, nil, nil
	case opOpenDir:
		f, err := l.open(os.O_RDONLY, 0)
		if err != nil {
			return nil, nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		if !fi.IsDir() {
			f.Close()
			return nil, nil, errNotDir
		}
		hf := &hfile{f: f, dir: true, at: p}
		h, err := s.add(hf)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return s.readDir(h, hf)
	case opOpen:
		flags, err := hostFlags(r.Flags)
		if err != nil {
			return nil, nil, err
		}
		f, err := l.open(flags, ModeFromUnix(r.Mode)&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
		if err != nil {
			return nil, nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		h, err := s.add(&hfile{f: f, dir: fi.IsDir(), append: r.Flags&wAppend != 0})
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		a := attrFromInfo(fi)
		return &reply{Handle: h, Attr: &a}, nil, nil
	case opMkdir:
		return nil, nil, l.mkdir(ModeFromUnix(r.Mode&0o7777), r.All)
	case opRemove:
		return nil, nil, l.remove(r.Kind)
	case opRename:
		l2, err := s.place(p2, grant.Write, false)
		if err != nil {
			return nil, nil, err
		}
		defer l2.release()
		return nil, nil, renameLoc(l, l2, r.Flags)
	case opSymlink:
		// Path is the link target, stored verbatim; Path2 the new link.
		if strings.IndexByte(r.Path, 0) >= 0 {
			return nil, nil, syscall.EINVAL
		}
		l2, err := s.place(p2, grant.Write, false)
		if err != nil {
			return nil, nil, err
		}
		defer l2.release()
		return nil, nil, l2.symlink(r.Path)
	case opReadlink:
		t, err := l.readlink()
		if err != nil {
			return nil, nil, err
		}
		return &reply{Str: t}, nil, nil
	case opLink:
		l2, err := s.place(p2, grant.Write, false)
		if err != nil {
			return nil, nil, err
		}
		defer l2.release()
		return nil, nil, linkLoc(l, l2)
	case opChmod:
		return nil, nil, l.chmod(ModeFromUnix(r.Mode & 0o7777))
	case opTruncate:
		if r.Size < 0 {
			return nil, nil, syscall.EINVAL
		}
		return nil, nil, l.truncate(r.Size)
	case opUtimes:
		return nil, nil, l.utimes(r.Atime, r.Mtime, r.NoFollow)
	case opStatFS:
		sf, err := l.statFS()
		if err != nil {
			return nil, nil, err
		}
		return &reply{StatFS: sf}, nil, nil
	case opReadFile:
		return readSmall(l, r.N)
	case opWriteFile:
		a, err := writeAtomic(l, putReq{Mode: r.Mode, Mtime: r.Mtime, NoFollow: r.NoFollow, Sync: r.Sync, Unique: r.Unique},
			writeOpts{restricted: s.guard != nil}, func(w io.Writer) error { _, err := w.Write(data); return err })
		if err != nil {
			return nil, nil, err
		}
		return &reply{Attr: a}, nil, nil
	case opPartial:
		// The name a resumable upload of p with this key uses (see
		// partialFileName); the client looks at that file, then
		// uploads with the key.
		if !canLock {
			return nil, nil, syscall.ENOTSUP
		}
		if !validPartialKey(r.Kind) {
			return nil, nil, syscall.EINVAL
		}
		base := filepath.Base(p)
		if base == "" || base == "." || base == string(filepath.Separator) {
			return nil, nil, syscall.EISDIR
		}
		var id string
		if s.scope != nil {
			id = s.scope.ID()
		}
		return &reply{Str: filepath.Join(filepath.Dir(p), partialFileName(id, base, r.Kind))}, nil, nil
	case opHash:
		f, err := l.open(os.O_RDONLY, 0)
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		// Off and Size select a byte range (Size 0: to the end); the
		// range must lie within the file, so a hash never silently
		// covers less than asked.
		var src io.Reader = f
		if r.Off < 0 || r.Size < 0 {
			return nil, nil, syscall.EINVAL
		}
		if r.Off > 0 || r.Size > 0 {
			fi, err := f.Stat()
			if err != nil {
				return nil, nil, err
			}
			n := r.Size
			if n == 0 {
				n = fi.Size() - r.Off
			}
			if r.Off+n > fi.Size() || n < 0 {
				return nil, nil, syscall.EINVAL
			}
			src = io.NewSectionReader(f, r.Off, n)
		}
		h := sha256.New()
		if _, err := io.Copy(h, src); err != nil {
			return nil, nil, err
		}
		return &reply{Str: hex.EncodeToString(h.Sum(nil))}, nil, nil
	case opUserDir:
		if r.Kind != "downloads" {
			return nil, nil, syscall.EINVAL
		}
		// REMOHOP_DOWNLOADS_DIR lets whoever runs the host choose where
		// received files land, ahead of the per-OS lookup.
		d, err := os.Getenv("REMOHOP_DOWNLOADS_DIR"), error(nil)
		if !filepath.IsAbs(d) {
			d, err = downloadsDir()
		}
		if err != nil {
			return nil, nil, err
		}
		if err := guarded(s.ctx, s.guard, "downloads", grant.Write, d); err != nil {
			return nil, nil, err
		}
		dl, err := s.place(d, grant.Write, true)
		if err != nil {
			return nil, nil, err
		}
		defer dl.release()
		if err := dl.mkdir(0o755, true); err != nil {
			return nil, nil, err
		}
		return &reply{Str: d}, nil, nil
	case opRealpath:
		if l.confined() {
			// Resolved by the scope, below the root; it must exist.
			if _, err := l.stat(); err != nil {
				return nil, nil, err
			}
			return &reply{Str: l.path()}, nil, nil
		}
		rp, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, nil, err
		}
		rp, err = filepath.Abs(rp)
		if err != nil {
			return nil, nil, err
		}
		return &reply{Str: rp}, nil, nil
	}
	return nil, nil, syscall.ENOSYS
}

// followsLink says whether an operation follows a symbolic link at its
// path (and so acts on the link's target) or acts on the link itself.
func followsLink(op byte, r *request) bool {
	switch op {
	case opLstat, opReadlink, opRemove, opRename, opLink, opMkdir, opPartial:
		return false
	case opUtimes:
		return !r.NoFollow
	case opWriteFile:
		return !r.NoFollow && !r.Unique
	case opOpen:
		// O_CREAT|O_EXCL fails on any existing entry, links included.
		return !(r.Flags&wCreate != 0 && r.Flags&wExcl != 0)
	}
	return true
}

// readDir returns the next batch of a directory handle, with each entry's
// lstat attributes and symlink target ("readdir plus"), closing the handle
// when the directory is exhausted.
func (s *hsession) readDir(h uint64, hf *hfile) (*reply, []byte, error) {
	if !hf.dir {
		return nil, nil, errNotDir
	}
	hf.mu.Lock()
	ents, err := hf.f.ReadDir(dirBatch)
	hf.mu.Unlock()
	eof := false
	if err == io.EOF || (err == nil && len(ents) < dirBatch) {
		eof, err = true, nil
	}
	if err != nil {
		return nil, nil, err
	}
	out := make([]Attr, 0, len(ents))
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue // removed since the directory was read
		}
		a := attrFromInfo(fi)
		if fi.Mode()&fs.ModeSymlink != 0 {
			// Resolved afresh (not followed): under a grant, through
			// the root.
			if l, err := s.place(filepath.Join(hf.at, e.Name()), grant.Read, false); err == nil {
				a.Target, _ = l.readlink()
				l.release()
			}
		}
		out = append(out, a)
	}
	rep := &reply{Entries: out, EOF: eof, Handle: h}
	if eof {
		if s.remove(h) != nil {
			hf.f.Close()
		}
		rep.Handle = 0
	}
	return rep, nil, nil
}

func hostFlags(w int) (int, error) {
	if w&^(wWronly|wRdwr|wAppend|wCreate|wExcl|wTrunc|wSync) != 0 || w&wWronly != 0 && w&wRdwr != 0 {
		return 0, syscall.EINVAL
	}
	f := os.O_RDONLY
	switch {
	case w&wWronly != 0:
		f = os.O_WRONLY
	case w&wRdwr != 0:
		f = os.O_RDWR
	}
	for _, m := range [...]struct{ w, o int }{
		{wAppend, os.O_APPEND}, {wCreate, os.O_CREATE}, {wExcl, os.O_EXCL},
		{wTrunc, os.O_TRUNC}, {wSync, os.O_SYNC},
	} {
		if w&m.w != 0 {
			f |= m.o
		}
	}
	return f, nil
}

// wireFlags converts local os.O_* flags to the portable wire form.
func wireFlags(f int) int {
	var w int
	switch f & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		w |= wWronly
	case os.O_RDWR:
		w |= wRdwr
	}
	for _, m := range [...]struct{ w, o int }{
		{wAppend, os.O_APPEND}, {wCreate, os.O_CREATE}, {wExcl, os.O_EXCL},
		{wTrunc, os.O_TRUNC}, {wSync, os.O_SYNC},
	} {
		if f&m.o != 0 {
			w |= m.w
		}
	}
	return w
}

// readSmall reads a whole file of at most max bytes, or reports TooBig with
// its attributes so the client switches to a bulk stream.
func readSmall(l loc, max int) (*reply, []byte, error) {
	if max <= 0 || max > SmallFile {
		max = SmallFile
	}
	f, err := l.open(os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if fi.IsDir() {
		return nil, nil, syscall.EISDIR
	}
	a := attrFromInfo(fi)
	if fi.Size() > int64(max) {
		return &reply{TooBig: true, Attr: &a}, nil, nil
	}
	// Read one byte more than allowed to notice a file that grew.
	buf := make([]byte, max+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, nil, err
	}
	if n > max {
		return &reply{TooBig: true, Attr: &a}, nil, nil
	}
	a.Size = int64(n)
	return &reply{Attr: &a}, buf[:n], nil
}

// writeOpts are writeAtomic's settings beyond the client's request.
type writeOpts struct {
	// restricted: the session stands on a grant. It cannot name partial
	// files itself (only by key, see partialFileName).
	restricted bool
	// scopeID binds partial file names to the grant credential.
	scopeID string
	// abort, if set, breaks the transfer feeding fill: a newer upload
	// resuming the same partial file calls it to take the file over.
	abort func()
	// verify, if set, is asked before the file is installed: the
	// session's grant must still be the one the upload started under.
	verify func() error
}

// writeAtomic writes a file through a temporary file in the destination
// directory and renames it into place, so readers see either the old or the
// complete new content. Without NoFollow a destination that is a symlink is
// resolved and its target replaced, as an editor saving through a link
// would; with NoFollow the link itself is replaced.
//
// Everything after resolving the destination happens through a handle on
// its directory (an os.Root, below the grant root for a granted session):
// the temporary or partial file is created, checked, timed and renamed by
// name in that directory, never by a path that a link swapped in meanwhile
// could redirect.
func writeAtomic(dst loc, o putReq, wo writeOpts, fill func(io.Writer) error) (*Attr, error) {
	if !dst.confined() && !o.NoFollow && !o.Unique {
		// Under a grant the scope resolved the link already (to a
		// target inside the root, or refused).
		if rp, err := filepath.EvalSymlinks(dst.path()); err == nil {
			dst = plainLoc(rp)
		}
	}
	if (o.Partial != "" || o.PartialKey != "") && !canLock {
		// Checked before anything is created: without file locks a
		// partial file cannot be shared safely, so there are none.
		return nil, syscall.ENOTSUP
	}
	d, base, err := dst.openDir()
	if err != nil {
		return nil, err
	}
	defer d.Close()
	// A new file gets 0666 minus the host user's umask, as any program
	// creating it would; an explicit mode from the client is applied
	// exactly (the client decides whether to preserve modes).
	mode := 0o666 &^ Umask()
	if o.Mode != 0 {
		mode = ModeFromUnix(o.Mode & 0o7777)
	} else if !o.Unique {
		fi, err := d.Stat(base)
		if err != nil && !dst.confined() {
			fi, err = os.Stat(dst.path()) // a link leading out of the directory
		}
		if err == nil && fi.Mode().IsRegular() {
			mode = fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
		}
	}
	if fi, err := d.Lstat(base); err == nil && fi.IsDir() && !o.Unique {
		return nil, syscall.EISDIR
	}
	var tmp *os.File
	var tmpName string
	keep := false // a partial file survives failures, a temporary one does not
	if o.Partial != "" || o.PartialKey != "" {
		name, err := partialFor(dst, base, o, wo)
		if err != nil {
			return nil, err
		}
		release := claimPartial(filepath.Join(filepath.Dir(dst.path()), name), wo.abort)
		defer release()
		if tmp, err = openPartial(d, name, o.Off); err != nil {
			return nil, err
		}
		tmpName, keep = name, true
	} else if tmp, tmpName, err = createTemp(d, base); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			if !keep {
				d.Remove(tmpName)
			}
		}
	}()
	if err := fill(tmp); err != nil {
		return nil, err
	}
	if err := tmp.Chmod(mode); err != nil {
		return nil, err
	}
	if o.Sync {
		if err := tmp.Sync(); err != nil {
			return nil, err
		}
	}
	if !keep {
		// A temporary file is closed before it is touched by name:
		// Windows refuses to rename (or link) a file that is open, and
		// applies the write time of a written handle when it closes,
		// which would undo the Chtimes below.
		if err := tmp.Close(); err != nil {
			return nil, err
		}
	}
	// A partial file (only where files can be locked, so not on Windows)
	// is installed while still open, and so still locked: a concurrent
	// resumer must not grab it between close and rename.
	if wo.verify != nil {
		if err := wo.verify(); err != nil {
			return nil, err
		}
	}
	if o.Mtime != nil {
		t := time.Unix(0, *o.Mtime)
		if err := d.Chtimes(tmpName, t, t); err != nil {
			return nil, err
		}
	}
	fi, err := d.Lstat(tmpName)
	if err != nil {
		return nil, err
	}
	final := base
	if o.Unique {
		final, err = installUnique(d, tmpName, base)
	} else {
		err = replaceFile(d, tmpName, base)
	}
	if err != nil {
		return nil, rootErr(err)
	}
	ok = true
	tmp.Close()
	if keep || fi.Size() >= resumeMin {
		removeStalePartials(d, base)
	}
	a := attrFromInfo(fi)
	a.Name = final
	return &a, nil
}

// partialFor decides the partial file an upload resumes into: by key, the
// host's own name for it (partialFileName); without one, the client's name
// (older clients), which must sit next to the destination and carry its
// prefix, and which only a session with full access may give.
func partialFor(dst loc, base string, o putReq, wo writeOpts) (string, error) {
	if o.PartialKey != "" {
		if !validPartialKey(o.PartialKey) {
			return "", syscall.EINVAL
		}
		name := partialFileName(wo.scopeID, base, o.PartialKey)
		if o.Partial != "" {
			pp, err := resolve(o.Partial)
			if err != nil {
				return "", err
			}
			if filepath.Base(pp) != name || !sameDir(filepath.Dir(pp), filepath.Dir(dst.path())) {
				return "", syscall.EINVAL
			}
		}
		return name, nil
	}
	if wo.restricted {
		// Not resumable: the client uploads normally instead.
		return "", syscall.ENOTSUP
	}
	pp, err := resolve(o.Partial)
	if err != nil {
		return "", err
	}
	// The client names the directory as it knows it, which may reach it
	// through symlinks (macOS: /var -> /private/var) that the destination
	// path above has had resolved; compare the directories themselves,
	// not their spelling.
	pdir, pbase := filepath.Split(pp)
	if pdir == "" {
		pdir = "."
	}
	if !sameDir(pdir, filepath.Dir(dst.path())) || !strings.HasPrefix(pbase, PartialPrefix(base)) {
		return "", syscall.EINVAL
	}
	return pbase, nil
}

// createTemp creates a new temporary file for base in directory d.
func createTemp(d *os.Root, base string) (*os.File, string, error) {
	var b [8]byte
	for range 10000 {
		rand.Read(b[:])
		name := "." + truncName(base) + ".remohop-" + hex.EncodeToString(b[:])
		f, err := d.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, name, err
	}
	return nil, "", syscall.EEXIST
}

// sameDir reports whether two paths name the same directory.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && fa.IsDir() && os.SameFile(fa, fb)
}

// replaceFile renames tmp (closed) onto dst, both names in d. Windows
// refuses to replace a read-only file, which a file copied with its mode
// can well be; the attribute is cleared and the rename retried.
func replaceFile(d *os.Root, tmp, dst string) error {
	err := d.Rename(tmp, dst)
	if err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		if d.Chmod(dst, 0o666) == nil {
			err = d.Rename(tmp, dst)
		}
	}
	return err
}

// PartialPrefix is the name prefix of resumable partial files for a
// destination named base. They live next to it, hidden, and a successful
// resumable install removes every one of them.
func PartialPrefix(base string) string { return "." + truncName(base) + ".remohop-partial-" }

// resumeMin mirrors the client's threshold for resumable transfers: only
// installs of files this large look for stale partial files to remove.
const resumeMin = 8 << 20

// removeStalePartials removes the partial files of destination base in
// directory d, except any that a transfer in progress holds locked. Only
// regular files are touched, opened through d, never a link's target.
func removeStalePartials(d *os.Root, base string) {
	df, err := d.Open(".")
	if err != nil {
		return
	}
	ents, err := df.ReadDir(-1)
	df.Close()
	if err != nil {
		return
	}
	pre := PartialPrefix(base)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), pre) || !e.Type().IsRegular() {
			continue
		}
		f, err := openRegular(d, e.Name(), os.O_RDWR)
		if err != nil {
			continue
		}
		// Removed while locked, so no transfer can pick it up in between.
		if lockFile(f) == nil {
			d.Remove(e.Name())
		}
		f.Close()
	}
}

// openRegular opens name in d, which must be a regular file with a single
// link, not a symbolic link to one: what is opened is checked against the
// directory entry after opening, so a link swapped in is noticed.
func openRegular(d *os.Root, name string, flag int) (*os.File, error) {
	if fi, err := d.Lstat(name); err == nil && !fi.Mode().IsRegular() {
		return nil, syscall.EINVAL
	}
	f, err := d.OpenFile(name, flag, 0o600)
	if err != nil {
		if isEscape(err) {
			err = syscall.EINVAL
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	li, err := d.Lstat(name)
	if err != nil || !li.Mode().IsRegular() || !fi.Mode().IsRegular() || !os.SameFile(fi, li) || linkCount(fi) != 1 {
		f.Close()
		return nil, syscall.EINVAL
	}
	return f, nil
}

// partialHolders tracks the uploads writing partial files, so that a new
// upload resuming one can take it over: a client killed without closing
// its session leaves the old upload waiting (and the file locked) until
// the session times out, and the user's retry should not have to wait.
var partialHolders = struct {
	sync.Mutex
	m map[string]*partialHolder
}{m: map[string]*partialHolder{}}

type partialHolder struct {
	abort func()
	done  chan struct{}
}

// claimPartial registers the caller as the writer of partial file p,
// aborting and waiting (briefly) for a previous writer. The returned
// function releases the claim.
func claimPartial(p string, abort func()) (release func()) {
	h := &partialHolder{abort: abort, done: make(chan struct{})}
	partialHolders.Lock()
	old := partialHolders.m[p]
	partialHolders.m[p] = h
	partialHolders.Unlock()
	if old != nil {
		if old.abort != nil {
			old.abort()
		}
		select {
		case <-old.done:
		case <-time.After(5 * time.Second):
		}
	}
	return func() {
		partialHolders.Lock()
		if partialHolders.m[p] == h {
			delete(partialHolders.m, p)
		}
		partialHolders.Unlock()
		close(h.done)
	}
}

// openPartial opens (or creates) partial file name in directory d for
// resuming at off: it must be a regular file with a single link (never a
// symbolic or hard link to a file elsewhere) holding at least off bytes,
// which are kept; anything after is cut. The file is locked for as long as
// it is open, so two transfers never write one partial file (the second
// gets EBUSY and should not resume).
func openPartial(d *os.Root, name string, off int64) (*os.File, error) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	f, err := openRegular(d, name, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*os.File, error) {
		f.Close()
		return nil, err
	}
	if err := lockFile(f); err != nil {
		return fail(err)
	}
	fi, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	if fi.Size() < off {
		return fail(syscall.EINVAL)
	}
	if err := f.Truncate(off); err != nil {
		return fail(err)
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return fail(err)
	}
	return f, nil
}

// UniqueName is the i-th candidate name for base: base itself, then
// "name (1).ext", "name (2).ext", ... (".tar.gz"-style double extensions
// and dotfiles are kept whole).
func UniqueName(base string, i int) string {
	if i == 0 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" { // ".bashrc"
		stem, ext = base, ""
	}
	if inner := filepath.Ext(stem); inner == ".tar" && stem != inner {
		stem, ext = strings.TrimSuffix(stem, inner), inner+ext
	}
	return fmt.Sprintf("%s (%d)%s", stem, i, ext)
}

// installUnique moves tmp to the first free UniqueName of base, both
// names in d, and returns the name. A hard link is atomic and fails if the
// name exists, so concurrent installs never collide and nothing is
// replaced; where links are unsupported a name is reserved with O_EXCL and
// the file renamed over the reservation.
func installUnique(d *os.Root, tmp, base string) (string, error) {
	for i := 0; i < 10000; i++ {
		cand := UniqueName(base, i)
		err := d.Link(tmp, cand)
		if err == nil {
			d.Remove(tmp)
			return cand, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		f, err := d.OpenFile(cand, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		f.Close()
		if err := d.Rename(tmp, cand); err != nil {
			d.Remove(cand)
			return "", err
		}
		return cand, nil
	}
	return "", syscall.EEXIST
}

// truncName keeps temporary names within NAME_MAX.
func truncName(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func sendStatus(st peer.Stream, a *Attr, err error) error {
	bs := bulkStatus{Attr: a}
	if err != nil {
		bs.Code, bs.Message = errorCode(err)
	}
	b, _ := json.Marshal(bs)
	return wire.WriteFrame(st, bulkEnd, b)
}

// serveGet streams one file to the client.
func serveGet(ctx context.Context, c *peer.Conn, st peer.Stream, raw json.RawMessage) error {
	var r getReq
	if err := peer.DecodeReq(raw, &r); err != nil {
		return sendStatus(st, nil, syscall.EINVAL)
	}
	p, err := resolve(r.Path)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	g := grant.Of(c)
	if err := guarded(ctx, g, "download", grant.Read, p); err != nil {
		return sendStatus(st, nil, err)
	}
	l, err := placeFor(g, nil, p, grant.Read, true)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	defer l.release()
	f, err := l.open(os.O_RDONLY, 0)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return sendStatus(st, nil, err)
	}
	if fi.IsDir() {
		return sendStatus(st, nil, syscall.EISDIR)
	}
	if r.Off < 0 {
		return sendStatus(st, nil, syscall.EINVAL)
	}
	a := attrFromInfo(fi)
	hdr, _ := json.Marshal(a)
	if err := wire.WriteFrame(st, bulkHeader, hdr); err != nil {
		return err
	}
	// Frames are assembled in one reused buffer: read straight into it
	// after a 5-byte header and write it with a single call.
	buf := make([]byte, 5+bulkChunk)
	off := r.Off
	for {
		n, rerr := f.ReadAt(buf[5:], off)
		if n > 0 {
			binary.BigEndian.PutUint32(buf, uint32(n))
			buf[4] = bulkData
			if _, err := st.Write(buf[:5+n]); err != nil {
				return err
			}
			off += int64(n)
		}
		if rerr == io.EOF {
			return sendStatus(st, nil, nil)
		}
		if rerr != nil {
			return sendStatus(st, nil, rerr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// servePut receives one file from the client and installs it atomically.
func servePut(ctx context.Context, c *peer.Conn, st peer.Stream, raw json.RawMessage) error {
	var r putReq
	if err := peer.DecodeReq(raw, &r); err != nil {
		return sendStatus(st, nil, syscall.EINVAL)
	}
	p, err := resolve(r.Path)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	g := grant.Of(c)
	if err := guarded(ctx, g, "upload", grant.Write, p); err != nil {
		return sendStatus(st, nil, err)
	}
	// The scope is taken once: the partial file's name is bound to it,
	// and the file is installed only if the grant is still the same.
	scope, err := scopeOf(g)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	dst, err := placeFor(g, scope, p, grant.Write, !r.NoFollow && !r.Unique)
	if err != nil {
		return sendStatus(st, nil, err)
	}
	defer dst.release()
	wo := writeOpts{restricted: g != nil, abort: st.Reset}
	if scope != nil {
		wo.scopeID = scope.ID()
		wo.verify = func() error {
			l, err := placeFor(g, scope, p, grant.Write, !r.NoFollow && !r.Unique)
			l.release()
			return err
		}
	}
	// Nothing is created on disk until the client's first frame arrives:
	// a client that gives up (or a session that dies) before sending any
	// data must not leave even an empty temporary or partial file behind,
	// let alone one created after it is gone.
	buf := make([]byte, bulkChunk)
	typ, payload, err := wire.ReadFrame(st, buf)
	if err != nil {
		st.Reset()
		return err
	}
	pending := true
	committed := false
	a, err := writeAtomic(dst, r, wo, func(w io.Writer) error {
		for {
			if !pending {
				var err error
				if typ, payload, err = wire.ReadFrame(st, buf); err != nil {
					return err
				}
			}
			pending = false
			switch typ {
			case bulkData:
				if _, err := w.Write(payload); err != nil {
					return err
				}
			case bulkEnd:
				committed = true
				return nil
			default:
				return syscall.EPROTO
			}
		}
	})
	if err != nil && !committed && !isFSError(err) {
		// The client went away mid-upload: the temporary file is
		// already removed (a partial one is kept for resuming) and
		// nobody is listening for a status.
		st.Reset()
		return err
	}
	return sendStatus(st, a, err)
}

// isFSError reports whether err came from the filesystem rather than from
// the stream.
func isFSError(err error) bool {
	var pe *fs.PathError
	var en syscall.Errno
	var le *os.LinkError
	return errors.As(err, &pe) || errors.As(err, &le) || (errors.As(err, &en) && en != syscall.EPROTO)
}
