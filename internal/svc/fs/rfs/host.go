package rfs

import (
	"context"
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
// The host serves with the user's own privileges, so no further confinement
// applies: a client may name any path that user may access.
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
	mu     sync.Mutex // serialises directory reads
}

// hsession is the per-control-stream host state.
type hsession struct {
	mu      sync.Mutex
	handles map[uint64]*hfile
	next    uint64
	// guard is the session's grant, nil for full access; ctx is the
	// stream's, for approvals the guard may wait for.
	guard grant.Guard
	ctx   context.Context
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
	if s.guard != nil {
		if name, a, ok := opAccess(op, r); ok {
			if err := guarded(s.ctx, s.guard, name, a, p); err != nil {
				return nil, nil, err
			}
		}
		switch op {
		case opRename, opLink, opSymlink:
			// The second path is created (or replaced) too.
			p2, err := resolve(r.Path2)
			if err != nil {
				return nil, nil, err
			}
			if err := guarded(s.ctx, s.guard, "create", grant.Write, p2); err != nil {
				return nil, nil, err
			}
		}
	}
	switch op {
	case opStat, opLstat:
		var fi fs.FileInfo
		if op == opStat {
			fi, err = os.Stat(p)
		} else {
			fi, err = os.Lstat(p)
		}
		if err != nil {
			return nil, nil, err
		}
		a := attrFromInfo(fi)
		if op == opLstat && fi.Mode()&fs.ModeSymlink != 0 {
			a.Target, _ = os.Readlink(p)
		}
		return &reply{Attr: &a}, nil, nil
	case opOpenDir:
		f, err := os.Open(p)
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
		hf := &hfile{f: f, dir: true}
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
		f, err := os.OpenFile(p, flags, ModeFromUnix(r.Mode)&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))
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
		m := ModeFromUnix(r.Mode & 0o7777)
		if r.All {
			return nil, nil, os.MkdirAll(p, m)
		}
		return nil, nil, os.Mkdir(p, m)
	case opRemove:
		switch r.Kind {
		case removeAny:
			return nil, nil, os.Remove(p)
		case removeFile:
			return nil, nil, unlink(p)
		case removeDir:
			return nil, nil, rmdir(p)
		case removeAll:
			return nil, nil, os.RemoveAll(p)
		}
		return nil, nil, syscall.EINVAL
	case opRename:
		p2, err := resolve(r.Path2)
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, rename(p, p2, r.Flags)
	case opSymlink:
		// Path is the link target, stored verbatim; Path2 the new link.
		p2, err := resolve(r.Path2)
		if err != nil {
			return nil, nil, err
		}
		if strings.IndexByte(r.Path, 0) >= 0 {
			return nil, nil, syscall.EINVAL
		}
		return nil, nil, os.Symlink(r.Path, p2)
	case opReadlink:
		t, err := os.Readlink(p)
		if err != nil {
			return nil, nil, err
		}
		return &reply{Str: t}, nil, nil
	case opLink:
		p2, err := resolve(r.Path2)
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, os.Link(p, p2)
	case opChmod:
		return nil, nil, os.Chmod(p, ModeFromUnix(r.Mode&0o7777))
	case opTruncate:
		if r.Size < 0 {
			return nil, nil, syscall.EINVAL
		}
		return nil, nil, os.Truncate(p, r.Size)
	case opUtimes:
		return nil, nil, utimes(p, r.Atime, r.Mtime, r.NoFollow)
	case opStatFS:
		sf, err := statFS(p)
		if err != nil {
			return nil, nil, err
		}
		return &reply{StatFS: sf}, nil, nil
	case opReadFile:
		return readSmall(p, r.N)
	case opWriteFile:
		a, err := writeAtomic(p, putReq{Mode: r.Mode, Mtime: r.Mtime, NoFollow: r.NoFollow, Sync: r.Sync, Unique: r.Unique}, nil,
			func(w io.Writer) error { _, err := w.Write(data); return err })
		if err != nil {
			return nil, nil, err
		}
		return &reply{Attr: a}, nil, nil
	case opHash:
		f, err := os.Open(p)
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
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, nil, err
		}
		return &reply{Str: d}, nil, nil
	case opRealpath:
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
	dir := hf.f.Name()
	out := make([]Attr, 0, len(ents))
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue // removed since the directory was read
		}
		a := attrFromInfo(fi)
		if fi.Mode()&fs.ModeSymlink != 0 {
			a.Target, _ = os.Readlink(filepath.Join(dir, e.Name()))
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
func readSmall(p string, max int) (*reply, []byte, error) {
	if max <= 0 || max > SmallFile {
		max = SmallFile
	}
	f, err := os.Open(p)
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

// writeAtomic writes a file through a temporary file in the destination
// directory and renames it into place, so readers see either the old or the
// complete new content. Without NoFollow a destination that is a symlink is
// resolved and its target replaced, as an editor saving through a link
// would; with NoFollow the link itself is replaced.
//
// abort, if set, breaks the transfer feeding fill: a newer upload resuming
// the same partial file calls it to take the file over.
func writeAtomic(p string, o putReq, abort func(), fill func(io.Writer) error) (*Attr, error) {
	if !o.NoFollow && !o.Unique {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			p = rp
		}
	}
	// A new file gets 0666 minus the host user's umask, as any program
	// creating it would; an explicit mode from the client is applied
	// exactly (the client decides whether to preserve modes).
	mode := 0o666 &^ Umask()
	if o.Mode != 0 {
		mode = ModeFromUnix(o.Mode & 0o7777)
	} else if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && !o.Unique {
		mode = fi.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	}
	dir, base := filepath.Split(p)
	if dir == "" {
		dir = "."
	}
	if base == "" {
		return nil, syscall.EISDIR
	}
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() && !o.Unique {
		return nil, syscall.EISDIR
	}
	if o.Partial != "" && !canLock {
		// Checked before anything is created: without file locks a
		// partial file cannot be shared safely, so there are none.
		return nil, syscall.ENOTSUP
	}
	var tmp *os.File
	keep := false // a partial file survives failures, a temporary one does not
	if o.Partial != "" {
		pp, err := resolve(o.Partial)
		if err != nil {
			return nil, err
		}
		// The partial file must sit in the destination's directory. The
		// client names that directory as it knows it, which may reach
		// it through symlinks (macOS: /var -> /private/var) that the
		// destination path above has had resolved; compare the
		// directories themselves, not their spelling.
		pdir, pbase := filepath.Split(pp)
		if pdir == "" {
			pdir = "."
		}
		if !sameDir(pdir, dir) || !strings.HasPrefix(pbase, PartialPrefix(base)) {
			return nil, syscall.EINVAL
		}
		release := claimPartial(pp, abort)
		defer release()
		if tmp, err = openPartial(pp, o.Off); err != nil {
			return nil, err
		}
		keep = true
	} else {
		var err error
		if tmp, err = os.CreateTemp(dir, "."+truncName(base)+".remohop-*"); err != nil {
			return nil, err
		}
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			if !keep {
				os.Remove(tmp.Name())
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
	if o.Mtime != nil {
		t := time.Unix(0, *o.Mtime)
		if err := os.Chtimes(tmp.Name(), t, t); err != nil {
			return nil, err
		}
	}
	fi, err := os.Lstat(tmp.Name())
	if err != nil {
		return nil, err
	}
	final := p
	if o.Unique {
		final, err = installUnique(tmp.Name(), p)
	} else {
		err = replaceFile(tmp.Name(), p)
	}
	if err != nil {
		return nil, err
	}
	ok = true
	tmp.Close()
	if o.Partial != "" || fi.Size() >= resumeMin {
		removeStalePartials(dir, base)
	}
	a := attrFromInfo(fi)
	a.Name = filepath.Base(final)
	return &a, nil
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

// replaceFile renames tmp (closed) onto dst. Windows refuses to replace a
// read-only file, which a file copied with its mode can well be; the
// attribute is cleared and the rename retried.
func replaceFile(tmp, dst string) error {
	err := os.Rename(tmp, dst)
	if err != nil && runtime.GOOS == "windows" && errors.Is(err, fs.ErrPermission) {
		if os.Chmod(dst, 0o666) == nil {
			err = os.Rename(tmp, dst)
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

// removeStalePartials removes the partial files of a destination, except
// any that a transfer in progress holds locked.
func removeStalePartials(dir, base string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	pre := PartialPrefix(base)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), pre) || !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		// Removed while locked, so no transfer can pick it up in between.
		if lockFile(f) == nil {
			os.Remove(p)
		}
		f.Close()
	}
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

// openPartial opens (or creates) a partial file for resuming at off: it
// must hold at least off bytes, which are kept; anything after is cut. The
// file is locked for as long as it is open, so two transfers never write
// one partial file (the second gets EBUSY and should not resume).
func openPartial(p string, off int64) (*os.File, error) {
	if off < 0 {
		return nil, syscall.EINVAL
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
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
	if !fi.Mode().IsRegular() || fi.Size() < off {
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

// installUnique moves tmp to the first free UniqueName of p. A hard link is
// atomic and fails if the name exists, so concurrent installs never
// collide and nothing is replaced; where links are unsupported a name is
// reserved with O_EXCL and the file renamed over the reservation.
func installUnique(tmp, p string) (string, error) {
	dir, base := filepath.Split(p)
	for i := 0; i < 10000; i++ {
		cand := filepath.Join(dir, UniqueName(base, i))
		err := os.Link(tmp, cand)
		if err == nil {
			os.Remove(tmp)
			return cand, nil
		}
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		f, err := os.OpenFile(cand, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		f.Close()
		if err := os.Rename(tmp, cand); err != nil {
			os.Remove(cand)
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
	if err := guarded(ctx, grant.Of(c), "download", grant.Read, p); err != nil {
		return sendStatus(st, nil, err)
	}
	f, err := os.Open(p)
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
	if err := guarded(ctx, grant.Of(c), "upload", grant.Write, p); err != nil {
		return sendStatus(st, nil, err)
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
	a, err := writeAtomic(p, r, st.Reset, func(w io.Writer) error {
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
