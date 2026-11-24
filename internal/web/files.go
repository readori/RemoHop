package web

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/svc/fs"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// Remote paths are opaque strings chosen by the user; the gateway only joins
// a directory and one validated name. It never touches its own filesystem
// for these calls: downloads are piped to the browser, uploads from it.

func (s *Server) conn(w http.ResponseWriter, ws *webSession, key string) (*pooled, func(), bool) {
	p, release, aerr := ws.acquire(key, s.now)
	if aerr != nil {
		writeErr(w, aerr)
		return nil, nil, false
	}
	return p, release, true
}

func (s *Server) fsFor(w http.ResponseWriter, r *http.Request, ws *webSession, key string) (*fs.Client, func(), bool) {
	p, release, ok := s.conn(w, ws, key)
	if !ok {
		return nil, nil, false
	}
	c, err := p.fsClient(r.Context())
	if err != nil {
		release()
		writeErr(w, classify(err))
		return nil, nil, false
	}
	return c, release, true
}

// sepOf guesses the host's path separator from a path it gave us.
func sepOf(p string) string {
	if len(p) >= 2 && p[1] == ':' || strings.Contains(p, `\`) && !strings.Contains(p, "/") {
		return `\`
	}
	return "/"
}

func joinRemote(dir, name string) string {
	if dir == "" {
		return name
	}
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir + name
	}
	return dir + sepOf(dir) + name
}

// parentOf returns the lexical parent of a remote path, "" at the top.
func parentOf(p string) string {
	sep := sepOf(p)
	t := strings.TrimRight(p, "/\\")
	if t == "" { // "/"
		return ""
	}
	i := strings.LastIndexAny(t, "/\\")
	switch {
	case i < 0:
		return ""
	case i == 0:
		return sep
	case sep == `\` && i == 2 && t[1] == ':':
		return t[:3] // "C:\"
	}
	return t[:i]
}

func baseName(p string) string {
	t := strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(t, "/\\"); i >= 0 {
		return t[i+1:]
	}
	return t
}

// validName checks a file name given by the page for upload, mkdir and
// rename targets: one path component, nothing that could be read as a path.
func validName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 255 || !utf8.ValidString(n) {
		return false
	}
	for _, r := range n {
		if r == '/' || r == '\\' || r == 0 || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// display makes a host-supplied name harmless to show.
func display(n string) string { return cli.Sanitize(n) }

type entry struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	Size    int64  `json:"size"`
	MTime   int64  `json:"mtime"` // Unix milliseconds
	Mode    string `json:"mode"`
	Dir     bool   `json:"dir"`
	Link    bool   `json:"link,omitempty"`
	Target  string `json:"target,omitempty"`
	// Bad marks a name that cannot round-trip through JSON (invalid UTF-8):
	// it is listed but the page offers no actions on it.
	Bad bool `json:"bad,omitempty"`
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request, ws *webSession) {
	c, release, ok := s.fsFor(w, r, ws, r.URL.Query().Get("c"))
	if !ok {
		return
	}
	defer release()
	ctx := r.Context()
	p := r.URL.Query().Get("path")
	home := ""
	if info, err := c.Info(ctx); err == nil {
		home = info.Home
	}
	if p == "" || p == "~" {
		p = home
	}
	if p == "" {
		p = "/"
	}
	if rp, err := c.Realpath(ctx, p); err == nil && rp != "" {
		p = rp
	}
	infos, err := c.ReadDir(ctx, p)
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	out := make([]entry, 0, len(infos))
	links := 0
	for _, fi := range infos {
		e := entry{Name: fi.Name(), Display: display(fi.Name()), Size: fi.Size(), MTime: fi.ModTime().UnixMilli(),
			Mode: fi.Mode().String(), Dir: fi.IsDir(), Bad: !utf8.ValidString(fi.Name())}
		if fi.IsSymlink() {
			e.Link, e.Target = true, display(fi.Target())
			// Learn what a link points at, for a bounded number of links.
			if links++; links <= 100 {
				if st, err := c.Stat(ctx, joinRemote(p, fi.Name())); err == nil {
					e.Dir, e.Size = st.IsDir(), st.Size()
				}
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display)
	})
	writeJSON(w, http.StatusOK, map[string]any{"path": p, "parent": parentOf(p), "home": home, "sep": sepOf(p), "entries": out})
}

// contentDisposition builds an attachment header with a safe filename in
// both the plain and the RFC 5987 form.
func contentDisposition(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == '"' {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	if name == "" {
		name = "download"
	}
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	ws := s.ticketAuth(w, r, "dl", false)
	if ws == nil {
		return
	}
	q := r.URL.Query()
	p, release, aerr := ws.acquire(q.Get("c"), s.now)
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	defer release()
	c, err := p.fsClient(r.Context())
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	remote := q.Get("path")
	rd, err := c.Download(r.Context(), remote, 0)
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	defer rd.Close()
	if !rd.Info().IsRegular() {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "not_a_file", Message: "only regular files can be downloaded"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", contentDisposition(baseName(remote)))
	h.Set("Content-Length", strconv.FormatInt(rd.Info().Size(), 10))
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// A plain copy: the response write blocks when the browser is slow,
	// which stops reading from the QUIC stream and so slows the host.
	io.Copy(w, rd)
}

type uploadResult struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Renamed bool   `json:"renamed"`
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request, ws *webSession) {
	q := r.URL.Query()
	name := q.Get("name")
	if !validName(name) {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_name", Message: "invalid file name"})
		return
	}
	dir := q.Get("dir")
	if dir == "" {
		writeErr(w, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "missing directory"})
		return
	}
	c, release, ok := s.fsFor(w, r, ws, q.Get("c"))
	if !ok {
		return
	}
	defer release()
	overwrite := q.Get("overwrite") == "1"
	dest := joinRemote(dir, name)
	// The body goes straight into the QUIC stream in 1 MiB chunks; nothing
	// is buffered beyond that, and a slow host slows the browser upload.
	fi, err := c.Upload(r.Context(), dest, r.Body, fs.PutOptions{Unique: !overwrite, NoFollow: true})
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	got := fi.Name()
	if got == "" {
		got = name
	}
	writeJSON(w, http.StatusOK, uploadResult{Name: display(got), Path: joinRemote(dir, got), Size: fi.Size(), Renamed: got != name})
}

type pathReq struct {
	C         string `json:"c"`
	Path      string `json:"path"`
	From      string `json:"from"`
	To        string `json:"to"`
	Recursive bool   `json:"recursive"`
}

func (s *Server) fileOp(w http.ResponseWriter, r *http.Request, ws *webSession, fn func(ctx context.Context, c *fs.Client, req pathReq) *apiError) {
	var req pathReq
	if aerr := readJSON(w, r, &req, 16<<10); aerr != nil {
		writeErr(w, aerr)
		return
	}
	c, release, ok := s.fsFor(w, r, ws, req.C)
	if !ok {
		return
	}
	defer release()
	if aerr := fn(r.Context(), c, req); aerr != nil {
		writeErr(w, aerr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func badName() *apiError {
	return &apiError{Status: http.StatusBadRequest, Code: "bad_name", Message: "invalid file name"}
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request, ws *webSession) {
	s.fileOp(w, r, ws, func(ctx context.Context, c *fs.Client, req pathReq) *apiError {
		if req.Path == "" || !validName(baseName(req.Path)) {
			return badName()
		}
		if err := c.Mkdir(ctx, req.Path, 0o755); err != nil {
			return classify(err)
		}
		return nil
	})
}

// handleRename never replaces an existing file: the host refuses with
// EEXIST and the page says so.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request, ws *webSession) {
	s.fileOp(w, r, ws, func(ctx context.Context, c *fs.Client, req pathReq) *apiError {
		if req.From == "" || req.To == "" || !validName(baseName(req.To)) {
			return badName()
		}
		if err := c.RenameFlags(ctx, req.From, req.To, rfs.RenameNoReplace); err != nil {
			return classify(err)
		}
		return nil
	})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, ws *webSession) {
	s.fileOp(w, r, ws, func(ctx context.Context, c *fs.Client, req pathReq) *apiError {
		if req.Path == "" || path.Clean(strings.ReplaceAll(req.Path, `\`, "/")) == "/" {
			return badName()
		}
		fi, err := c.Lstat(ctx, req.Path)
		if err != nil {
			return classify(err)
		}
		switch {
		case fi.IsDir() && req.Recursive:
			err = c.RemoveAll(ctx, req.Path)
		case fi.IsDir():
			err = c.Rmdir(ctx, req.Path)
		default:
			err = c.Unlink(ctx, req.Path)
		}
		if err != nil {
			return classify(err)
		}
		return nil
	})
}
