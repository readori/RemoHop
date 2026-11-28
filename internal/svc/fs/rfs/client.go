package rfs

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Client is a remote filesystem session over one control stream. All
// methods are safe for concurrent use; concurrent calls are pipelined on the
// stream and served in parallel by the host.
//
// Paths are host paths: absolute, or relative to the host user's home
// directory ("~" and "~/x" also work).
type Client struct {
	conn *peer.Conn
	st   peer.Stream

	wmu sync.Mutex // serialises frame writes

	mu      sync.Mutex
	nextID  uint32
	pending map[uint32]chan result
	err     error
	done    chan struct{}

	infoMu sync.Mutex
	info   *Info
}

type result struct {
	typ  byte
	rep  reply
	data []byte
	err  error
}

// NewClient opens a filesystem session on conn.
func NewClient(ctx context.Context, conn *peer.Conn) (*Client, error) {
	st, err := conn.Open(ctx, ServiceControl, openReq{Version: protoVersion})
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, st: st, pending: map[uint32]chan result{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

// Conn returns the session the client runs on.
func (c *Client) Conn() *peer.Conn { return c.conn }

// Done is closed when the client can no longer be used (Close was called,
// the stream broke or the session ended).
func (c *Client) Done() <-chan struct{} { return c.done }

// Close ends the control stream. The host closes every handle opened
// through it. Bulk transfers in progress are not affected.
func (c *Client) Close() error {
	c.fail(ErrClosed)
	return c.st.Close()
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
	for id, ch := range c.pending {
		ch <- result{err: err}
		delete(c.pending, id)
	}
}

func (c *Client) readLoop() {
	for {
		typ, payload, err := wire.ReadFrame(c.st, nil)
		if err == nil {
			err = c.deliver(typ, payload)
		}
		if err != nil {
			c.fail(err)
			c.st.Reset()
			return
		}
	}
}

func (c *Client) deliver(typ byte, payload []byte) error {
	id, meta, data, err := parseMsg(payload)
	if err != nil {
		return err
	}
	var rep reply
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &rep); err != nil {
			return fmt.Errorf("rfs: bad reply: %w", err)
		}
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- result{typ: typ, rep: rep, data: data}
	}
	return nil
}

// call sends one request and waits for its reply. A remote failure comes
// back as a *fs.PathError naming op and path.
func (c *Client) call(ctx context.Context, op byte, opName, path string, req *request, data []byte) (*result, error) {
	meta, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan result, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, &fs.PathError{Op: opName, Path: path, Err: ErrClosed}
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()

	buf := appendMsg(op, id, meta, data)
	c.wmu.Lock()
	_, err = c.st.Write(buf)
	c.wmu.Unlock()
	if err != nil {
		c.fail(err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, &fs.PathError{Op: opName, Path: path, Err: ErrClosed}
		}
		if r.typ != stOK {
			return nil, remoteErr(opName, path, r.rep.Code, r.rep.Message)
		}
		return &r, nil
	case <-ctx.Done():
		// The reply may still create a handle on the host; close it
		// when it arrives so abandoned calls do not leak.
		go func() {
			if r := <-ch; r.err == nil && r.typ == stOK && r.rep.Handle != 0 {
				c.closeHandle(r.rep.Handle)
			}
		}()
		return nil, ctx.Err()
	}
}

func (c *Client) closeHandle(h uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.call(ctx, opClose, "close", "", &request{Handle: h}, nil)
	return err
}

// Info describes the host (home directory, OS, user and group IDs). It is
// fetched once and cached.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	c.infoMu.Lock()
	defer c.infoMu.Unlock()
	if c.info != nil {
		return c.info, nil
	}
	r, err := c.call(ctx, opInfo, "info", "", &request{}, nil)
	if err != nil {
		return nil, err
	}
	if r.rep.Info == nil {
		return nil, errors.New("rfs: empty info reply")
	}
	c.info = r.rep.Info
	return c.info, nil
}

func (c *Client) attrCall(ctx context.Context, op byte, opName, name string) (*FileInfo, error) {
	r, err := c.call(ctx, op, opName, name, &request{Path: name}, nil)
	if err != nil {
		return nil, err
	}
	if r.rep.Attr == nil {
		return nil, &fs.PathError{Op: opName, Path: name, Err: errors.New("rfs: missing attributes")}
	}
	return newInfo(*r.rep.Attr), nil
}

