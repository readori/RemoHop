package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// Limits for the file tools.
const (
	defaultReadBytes = 64 << 10
	maxReadBytes     = 1 << 20
	maxWriteBytes    = 4 << 20
	defaultListItems = 500
	maxListItems     = 5000
	maxTransferBytes = 512 << 20
)

type readFileIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Path   string `json:"path" jsonschema:"Absolute path on the remote machine (relative paths and ~ are relative to the remote user's home)."`
	Offset int64  `json:"offset,omitempty" jsonschema:"Byte offset to start reading at (default 0)."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum bytes to return (default 65536, max 1048576)."`
}

type readFileOut struct {
	Path      string `json:"path"`
	Size      int64  `json:"size" jsonschema:"File size in bytes as reported by the host (0 for some special files)."`
	Offset    int64  `json:"offset"`
	BytesRead int    `json:"bytes_read"`
	Content   string `json:"content,omitempty"`
	Binary    bool   `json:"binary,omitempty" jsonschema:"True if the data is not text; content is omitted. Use remohop_download to fetch binary files."`
	More      bool   `json:"more" jsonschema:"True if the file continues after this chunk; call again with offset = offset + bytes_read."`
	Sanitized bool   `json:"sanitized,omitempty" jsonschema:"True if terminal escape sequences or control characters were removed from content."`
}

type writeFileIn struct {
	Target   string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Path     string `json:"path" jsonschema:"Absolute path on the remote machine (relative paths and ~ are relative to the remote user's home)."`
	Content  string `json:"content" jsonschema:"Text to write (UTF-8, max 4 MiB). Written exactly as given; no newline is added."`
	Append   bool   `json:"append,omitempty" jsonschema:"Append to the file instead of replacing it (creates it if missing)."`
	MakeDirs bool   `json:"make_dirs,omitempty" jsonschema:"Create missing parent directories."`
	Mode     string `json:"mode,omitempty" jsonschema:"Octal permission bits for a newly created file, e.g. 0644 or 0755. Existing files keep their mode."`
}

type writeFileOut struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
	Size  int64  `json:"size" jsonschema:"File size after the write."`
}

type listDirIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Path   string `json:"path" jsonschema:"Directory on the remote machine (relative paths and ~ are relative to the remote user's home)."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum entries to return (default 500, max 5000)."`
}

type dirEntry struct {
	Name     string `json:"name"`
	Type     string `json:"type" jsonschema:"file, dir, symlink or other."`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	Link     string `json:"link,omitempty" jsonschema:"Symlink target."`
}

type listDirOut struct {
	Path      string     `json:"path"`
	Entries   []dirEntry `json:"entries"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated,omitempty"`
}

type uploadIn struct {
	Target     string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	LocalPath  string `json:"local_path" jsonschema:"Absolute path of a regular file on THIS computer (where the MCP server runs)."`
	RemotePath string `json:"remote_path" jsonschema:"Destination on the remote machine. If it is an existing directory the file keeps its name inside it."`
	Overwrite  bool   `json:"overwrite,omitempty" jsonschema:"Replace the destination if it exists (default: refuse)."`
}

type downloadIn struct {
	Target     string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	RemotePath string `json:"remote_path" jsonschema:"Regular file on the remote machine."`
	LocalPath  string `json:"local_path" jsonschema:"Absolute destination path on THIS computer. If it is an existing directory the file keeps its name inside it."`
	Overwrite  bool   `json:"overwrite,omitempty" jsonschema:"Replace the destination if it exists (default: refuse)."`
}

type transferOut struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Bytes int64  `json:"bytes"`
}

func (s *Server) registerFiles() {
	addTool(s, "remohop_read_file", "Read a text file on a remote machine",
		"Read up to `limit` bytes (default 64 KiB, max 1 MiB) of a text file on the remote machine starting at `offset`. "+
			"Binary files are detected and not returned (use remohop_download). If more remains, `more` is true: call again with the next offset. "+
			"For logs prefer remohop_exec with tail/grep. Works on /proc and other special files.",
		readOnly(), s.readFile)
	addTool(s, "remohop_write_file", "Write a text file on a remote machine",
		"Create or replace a file on the remote machine with the given text, atomically (a temporary file is renamed into place), or append to it. "+
			"Overwrites without confirmation; read the file first if you are editing. For large or binary files use remohop_upload.",
		destructive(false), s.writeFile)
	addTool(s, "remohop_list_dir", "List a directory on a remote machine",
		"List a directory on the remote machine: name, type, size, permissions, modification time, symlink targets. Sorted by name.",
		readOnly(), s.listDir)
	addTool(s, "remohop_upload", "Copy a local file to a remote machine",
		"Copy one file from THIS computer (where the MCP server runs) to the remote machine, installed atomically. Up to 512 MiB. "+
			"Refuses to replace an existing file unless overwrite is true. For directory trees use the CLI: remohop cp -r / remohop sync.",
		destructive(true), s.upload)
	addTool(s, "remohop_download", "Copy a remote file to this computer",
		"Copy one regular file from the remote machine to THIS computer (where the MCP server runs). Up to 512 MiB. "+
			"Refuses to replace an existing local file unless overwrite is true. Afterwards you can open the local file with your own file tools. For directory trees use the CLI: remohop cp -r / remohop sync.",
		destructive(true), s.download)
}

// withFS runs fn with a filesystem client on the pooled session.
func (s *Server) withFS(ctx context.Context, target string, retry bool, fn func(context.Context, *rfs.Client) error) error {
	return s.withConn(ctx, target, retry, func(ctx context.Context, c *peer.Conn) error {
		fc, err := rfs.NewClient(ctx, c)
		if err != nil {
			return err
		}
		defer fc.Close()
		return fn(ctx, fc)
	})
}

// fsErr turns a host filesystem error into advice for the model.
func fsErr(op, path string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s %s: no such file or directory", op, path)
	case errors.Is(err, fs.ErrPermission):
		// A grant's refusal says which limit applies; keep its words.
		// (Refusals and approval answers under a grant name it.)
		if msg := rfsMsg(err); strings.Contains(msg, "grant ") {
			return fmt.Errorf("%s %s: %s", op, path, auth.CleanText(msg, 500))
		}
		return fmt.Errorf("%s %s: permission denied for the remote user", op, path)
	}
	return fmt.Errorf("%s %s: %w", op, path, err)
}

func (s *Server) readFile(ctx context.Context, in readFileIn) (*reply[readFileOut], error) {
	if in.Path == "" {
		return nil, errors.New("path is required")
	}
	if in.Offset < 0 {
		return nil, errors.New("offset must not be negative")
	}
	limit := clamp(in.Limit, defaultReadBytes, maxReadBytes)
	out := readFileOut{Path: in.Path, Offset: in.Offset}
	var data []byte
	err := s.withFS(ctx, in.Target, true, func(ctx context.Context, c *rfs.Client) error {
		st, err := c.Stat(ctx, in.Path)
		if err != nil {
			return fsErr("read", in.Path, err)
		}
		if st.IsDir() {
			return fmt.Errorf("%s is a directory; use remohop_list_dir", in.Path)
		}
		out.Size = st.Size()
		f, err := c.Open(ctx, in.Path)
		if err != nil {
			return fsErr("read", in.Path, err)
		}
		defer f.Close()
		// One byte more than asked reveals whether the file goes on, even
		// for special files that report size 0.
		buf := make([]byte, limit+1)
		n, err := f.ReadAt(ctx, buf, in.Offset)
		if err != nil && !errors.Is(err, io.EOF) {
			return fsErr("read", in.Path, err)
		}
		data = buf[:n]
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		data, out.More = data[:limit], true
	}
	if out.More {
		data = trimPartialRune(data)
	}
	out.BytesRead = len(data)
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		out.Binary = true
		return textReply(out, fmt.Sprintf("%s looks like a binary file (%d bytes read at offset %d); use remohop_download to fetch it", in.Path, len(data), in.Offset)), nil
	}
	out.Content, out.Sanitized = sanitize(string(data), true)
	msg := out.Content
	if out.More {
		msg += fmt.Sprintf("\n[more data follows: call again with offset %d]", in.Offset+int64(len(data)))
	}
	return textReply(out, msg), nil
}

// trimPartialRune drops an incomplete UTF-8 sequence at the end of a chunk
// that was cut at a byte limit, so a multi-byte character split across two
// reads is not mistaken for binary data.
func trimPartialRune(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		c := b[len(b)-i]
		if !utf8.RuneStart(c) {
			continue
		}
		if !utf8.FullRune(b[len(b)-i:]) {
			return b[:len(b)-i]
		}
		break
	}
	return b
}