// Stat returns the attributes of name, following symlinks.
func (c *Client) Stat(ctx context.Context, name string) (*FileInfo, error) {
	return c.attrCall(ctx, opStat, "stat", name)
}

// Lstat returns the attributes of name without following a final symlink;
// for a symlink, FileInfo.Target holds the link target.
func (c *Client) Lstat(ctx context.Context, name string) (*FileInfo, error) {
	return c.attrCall(ctx, opLstat, "lstat", name)
}

// ReadDir lists a directory with every entry's lstat attributes, sorted by
// name. A directory of up to 1024 entries takes one round trip.
func (c *Client) ReadDir(ctx context.Context, name string) ([]*FileInfo, error) {
	var out []*FileInfo
	r, err := c.call(ctx, opOpenDir, "readdir", name, &request{Path: name}, nil)
	for {
		if err != nil {
			return nil, err
		}
		for _, a := range r.rep.Entries {
			if !validName(a.Name) {
				continue
			}
			out = append(out, newInfo(a))
		}
		if r.rep.EOF || r.rep.Handle == 0 {
			break
		}
		h := r.rep.Handle
		// A host must not keep a client listing forever: bound the
		// total, and refuse batches that make no progress.
		if len(out) > maxDirEntries || len(r.rep.Entries) == 0 {
			c.closeHandle(h)
			return nil, remoteErr("readdir", name, "E2BIG",
				fmt.Sprintf("readdir %s: listing exceeds %d entries or stalls", name, maxDirEntries))
		}
		r, err = c.call(ctx, opReadDir, "readdir", name, &request{Handle: h}, nil)
		if err != nil {
			c.closeHandle(h)
		}
	}
	if len(out) > maxDirEntries {
		return nil, remoteErr("readdir", name, "E2BIG", fmt.Sprintf("readdir %s: listing exceeds %d entries", name, maxDirEntries))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].a.Name < out[j].a.Name })
	return out, nil
}

// MaxDirEntries bounds one ReadDir.
const MaxDirEntries = 1 << 20

// maxDirEntries is MaxDirEntries, lower in tests: reaching a million
// entries takes over a thousand round trips, too slow on a loaded machine.
var maxDirEntries = MaxDirEntries

// validName reports whether a directory entry name from the host is a
// single path component. Hosts are not trusted: a name such as "../x" or
// "/etc" must never reach a caller that joins it onto a local path. On a
// Windows client the name must also be local to Win32 (no device names
// such as NUL or COM1.txt, no drive or stream colon) and not end in a dot
// or space, which Win32 strips, aliasing the name onto another entry.
func validName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 4096 {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] == '/' || n[i] == 0 || n[i] == '\\' {
			return false
		}
	}
	if runtime.GOOS == "windows" {
		if !filepath.IsLocal(n) || strings.ContainsAny(n, `:<>"|?*`) ||
			strings.HasSuffix(n, ".") || strings.HasSuffix(n, " ") {
			return false
		}
	}
	return true
}

// ValidName reports whether name is safe to use as one component of a local
// path: not empty, ".", "..", and free of separators and NUL. Callers
// joining names from ReadDir onto local paths must check them (ReadDir
// already drops invalid ones).
func ValidName(name string) bool { return validName(name) }

// Mkdir creates a directory.
func (c *Client) Mkdir(ctx context.Context, name string, perm fs.FileMode) error {
	_, err := c.call(ctx, opMkdir, "mkdir", name, &request{Path: name, Mode: ModeToUnix(perm) & 0o7777}, nil)
	return err
}

// MkdirAll creates a directory and any missing parents.
func (c *Client) MkdirAll(ctx context.Context, name string, perm fs.FileMode) error {
	_, err := c.call(ctx, opMkdir, "mkdir", name, &request{Path: name, Mode: ModeToUnix(perm) & 0o7777, All: true}, nil)
	return err
}

func (c *Client) remove(ctx context.Context, opName, kind, name string) error {
	_, err := c.call(ctx, opRemove, opName, name, &request{Path: name, Kind: kind}, nil)
	return err
}