func (s *Server) writeFile(ctx context.Context, in writeFileIn) (*reply[writeFileOut], error) {
	if in.Path == "" {
		return nil, errors.New("path is required")
	}
	if len(in.Content) > maxWriteBytes {
		return nil, fmt.Errorf("content is %d bytes; the limit is %d (use remohop_upload for larger files)", len(in.Content), maxWriteBytes)
	}
	if !utf8.ValidString(in.Content) {
		return nil, errors.New("content is not valid UTF-8; use remohop_upload for binary data")
	}
	mode := fs.FileMode(0)
	if in.Mode != "" {
		m, err := strconv.ParseUint(in.Mode, 8, 32)
		if err != nil || m > 0o7777 {
			return nil, fmt.Errorf("mode %q is not an octal permission like 0644", in.Mode)
		}
		mode = fs.FileMode(m)
	}
	out := writeFileOut{Path: in.Path, Bytes: len(in.Content)}
	err := s.withFS(ctx, in.Target, false, func(ctx context.Context, c *rfs.Client) error {
		if in.MakeDirs {
			if dir := remoteDir(in.Path); dir != "" {
				if err := c.MkdirAll(ctx, dir, 0o755); err != nil {
					return fsErr("create directory", dir, err)
				}
			}
		}
		var fi *rfs.FileInfo
		var err error
		if in.Append {
			fi, err = appendFile(ctx, c, in.Path, []byte(in.Content), mode)
		} else {
			fi, err = c.Put(ctx, in.Path, []byte(in.Content), rfs.PutOptions{Mode: mode})
		}
		if err != nil {
			return fsErr("write", in.Path, err)
		}
		if fi != nil {
			out.Size = fi.Size()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	verb := "wrote"
	if in.Append {
		verb = "appended"
	}
	return textReply(out, fmt.Sprintf("%s %d bytes to %s (file is now %d bytes)", verb, out.Bytes, in.Path, out.Size)), nil
}

func appendFile(ctx context.Context, c *rfs.Client, path string, data []byte, mode fs.FileMode) (*rfs.FileInfo, error) {
	if mode == 0 {
		mode = 0o644
	}
	f, err := c.OpenFile(ctx, path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, mode)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	for len(data) > 0 {
		chunk := data[:min(len(data), rfs.MaxIO)]
		if _, err := f.WriteAt(ctx, chunk, 0); err != nil {
			return nil, err
		}
		data = data[len(chunk):]
	}
	return f.Stat(ctx)
}

// remoteDir is the parent of a remote path, which may use either separator
// (the host can be Windows).
func remoteDir(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	switch {
	case i < 0:
		return ""
	case i == 0:
		return p[:1]
	}
	return p[:i]
}

func remoteJoin(dir, name string) string {
	sep := "/"
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		sep = `\`
	}
	return strings.TrimRight(dir, `/\`) + sep + name
}

func (s *Server) listDir(ctx context.Context, in listDirIn) (*reply[listDirOut], error) {
	if in.Path == "" {
		return nil, errors.New("path is required")
	}
	limit := clamp(in.Limit, defaultListItems, maxListItems)
	out := listDirOut{Path: in.Path, Entries: []dirEntry{}}
	var list []*rfs.FileInfo
	err := s.withFS(ctx, in.Target, true, func(ctx context.Context, c *rfs.Client) (err error) {
		list, err = c.ReadDir(ctx, in.Path)
		if err != nil {
			return fsErr("list", in.Path, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Total = len(list)
	var b strings.Builder
	for i, fi := range list {
		if i >= limit {
			out.Truncated = true
			break
		}
		e := dirEntry{Name: clean(fi.Name()), Type: fileType(fi), Size: fi.Size(), Mode: fi.Mode().String(), Modified: fi.ModTime().UTC().Format(time.RFC3339), Link: clean(fi.Target())}
		out.Entries = append(out.Entries, e)
		name := e.Name
		switch e.Type {
		case "dir":
			name += "/"
		case "symlink":
			name += " -> " + e.Link
		}
		fmt.Fprintf(&b, "%s %10d %s %s\n", e.Mode, e.Size, fi.ModTime().UTC().Format("2006-01-02 15:04"), name)
	}
	if out.Total == 0 {
		b.WriteString("(empty directory)\n")
	}
	if out.Truncated {
		fmt.Fprintf(&b, "[showing %d of %d entries]\n", len(out.Entries), out.Total)
	}
	return textReply(out, b.String()), nil
}

func fileType(fi *rfs.FileInfo) string {
	switch {
	case fi.IsDir():
		return "dir"
	case fi.IsSymlink():
		return "symlink"
	case fi.IsRegular():
		return "file"
	}
	return "other"
}

func (s *Server) upload(ctx context.Context, in uploadIn) (*reply[transferOut], error) {
	if in.RemotePath == "" {
		return nil, errors.New("remote_path is required")
	}
	lp, err := guardLocal(in.LocalPath)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(lp)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (for directories use `remohop <ID> cp -r` or `remohop <ID> sync`)", lp)
	}
	if st.Size() > maxTransferBytes {
		return nil, fmt.Errorf("%s is %d bytes; the limit is %d (use `remohop <ID> cp` from a terminal)", lp, st.Size(), int64(maxTransferBytes))
	}
	dest := in.RemotePath
	var written int64
	err = s.withFS(ctx, in.Target, false, func(ctx context.Context, c *rfs.Client) error {
		if rst, err := c.Stat(ctx, dest); err == nil {
			if rst.IsDir() {
				dest = remoteJoin(dest, filepath.Base(lp))
				rst, err = c.Stat(ctx, dest)
			}
			if err == nil && rst != nil && !in.Overwrite {
				return fmt.Errorf("%s already exists on the remote machine; pass overwrite=true to replace it", dest)
			}
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		fi, err := c.Upload(ctx, dest, io.LimitReader(f, maxTransferBytes+1), rfs.PutOptions{Mode: st.Mode().Perm(), ModTime: st.ModTime()})
		if err != nil {
			return fsErr("upload to", dest, err)
		}
		written = fi.Size()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return textReply(transferOut{From: lp, To: dest, Bytes: written}, fmt.Sprintf("uploaded %s -> %s (%d bytes)", lp, dest, written)), nil
}

func (s *Server) download(ctx context.Context, in downloadIn) (*reply[transferOut], error) {
	if in.RemotePath == "" {
		return nil, errors.New("remote_path is required")
	}
	dest, err := guardLocal(in.LocalPath)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(dest); err == nil && st.IsDir() {
		if dest, err = guardLocal(filepath.Join(dest, remoteBase(in.RemotePath))); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(dest); err == nil && !in.Overwrite {
		return nil, fmt.Errorf("%s already exists; pass overwrite=true to replace it", dest)
	}
	var written int64
	err = s.withFS(ctx, in.Target, true, func(ctx context.Context, c *rfs.Client) error {
		st, err := c.Stat(ctx, in.RemotePath)
		if err != nil {
			return fsErr("download", in.RemotePath, err)
		}
		if !st.IsRegular() {
			return fmt.Errorf("%s is not a regular file (for directories use `remohop <ID> cp -r` or `remohop <ID> sync`)", in.RemotePath)
		}
		if st.Size() > maxTransferBytes {
			return fmt.Errorf("%s is %d bytes; the limit is %d (use `remohop <ID> cp` from a terminal)", in.RemotePath, st.Size(), int64(maxTransferBytes))
		}
		rd, err := c.Download(ctx, in.RemotePath, 0)
		if err != nil {
			return fsErr("download", in.RemotePath, err)
		}
		defer rd.Close()
		written, err = writeLocal(dest, io.LimitReader(rd, maxTransferBytes+1), st.Mode().Perm()|0o600, in.Overwrite, maxTransferBytes)
		return err
	})
	if err != nil {
		return nil, err
	}
	return textReply(transferOut{From: in.RemotePath, To: dest, Bytes: written}, fmt.Sprintf("downloaded %s -> %s (%d bytes)", in.RemotePath, dest, written)), nil
}

func remoteBase(p string) string {
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	if p == "" || p == "." || p == ".." {
		return "download"
	}
	return p
}

// rfsMsg is the host's own text of a filesystem error.
func rfsMsg(err error) string {
	var re *rfs.Error
	if errors.As(err, &re) && re.Msg != "" {
		return re.Msg
	}
	return err.Error()
}