// Remove removes a file or an empty directory.
func (c *Client) Remove(ctx context.Context, name string) error {
	return c.remove(ctx, "remove", removeAny, name)
}

// RemoveAll removes name and everything below it. A missing name is not an
// error.
func (c *Client) RemoveAll(ctx context.Context, name string) error {
	return c.remove(ctx, "removeall", removeAll, name)
}

// Unlink removes a non-directory (unlink(2)).
func (c *Client) Unlink(ctx context.Context, name string) error {
	return c.remove(ctx, "unlink", removeFile, name)
}

// Rmdir removes an empty directory (rmdir(2)).
func (c *Client) Rmdir(ctx context.Context, name string) error {
	return c.remove(ctx, "rmdir", removeDir, name)
}

// Rename renames from to to, replacing to if it exists.
func (c *Client) Rename(ctx context.Context, from, to string) error {
	return c.RenameFlags(ctx, from, to, 0)
}

// RenameFlags renames with RenameNoReplace and/or RenameExchange.
func (c *Client) RenameFlags(ctx context.Context, from, to string, flags int) error {
	_, err := c.call(ctx, opRename, "rename", from, &request{Path: from, Path2: to, Flags: flags}, nil)
	return err
}

// Symlink creates link pointing at target (stored verbatim).
func (c *Client) Symlink(ctx context.Context, target, link string) error {
	_, err := c.call(ctx, opSymlink, "symlink", link, &request{Path: target, Path2: link}, nil)
	return err
}

// Readlink returns a symlink's target.
func (c *Client) Readlink(ctx context.Context, name string) (string, error) {
	r, err := c.call(ctx, opReadlink, "readlink", name, &request{Path: name}, nil)
	if err != nil {
		return "", err
	}
	return r.rep.Str, nil
}

// Link creates a hard link newname to oldname.
func (c *Client) Link(ctx context.Context, oldname, newname string) error {
	_, err := c.call(ctx, opLink, "link", newname, &request{Path: oldname, Path2: newname}, nil)
	return err
}

// Chmod changes permission bits (including setuid, setgid and sticky).
func (c *Client) Chmod(ctx context.Context, name string, mode fs.FileMode) error {
	_, err := c.call(ctx, opChmod, "chmod", name, &request{Path: name, Mode: ModeToUnix(mode) & 0o7777}, nil)
	return err
}

// Truncate changes the size of a file.
func (c *Client) Truncate(ctx context.Context, name string, size int64) error {
	_, err := c.call(ctx, opTruncate, "truncate", name, &request{Path: name, Size: size}, nil)
	return err
}

// Utimes sets access and modification times, following symlinks. A zero
// time leaves that time unchanged.
func (c *Client) Utimes(ctx context.Context, name string, atime, mtime time.Time) error {
	_, err := c.call(ctx, opUtimes, "utimes", name, &request{Path: name, Atime: timePtr(atime), Mtime: timePtr(mtime)}, nil)
	return err
}

// Lutimes is Utimes on a symlink itself (Linux hosts; elsewhere it follows
// the link).
func (c *Client) Lutimes(ctx context.Context, name string, atime, mtime time.Time) error {
	_, err := c.call(ctx, opUtimes, "utimes", name, &request{Path: name, Atime: timePtr(atime), Mtime: timePtr(mtime), NoFollow: true}, nil)
	return err
}

// StatFS describes the filesystem holding name.
func (c *Client) StatFS(ctx context.Context, name string) (*StatFS, error) {
	r, err := c.call(ctx, opStatFS, "statfs", name, &request{Path: name}, nil)
	if err != nil {
		return nil, err
	}
	if r.rep.StatFS == nil {
		return nil, &fs.PathError{Op: "statfs", Path: name, Err: errors.New("rfs: empty reply")}
	}
	return r.rep.StatFS, nil
}

// Hash returns the hex SHA-256 of a file's content, computed on the host.
func (c *Client) Hash(ctx context.Context, name string) (string, error) {
	return c.HashRange(ctx, name, 0, 0)
}

// HashRange returns the hex SHA-256 of n bytes at off (n 0: to the end),
// computed on the host. The range must lie within the file (else EINVAL).
func (c *Client) HashRange(ctx context.Context, name string, off, n int64) (string, error) {
	r, err := c.call(ctx, opHash, "hash", name, &request{Path: name, Off: off, Size: n}, nil)
	if err != nil {
		return "", err
	}
	return r.rep.Str, nil
}

// DownloadsDir returns the host user's download directory (XDG user dir,
// Windows known folder, or ~/Downloads), creating it if missing.
func (c *Client) DownloadsDir(ctx context.Context) (string, error) {
	r, err := c.call(ctx, opUserDir, "userdir", "downloads", &request{Kind: "downloads"}, nil)
	if err != nil {
		return "", err
	}
	return r.rep.Str, nil
}

// PartialPath returns the host's partial file for a resumable upload to
// name, for key (1-64 letters, digits, '-' or '_', identifying the source:
// a different key gets a different file). The host derives the name from
// the key, the destination and the session's grant, so a partial file is
// only ever resumed by an upload of the same source to the same place under
// the same credential. Hosts that predate this answer ENOSYS; hosts that
// cannot resume at all (no file locks) ENOTSUP.
func (c *Client) PartialPath(ctx context.Context, name, key string) (string, error) {
	r, err := c.call(ctx, opPartial, "partial", name, &request{Path: name, Kind: key}, nil)
	if err != nil {
		return "", err
	}
	return r.rep.Str, nil
}

// Realpath resolves name to an absolute host path without symlinks.
func (c *Client) Realpath(ctx context.Context, name string) (string, error) {
	r, err := c.call(ctx, opRealpath, "realpath", name, &request{Path: name}, nil)
	if err != nil {
		return "", err
	}
	return r.rep.Str, nil
}

// MaxReadFile is the largest file ReadFile returns; use Download (or
// ReadFileLimit) to choose another bound.
const MaxReadFile = 64 << 20

// ReadFile returns the whole content of a file of at most MaxReadFile bytes.
// Files up to SmallFile bytes take one round trip on the control stream;
// larger ones stream.
func (c *Client) ReadFile(ctx context.Context, name string) ([]byte, error) {
	return c.ReadFileLimit(ctx, name, MaxReadFile)
}

// ReadFileLimit is ReadFile with a caller-chosen bound. A larger file fails
// with EFBIG, whatever size the host announced: the bound holds even
// against a host that lies or a file that grows during the read.
func (c *Client) ReadFileLimit(ctx context.Context, name string, max int64) ([]byte, error) {
	data, ok, err := c.ReadSmall(ctx, name)
	if err != nil || ok {
		if ok && int64(len(data)) > max {
			return nil, remoteErr("read", name, "EFBIG", "")
		}
		return data, err
	}
	rd, err := c.Download(ctx, name, 0)
	if err != nil {
		return nil, err
	}
	defer rd.Close()
	var buf bytes.Buffer
	if sz := rd.Info().Size(); sz > 0 && sz <= max {
		buf.Grow(int(sz))
	}
	n, err := buf.ReadFrom(io.LimitReader(rd, max+1))
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, remoteErr("read", name, "EFBIG", "")
	}
	return buf.Bytes(), nil
}

// ReadSmall reads a whole file in one round trip if it holds at most
// SmallFile bytes; otherwise ok is false and nothing was transferred (the
// caller should stream it with Download).
func (c *Client) ReadSmall(ctx context.Context, name string) (data []byte, ok bool, err error) {
	r, err := c.call(ctx, opReadFile, "read", name, &request{Path: name, N: SmallFile}, nil)
	if err != nil {
		return nil, false, err
	}
	if r.rep.TooBig {
		return nil, false, nil
	}
	if len(r.data) > SmallFile {
		return nil, false, &fs.PathError{Op: "read", Path: name, Err: errors.New("rfs: oversized read reply")}
	}
	return r.data, true, nil
}

// PutOptions control how WriteFile and Upload install a file.
type PutOptions struct {
	// Mode is the permission of the new file, applied exactly. Zero keeps
	// the mode of the file being replaced, or gives a new file 0666 minus
	// the host's umask.
	Mode fs.FileMode
	// ModTime sets the modification time; zero leaves it at "now".
	ModTime time.Time
	// NoFollow replaces a symlink at the destination instead of writing to
	// the file it points to.
	NoFollow bool
	// Sync fsyncs the data before it is renamed into place.
	Sync bool
	// Unique installs under the first free name, "name (1).ext",
	// "name (2).ext", ..., never replacing anything; the returned
	// FileInfo's Name is the name chosen. The choice is atomic on the
	// host, so concurrent uploads cannot collide.
	Unique bool
	// Partial (Upload only) makes the upload resumable: data goes to this
	// file, which must sit next to the destination and be named
	// PartialPrefix(base)+anything. Its first Offset bytes are kept (the
	// reader supplies what follows) and it survives a broken upload, so a
	// later Upload can continue it. A successful install removes every
	// partial file of the destination. EBUSY means another transfer holds
	// it; ENOTSUP that the host cannot resume (upload normally then).
	Partial string
	Offset  int64
	// PartialKey, when set, says Partial is the name PartialPath returned
	// for this key: the host then checks that, and uses its own name for
	// the partial file. Hosts refuse client-chosen partial names
	// (ENOTSUP) for sessions on a grant.
	PartialKey string
}

func (o PutOptions) mode() uint32 {
	if o.Mode == 0 {
		return 0
	}
	return ModeToUnix(o.Mode) & 0o7777
}

// WriteFile replaces name with data atomically: the host writes a temporary
// file in the same directory and renames it over name. perm applies as in
// PutOptions.Mode.
func (c *Client) WriteFile(ctx context.Context, name string, data []byte, perm fs.FileMode) error {
	_, err := c.Put(ctx, name, data, PutOptions{Mode: perm})
	return err
}

// Put is WriteFile with options; it returns the new file's attributes.
func (c *Client) Put(ctx context.Context, name string, data []byte, o PutOptions) (*FileInfo, error) {
	if len(data) > SmallFile {
		return c.Upload(ctx, name, bytes.NewReader(data), o)
	}
	r, err := c.call(ctx, opWriteFile, "write", name, &request{
		Path: name, Mode: o.mode(), Mtime: timePtr(o.ModTime), NoFollow: o.NoFollow, Sync: o.Sync, Unique: o.Unique,
	}, data)
	if err != nil {
		return nil, err
	}
	if r.rep.Attr == nil {
		return nil, &fs.PathError{Op: "write", Path: name, Err: errors.New("rfs: missing attributes")}
	}
	return newInfo(*r.rep.Attr), nil
}

// Upload streams r to name on a dedicated stream and installs it atomically
// once r is exhausted. If r fails or ctx is cancelled the destination is
// left untouched.
func (c *Client) Upload(ctx context.Context, name string, r io.Reader, o PutOptions) (*FileInfo, error) {
	st, err := c.conn.Open(ctx, ServicePut, putReq{
		Path: name, Mode: o.mode(), Mtime: timePtr(o.ModTime), NoFollow: o.NoFollow, Sync: o.Sync,
		Unique: o.Unique, Partial: o.Partial, Off: o.Offset, PartialKey: o.PartialKey,
	})
	if err != nil {
		return nil, &fs.PathError{Op: "upload", Path: name, Err: err}
	}
	stop := context.AfterFunc(ctx, st.Reset)
	defer stop()
	fail := func(err error) (*FileInfo, error) {
		st.Reset()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, &fs.PathError{Op: "upload", Path: name, Err: err}
	}

	buf := make([]byte, 5+bulkChunk)
	var werr error
	for werr == nil {
		n, rerr := io.ReadFull(r, buf[5:])
		if n > 0 {
			binary.BigEndian.PutUint32(buf, uint32(n))
			buf[4] = bulkData
			_, werr = st.Write(buf[:5+n])
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if werr == nil {
		werr = wire.WriteFrame(st, bulkEnd, nil)
	}
	// Read the host's status even after a write error: the host stops
	// reading when it fails (say ENOSPC) and its status explains why.
	st.CloseWrite()
	st.SetReadDeadline(time.Now().Add(2 * time.Minute))
	typ, payload, err := wire.ReadFrame(st, nil)
	if err != nil {
		if werr != nil {
			return fail(werr)
		}
		return fail(err)
	}
	st.Close()
	if typ != bulkEnd {
		return fail(errors.New("rfs: unexpected frame in upload reply"))
	}
	var bs bulkStatus
	if err := json.Unmarshal(payload, &bs); err != nil {
		return fail(err)
	}
	if bs.Code != "" {
		return nil, remoteErr("upload", name, bs.Code, bs.Message)
	}
	if werr != nil {
		return fail(werr)
	}
	if bs.Attr == nil {
		return fail(errors.New("rfs: missing attributes"))
	}
	return newInfo(*bs.Attr), nil
}

// Reader streams a remote file. It is returned by Download.
type Reader struct {
	st   peer.Stream
	stop func() bool
	name string
	info *FileInfo
	buf  []byte
	rest []byte
	err  error
}

// Download opens name for streaming from offset off on a dedicated stream.
// Cancelling ctx aborts the transfer.
func (c *Client) Download(ctx context.Context, name string, off int64) (*Reader, error) {
	st, err := c.conn.Open(ctx, ServiceGet, getReq{Path: name, Off: off})
	if err != nil {
		return nil, &fs.PathError{Op: "download", Path: name, Err: err}
	}
	rd := &Reader{st: st, name: name, stop: context.AfterFunc(ctx, st.Reset)}
	typ, payload, err := wire.ReadFrame(st, nil)
	if err == nil {
		switch typ {
		case bulkHeader:
			var a Attr
			if err = json.Unmarshal(payload, &a); err == nil {
				rd.info = newInfo(a)
				rd.buf = make([]byte, bulkChunk)
				return rd, nil
			}
		case bulkEnd:
			err = rd.status(payload)
			if err == nil {
				err = errors.New("rfs: empty download reply")
			}
		default:
			err = errors.New("rfs: unexpected frame")
		}
	}
	rd.Close()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return nil, err
	}
	return nil, &fs.PathError{Op: "download", Path: name, Err: err}
}

func (rd *Reader) status(payload []byte) error {
	var bs bulkStatus
	if err := json.Unmarshal(payload, &bs); err != nil {
		return err
	}
	if bs.Code != "" {
		return remoteErr("download", rd.name, bs.Code, bs.Message)
	}
	return nil
}

// Info returns the file's attributes as of the start of the transfer.
func (rd *Reader) Info() *FileInfo { return rd.info }

// Read implements io.Reader. It returns io.EOF only after the host has
// confirmed the whole file was sent, so a truncated transfer is an error.
func (rd *Reader) Read(p []byte) (int, error) {
	for len(rd.rest) == 0 {
		if rd.err != nil {
			return 0, rd.err
		}
		typ, payload, err := wire.ReadFrame(rd.st, rd.buf)
		switch {
		case err != nil:
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			rd.err = &fs.PathError{Op: "download", Path: rd.name, Err: err}
		case typ == bulkData:
			rd.rest = payload
		case typ == bulkEnd:
			rd.err = rd.status(payload)
			if rd.err == nil {
				rd.err = io.EOF
			}
		default:
			rd.err = &fs.PathError{Op: "download", Path: rd.name, Err: errors.New("rfs: unexpected frame")}
		}
	}
	n := copy(p, rd.rest)
	rd.rest = rd.rest[n:]
	return n, nil
}

// Close ends the transfer, aborting it if it is not complete.
func (rd *Reader) Close() error {
	rd.stop()
	if rd.err == io.EOF {
		return rd.st.Close()
	}
	rd.st.Reset()
	return nil
}

// File is an open remote file handle.
type File struct {
	c      *Client
	h      uint64
	name   string
	append bool
	info   *FileInfo
	closed atomic.Bool
}

// OpenFile opens name with os.O_* flags; perm applies when a file is
// created.
func (c *Client) OpenFile(ctx context.Context, name string, flag int, perm fs.FileMode) (*File, error) {
	w := wireFlags(flag)
	r, err := c.call(ctx, opOpen, "open", name, &request{Path: name, Flags: w, Mode: ModeToUnix(perm) & 0o7777}, nil)
	if err != nil {
		return nil, err
	}
	f := &File{c: c, h: r.rep.Handle, name: name, append: w&wAppend != 0}
	if r.rep.Attr != nil {
		f.info = newInfo(*r.rep.Attr)
	}
	return f, nil
}

// Open opens name read-only.
func (c *Client) Open(ctx context.Context, name string) (*File, error) {
	return c.OpenFile(ctx, name, 0, 0)
}

// Create creates or truncates name for writing (mode 0666 before umask).
func (c *Client) Create(ctx context.Context, name string) (*File, error) {
	return c.OpenFile(ctx, name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

// Name is the path the file was opened with.
func (f *File) Name() string { return f.name }

// Info returns the attributes the file had when it was opened.
func (f *File) Info() *FileInfo { return f.info }

// ioParallel bounds the concurrent chunk requests of one ReadAt/WriteAt.
const ioParallel = 16

// chunks runs fn over p split into MaxIO pieces, concurrently, and returns
// how many leading bytes completed and the first error in offset order.
func chunks(p []byte, off int64, fn func(b []byte, off int64) (int, error)) (int, error) {
	if len(p) <= MaxIO {
		return fn(p, off)
	}
	n := (len(p) + MaxIO - 1) / MaxIO
	ns := make([]int, n)
	errs := make([]error, n)
	sem := make(chan struct{}, ioParallel)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		lo := i * MaxIO
		hi := min(lo+MaxIO, len(p))
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ns[i], errs[i] = fn(p[lo:hi], off+int64(lo))
		}()
	}
	wg.Wait()
	total := 0
	for i := 0; i < n; i++ {
		total += ns[i]
		if errs[i] != nil {
			return total, errs[i]
		}
		if ns[i] < min(MaxIO, len(p)-i*MaxIO) {
			break
		}
	}
	return total, nil
}

// ReadAt reads len(p) bytes at off. Large reads are split and pipelined. It
// returns io.EOF when the file ends before p is full.
func (f *File) ReadAt(ctx context.Context, p []byte, off int64) (int, error) {
	n, err := chunks(p, off, func(b []byte, o int64) (int, error) {
		r, err := f.c.call(ctx, opRead, "read", f.name, &request{Handle: f.h, Off: o, N: len(b)}, nil)
		if err != nil {
			return 0, err
		}
		if len(r.data) > len(b) {
			return 0, &fs.PathError{Op: "read", Path: f.name, Err: errors.New("rfs: oversized read reply")}
		}
		return copy(b, r.data), nil
	})
	if err == nil && n < len(p) {
		err = io.EOF
	}
	return n, err
}

// WriteAt writes p at off. On a file opened with os.O_APPEND the offset is
// ignored and data is appended, in order.
func (f *File) WriteAt(ctx context.Context, p []byte, off int64) (int, error) {
	write := func(b []byte, o int64) (int, error) {
		r, err := f.c.call(ctx, opWrite, "write", f.name, &request{Handle: f.h, Off: o}, b)
		if err != nil {
			return 0, err
		}
		if r.rep.N != len(b) {
			return r.rep.N, &fs.PathError{Op: "write", Path: f.name, Err: io.ErrShortWrite}
		}
		return r.rep.N, nil
	}
	if !f.append {
		return chunks(p, off, write)
	}
	total := 0
	for len(p) > 0 {
		b := p[:min(len(p), MaxIO)]
		n, err := write(b, 0)
		total += n
		if err != nil {
			return total, err
		}
		p = p[len(b):]
	}
	return total, nil
}

// Stat returns the file's current attributes (fstat).
func (f *File) Stat(ctx context.Context) (*FileInfo, error) {
	r, err := f.c.call(ctx, opFstat, "fstat", f.name, &request{Handle: f.h}, nil)
	if err != nil {
		return nil, err
	}
	if r.rep.Attr == nil {
		return nil, &fs.PathError{Op: "fstat", Path: f.name, Err: errors.New("rfs: missing attributes")}
	}
	return newInfo(*r.rep.Attr), nil
}

// Truncate changes the file's size (ftruncate).
func (f *File) Truncate(ctx context.Context, size int64) error {
	_, err := f.c.call(ctx, opFtruncate, "truncate", f.name, &request{Handle: f.h, Size: size}, nil)
	return err
}

// Sync flushes the file to stable storage on the host (fsync).
func (f *File) Sync(ctx context.Context) error {
	_, err := f.c.call(ctx, opFsync, "fsync", f.name, &request{Handle: f.h}, nil)
	return err
}

// Close releases the handle. Closing twice is a no-op.
func (f *File) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	if err := f.c.closeHandle(f.h); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			pe.Path = f.name
		}
		return err
	}
	return nil
}
