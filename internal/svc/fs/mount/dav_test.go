package mount

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// davFixture is a WebDAV server over a real loopback session, not mounted:
// the tests speak to it as the macOS and Windows clients do.
type davFixture struct {
	t      *testing.T
	d      *WebDAV
	conn   *peer.Conn
	remote string
}

func newDavFixture(t *testing.T, o Options) *davFixture {
	t.Helper()
	remote := t.TempDir()
	conn, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	o.ServeOnly = true
	if o.ReconnectFor == 0 {
		o.ReconnectFor = -1
	}
	d, err := NewWebDAV(ctx, conn, remote, "", o)
	if err != nil {
		t.Fatal(err)
	}
	f := &davFixture{t: t, d: d, conn: conn, remote: remote}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("server request log:\n%s", d.RequestLog())
		}
		d.Unmount()
	})
	return f
}

func (f *davFixture) r(p string) string { return filepath.Join(f.remote, filepath.FromSlash(p)) }

// do sends one request for path p (relative to the share) with optional
// "Header: value" pairs and returns status and body.
func (f *davFixture) do(method, p string, body []byte, hdr ...string) (int, string, http.Header) {
	f.t.Helper()
	return f.raw(method, f.d.URL+strings.TrimPrefix(p, "/"), body, hdr...)
}

func (f *davFixture) raw(method, u string, body []byte, hdr ...string) (int, string, http.Header) {
	f.t.Helper()
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (f *davFixture) want(status int, method, p string, body []byte, hdr ...string) string {
	f.t.Helper()
	got, b, _ := f.do(method, p, body, hdr...)
	if got != status {
		f.t.Fatalf("%s %s: status %d, want %d (%.200s)", method, p, got, status, b)
	}
	return b
}

var hrefRE = regexp.MustCompile(`<D:href>([^<]*)</D:href>`)

// hrefs lists the names in a PROPFIND reply, relative to the share.
func (f *davFixture) hrefs(body string) []string {
	var out []string
	for _, m := range hrefRE.FindAllStringSubmatch(body, -1) {
		p, _ := url.PathUnescape(m[1])
		out = append(out, strings.TrimPrefix(p, "/"+f.d.secret))
	}
	return out
}

func TestDavSecurity(t *testing.T) {
	f := newDavFixture(t, Options{})
	os.WriteFile(f.r("x"), []byte("data"), 0o644)
	base := strings.TrimSuffix(f.d.URL, f.d.secret+"/")

	if got := f.want(200, "GET", "x", nil); got != "data" {
		t.Fatalf("GET: %q", got)
	}
	// Without the secret, or with a wrong or truncated one: nothing.
	for _, p := range []string{"x", "wrong/x", f.d.secret[:31] + "/x", f.d.secret + "x/x", strings.ToUpper(f.d.secret) + "0/x"} {
		for _, m := range []string{"GET", "PUT", "DELETE", "PROPFIND", "MKCOL"} {
			if code, _, _ := f.raw(m, base+p, nil); code != 404 {
				t.Errorf("%s /%s: %d, want 404", m, p, code)
			}
		}
	}
	// The root says only that a WebDAV server is here.
	if code, body, h := f.raw("OPTIONS", base, nil); code != 200 || !strings.Contains(h.Get("DAV"), "1") || body != "" {
		t.Errorf("OPTIONS /: %d %q", code, h.Get("DAV"))
	}
	if code, body, _ := f.raw("PROPFIND", base, nil, "Depth", "1"); code != 207 || strings.Contains(body, f.d.secret) || strings.Count(body, "<D:response>") != 1 {
		t.Errorf("PROPFIND /: %d %s", code, body)
	}
	// Not for web pages, whatever they know.
	port := f.d.ln.Addr().String()[len("127.0.0.1:"):]
	for _, h := range [][]string{
		{"Origin", "http://evil.example"}, {"Origin", "null"},
		{"Sec-Fetch-Site", "cross-site"}, {"Sec-Fetch-Site", "same-site"},
		{"Host", "evil.example:" + port},
		{"Host", "evil.example"}, {"Host", "127.0.0.1:1"}, {"Host", "127.0.0.1.evil.example"},
	} {
		for _, m := range []string{"GET", "PUT", "OPTIONS"} {
			if code, _, _ := f.do(m, "x", []byte("pwned"), h...); code != 403 {
				t.Errorf("%s with %v: %d, want 403", m, h, code)
			}
		}
	}
	if b, _ := os.ReadFile(f.r("x")); string(b) != "data" {
		t.Fatalf("a refused request wrote: %q", b)
	}
	// localhost is this machine too.
	if code, _, _ := f.do("GET", "x", nil, "Host", "localhost:"+port); code != 200 {
		t.Errorf("Host localhost: %d", code)
	}
	// Paths cannot climb out of the remote root.
	os.WriteFile(filepath.Join(filepath.Dir(f.remote), "outside"), []byte("o"), 0o644)
	defer os.Remove(filepath.Join(filepath.Dir(f.remote), "outside"))
	for _, p := range []string{"../outside", "a/../../outside", "%2e%2e/outside", "..%2foutside"} {
		if code, body, _ := f.raw("GET", f.d.URL+p, nil); code == 200 && body == "o" {
			t.Errorf("GET %s escaped the root", p)
		}
	}
	if strings.Contains(f.d.RequestLog(), f.d.secret) {
		t.Error("the request log holds the secret")
	}
}

func TestDavFileOperations(t *testing.T) {
	f := newDavFixture(t, Options{})
	// PUT, GET, overwrite.
	f.want(201, "PUT", "a.txt", []byte("one"))
	if b, _ := os.ReadFile(f.r("a.txt")); string(b) != "one" {
		t.Fatalf("PUT: %q", b)
	}
	f.want(201, "PUT", "a.txt", []byte("two!"))
	if got := f.want(200, "GET", "a.txt", nil); got != "two!" {
		t.Fatalf("GET: %q", got)
	}
	if got := f.want(206, "GET", "a.txt", nil, "Range", "bytes=1-2"); got != "wo" {
		t.Fatalf("range: %q", got)
	}
	if code, _, h := f.do("HEAD", "a.txt", nil); code != 200 || h.Get("Content-Length") != "4" || h.Get("ETag") == "" {
		t.Fatalf("HEAD: %d %v", code, h)
	}
	f.want(404, "GET", "missing", nil)
	f.want(409, "PUT", "nodir/x", []byte("x")) // no parent collection
	// Collections.
	f.want(201, "MKCOL", "dir", nil)
	f.want(405, "MKCOL", "dir", nil)
	f.want(201, "PUT", "dir/b.txt", []byte("bee"))
	if fi, err := os.Stat(f.r("dir")); err != nil || !fi.IsDir() {
		t.Fatal("MKCOL did not reach the host")
	}
	// MOVE and COPY, with and without overwriting.
	dst := func(p string) string { return f.d.URL + p }
	f.want(201, "MOVE", "a.txt", nil, "Destination", dst("dir/moved.txt"))
	if _, err := os.Stat(f.r("a.txt")); err == nil {
		t.Fatal("MOVE left the source")
	}
	f.want(201, "COPY", "dir/moved.txt", nil, "Destination", dst("copy.txt"))
	f.want(412, "COPY", "dir/b.txt", nil, "Destination", dst("copy.txt"), "Overwrite", "F")
	f.want(204, "COPY", "dir/b.txt", nil, "Destination", dst("copy.txt"), "Overwrite", "T")
	if b, _ := os.ReadFile(f.r("copy.txt")); string(b) != "bee" {
		t.Fatalf("COPY: %q", b)
	}
	f.want(201, "MOVE", "dir", nil, "Destination", dst("renamed"))
	if b, _ := os.ReadFile(f.r("renamed/moved.txt")); string(b) != "two!" {
		t.Fatalf("directory MOVE: %q", b)
	}
	// PROPFIND.
	body := f.want(207, "PROPFIND", "", nil, "Depth", "1")
	got := strings.Join(f.hrefs(body), " ")
	if got != "/ /copy.txt /renamed/" {
		t.Fatalf("listing: %q", got)
	}
	if !strings.Contains(body, "<D:getcontentlength>3</D:getcontentlength>") || !strings.Contains(body, "<D:collection") {
		t.Fatalf("listing lacks properties:\n%s", body)
	}
	body = f.want(207, "PROPFIND", "copy.txt", nil, "Depth", "0")
	if !strings.Contains(body, "text/plain") {
		t.Fatalf("content type by extension missing:\n%s", body)
	}
	// DELETE, files and trees.
	f.want(204, "DELETE", "copy.txt", nil)
	f.want(204, "DELETE", "renamed/", nil)
	if ents, _ := os.ReadDir(f.remote); len(ents) != 0 {
		t.Fatalf("left on the host: %v", ents)
	}
	// Remote changes show up within the cache lifetime.
	os.WriteFile(f.r("late"), []byte("l"), 0o644)
	time.Sleep(davAttrTTL + 100*time.Millisecond)
	if got := f.want(200, "GET", "late", nil); got != "l" {
		t.Fatalf("late: %q", got)
	}
	// Symlinks appear as what they point to; dangling ones not at all.
	if os.Symlink("late", f.r("link")) == nil {
		os.Symlink("nowhere", f.r("dangling"))
		time.Sleep(davAttrTTL + 100*time.Millisecond)
		if got := f.want(200, "GET", "link", nil); got != "l" {
			t.Fatalf("through link: %q", got)
		}
		if got := strings.Join(f.hrefs(f.want(207, "PROPFIND", "", nil, "Depth", "1")), " "); got != "/ /late /link" {
			t.Fatalf("listing with links: %q", got)
		}
	}
}

func TestDavLargeFileAndListing(t *testing.T) {
	f := newDavFixture(t, Options{})
	data := make([]byte, 20<<20+123)
	rand.Read(data)
	want := sha256.Sum256(data)
	f.want(201, "PUT", "big.bin", data)
	b, _ := os.ReadFile(f.r("big.bin"))
	if sha256.Sum256(b) != want {
		t.Fatal("uploaded file differs on the host")
	}
	resp, err := http.Get(f.d.URL + "big.bin")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	n, _ := io.Copy(h, resp.Body)
	resp.Body.Close()
	if n != int64(len(data)) || !bytes.Equal(h.Sum(nil), want[:]) {
		t.Fatalf("downloaded %d bytes, hash mismatch", n)
	}
	if got := f.want(206, "GET", "big.bin", nil, "Range", fmt.Sprintf("bytes=%d-", len(data)-5)); got != string(data[len(data)-5:]) {
		t.Fatal("tail range")
	}
	// A big directory is one listing on the host, not a stat per entry:
	// 2000 entries must not take 2000 round trips' worth of time.
	os.Mkdir(f.r("many"), 0o755)
	for i := 0; i < 2000; i++ {
		os.WriteFile(f.r(fmt.Sprintf("many/f%04d.go", i)), []byte("x"), 0o644)
	}
	body := f.want(207, "PROPFIND", "many/", nil, "Depth", "1")
	if n := len(f.hrefs(body)); n != 2001 {
		t.Fatalf("%d entries listed", n)
	}
	// Concurrent clients.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := fmt.Sprintf("c%d", i)
			payload := bytes.Repeat([]byte{byte(i)}, 100000+i)
			if code, _, _ := f.do("PUT", p, payload); code != 201 {
				t.Errorf("PUT %s: %d", p, code)
			}
			if code, got, _ := f.do("GET", p, nil); code != 200 || got != string(payload) {
				t.Errorf("GET %s: %d", p, code)
			}
		}()
	}
	wg.Wait()
}

// The sequence Finder (WebDAVFS) uses to save a file: LOCK the new name,
// PUT under the lock, UNLOCK, with AppleDouble and .DS_Store files around
// it.
func TestDavMacOSClientSequence(t *testing.T) {
	f := newDavFixture(t, Options{})
	const ua = "WebDAVFS/3.0.0 (03008000) Darwin/24.0.0 (arm64)"
	lockBody := []byte(`<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner><D:href>http://www.apple.com/webdav_fs/</D:href></D:owner></D:lockinfo>`)
	if code, _, h := f.do("OPTIONS", "", nil, "User-Agent", ua); code != 200 || !strings.Contains(h.Get("DAV"), "2") {
		t.Fatalf("OPTIONS: %d DAV=%q (class 2 is required for a read-write mount)", code, h.Get("DAV"))
	}
	f.want(404, "PROPFIND", "doc.txt", nil, "Depth", "0", "User-Agent", ua)
	code, body, h := f.do("LOCK", "doc.txt", lockBody, "Timeout", "Second-600", "Depth", "0", "User-Agent", ua)
	if code != 201 {
		t.Fatalf("LOCK on a new name: %d %s", code, body)
	}
	token := strings.Trim(h.Get("Lock-Token"), "<>")
	if token == "" {
		t.Fatalf("no lock token: %v", h)
	}
	if fi, err := os.Stat(f.r("doc.txt")); err != nil || fi.Size() != 0 {
		t.Fatalf("LOCK did not create an empty file: %v", err)
	}
	// Without the token the locked file cannot be written.
	f.want(423, "PUT", "doc.txt", []byte("nope"), "User-Agent", ua)
	f.want(201, "PUT", "doc.txt", []byte("content"), "If", "(<"+token+">)", "User-Agent", ua)
	f.want(204, "UNLOCK", "doc.txt", nil, "Lock-Token", "<"+token+">", "User-Agent", ua)
	if b, _ := os.ReadFile(f.r("doc.txt")); string(b) != "content" {
		t.Fatalf("PUT under lock: %q", b)
	}
	// Desktop metadata stays in the mount, off the host, in any directory.
	os.Mkdir(f.r("sub"), 0o755)
	for _, p := range []string{"._doc.txt", ".DS_Store", "sub/._x", "sub/.DS_Store"} {
		f.want(201, "PUT", p, []byte("appledouble"), "User-Agent", ua)
		if got := f.want(200, "GET", p, nil, "User-Agent", ua); got != "appledouble" {
			t.Fatalf("GET %s: %q", p, got)
		}
		f.want(207, "PROPFIND", p, nil, "Depth", "0", "User-Agent", ua)
		if _, err := os.Lstat(f.r(p)); err == nil {
			t.Fatalf("%s reached the host", p)
		}
	}
	f.want(404, "GET", "._never-written", nil)
	// Metadata the host already holds is hidden from listings.
	os.WriteFile(f.r("sub/._stray"), []byte("s"), 0o644)
	os.WriteFile(f.r("sub/real"), []byte("r"), 0o644)
	time.Sleep(davAttrTTL + 100*time.Millisecond)
	if got := strings.Join(f.hrefs(f.want(207, "PROPFIND", "sub/", nil, "Depth", "1")), " "); got != "/sub/ /sub/real" {
		t.Fatalf("listing: %q", got)
	}
	// Renaming the file renames its AppleDouble companion too; deleting a
	// directory drops the metadata below it.
	f.want(201, "MOVE", "doc.txt", nil, "Destination", f.d.URL+"doc2.txt")
	f.want(201, "MOVE", "._doc.txt", nil, "Destination", f.d.URL+"._doc2.txt")
	f.want(200, "GET", "._doc2.txt", nil)
	f.want(204, "DELETE", "sub/", nil)
	f.want(404, "GET", "sub/._x", nil)
	ents, _ := os.ReadDir(f.remote)
	if len(ents) != 1 || ents[0].Name() != "doc2.txt" {
		t.Fatalf("host holds %v", ents)
	}
}

// What the Windows redirector (Microsoft-WebDAV-MiniRedir) does around a
// save: OPTIONS on the server root and the share, PROPFIND with depth,
// GET with "translate: f", and PUT of an empty file, LOCK, PUT, UNLOCK.
func TestDavWindowsClientSequence(t *testing.T) {
	f := newDavFixture(t, Options{})
	const ua = "Microsoft-WebDAV-MiniRedir/10.0.26100"
	base := strings.TrimSuffix(f.d.URL, f.d.secret+"/")
	for _, u := range []string{base, f.d.URL, strings.TrimSuffix(f.d.URL, "/")} {
		code, _, h := f.raw("OPTIONS", u, nil, "User-Agent", ua, "translate", "f")
		if code != 200 || h.Get("DAV") == "" || h.Get("MS-Author-Via") != "DAV" {
			t.Fatalf("OPTIONS %s: %d DAV=%q MS-Author-Via=%q", strings.Replace(u, f.d.secret, "<secret>", 1), code, h.Get("DAV"), h.Get("MS-Author-Via"))
		}
	}
	if code, _, _ := f.raw("PROPFIND", base, nil, "User-Agent", ua, "Depth", "0", "translate", "f"); code != 207 {
		t.Fatalf("PROPFIND /: %d", code)
	}
	// The share without its trailing slash is the share.
	if code, body, _ := f.raw("PROPFIND", strings.TrimSuffix(f.d.URL, "/"), nil, "User-Agent", ua, "Depth", "1", "translate", "f"); code != 207 || !strings.Contains(body, "<D:collection") {
		t.Fatalf("PROPFIND share: %d", code)
	}
	f.want(404, "PROPFIND", "New Text Document.txt", nil, "Depth", "0", "User-Agent", ua, "translate", "f")
	f.want(404, "PROPFIND", "desktop.ini", nil, "Depth", "0", "User-Agent", ua, "translate", "f")
	f.want(201, "PUT", "New Text Document.txt", nil, "User-Agent", ua)
	lockBody := []byte(`<?xml version="1.0" encoding="utf-8" ?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner><D:href>RUNNER\user</D:href></D:owner></D:lockinfo>`)
	code, body, h := f.do("LOCK", "New Text Document.txt", lockBody, "Timeout", "Second-3600", "User-Agent", ua, "translate", "f")
	if code != 200 {
		t.Fatalf("LOCK: %d %s", code, body)
	}
	token := strings.Trim(h.Get("Lock-Token"), "<>")
	f.want(201, "PUT", "New Text Document.txt", []byte("hello windows"), "If", "(<"+token+">)", "User-Agent", ua, "translate", "f")
	f.want(207, "PROPPATCH", "New Text Document.txt", []byte(`<?xml version="1.0" encoding="utf-8" ?><D:propertyupdate xmlns:D="DAV:" xmlns:Z="urn:schemas-microsoft-com:"><D:set><D:prop><Z:Win32LastModifiedTime>Thu, 19 Nov 2026 16:00:00 GMT</Z:Win32LastModifiedTime></D:prop></D:set></D:propertyupdate>`),
		"If", "(<"+token+">)", "User-Agent", ua, "translate", "f")
	f.want(204, "UNLOCK", "New Text Document.txt", nil, "Lock-Token", "<"+token+">", "User-Agent", ua, "translate", "f")
	if got := f.want(200, "GET", "New Text Document.txt", nil, "User-Agent", ua, "translate", "f"); got != "hello windows" {
		t.Fatalf("GET: %q", got)
	}
	if b, _ := os.ReadFile(f.r("New Text Document.txt")); string(b) != "hello windows" {
		t.Fatalf("host: %q", b)
	}
	// Explorer's own droppings stay off the host.
	f.want(201, "PUT", "desktop.ini", []byte("[.ShellClassInfo]"), "User-Agent", ua)
	f.want(201, "PUT", "Thumbs.db", []byte("t"), "User-Agent", ua)
	if ents, _ := os.ReadDir(f.remote); len(ents) != 1 {
		t.Fatalf("host holds %v", ents)
	}
	// Rename and delete as Explorer does.
	f.want(201, "MOVE", "New Text Document.txt", nil, "Destination", f.d.URL+"notes.txt", "Overwrite", "F", "User-Agent", ua, "translate", "f")
	f.want(204, "DELETE", "notes.txt", nil, "User-Agent", ua, "translate", "f")
}

func TestDavReadOnly(t *testing.T) {
	f := newDavFixture(t, Options{ReadOnly: true})
	os.WriteFile(f.r("x"), []byte("data"), 0o644)
	os.Mkdir(f.r("d"), 0o755)
	if got := f.want(200, "GET", "x", nil); got != "data" {
		t.Fatal(got)
	}
	f.want(207, "PROPFIND", "", nil, "Depth", "1")
	for _, tc := range [][]string{
		{"PUT", "x"}, {"PUT", "new"}, {"PUT", "._x"}, {"DELETE", "x"}, {"MKCOL", "nd"},
		{"MOVE", "x"}, {"COPY", "x"}, {"PROPPATCH", "x"}, {"DELETE", "d/"},
	} {
		if code, _, _ := f.do(tc[0], tc[1], []byte("w"), "Destination", f.d.URL+"y"); code != 403 {
			t.Errorf("%s %s on a read-only mount: %d, want 403", tc[0], tc[1], code)
		}
	}
	// LOCK must not create anything either.
	lock := []byte(`<D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype></D:lockinfo>`)
	if code, _, _ := f.do("LOCK", "created-by-lock", lock); code/100 == 2 {
		t.Errorf("LOCK created a file on a read-only mount: %d", code)
	}
	ents, _ := os.ReadDir(f.remote)
	if b, _ := os.ReadFile(f.r("x")); len(ents) != 2 || string(b) != "data" {
		t.Fatalf("read-only mount changed the host: %v %q", ents, b)
	}
}

func TestDavSessionLossAndReconnect(t *testing.T) {
	spare, _ := peertest.Pair(t)
	redialed := make(chan struct{}, 4)
	f := newDavFixture(t, Options{ReconnectFor: 20 * time.Second, Redial: func(context.Context) (*peer.Conn, error) {
		redialed <- struct{}{}
		return spare, nil
	}})
	os.WriteFile(f.r("x"), []byte("data"), 0o644)
	f.want(200, "GET", "x", nil)
	f.conn.Close()
	select {
	case <-redialed:
	case <-time.After(10 * time.Second):
		t.Fatal("no redial")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		code, body, _ := f.do("GET", "x", nil)
		if code == 200 && body == "data" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not recover: %d", code)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Without a way back, the server stops instead of serving errors.
	g := newDavFixture(t, Options{})
	g.conn.Close()
	select {
	case <-g.d.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("still serving after the session ended")
	}
	if g.d.Err() == nil {
		t.Fatal("no error reported")
	}
}

// `remohop <ID> mount -o nomount`: the command path (foreground, bg record,
// prompt exit on cancel) without an OS client.
func TestDavCommandServeOnly(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	remote := t.TempDir()
	os.WriteFile(filepath.Join(remote, "hello"), []byte("hi"), 0o644)
	conn, _ := peertest.Pair(t)
	var out, errb lockedBuffer
	env := &cli.Env{Target: "test", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return conn, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := make(chan int, 1)
	go func() {
		code <- cli.Lookup("mount").Run(ctx, env, []string{"-f", "--ro", "-o", "nomount,reconnect=0", ":" + remote})
	}()
	urlRE := regexp.MustCompile(`http://127\.0\.0\.1:\d+/[0-9a-f]{32}/`)
	var u string
	for deadline := time.Now().Add(10 * time.Second); u == ""; {
		if u = urlRE.FindString(out.String()); u == "" {
			if time.Now().After(deadline) {
				t.Fatalf("no URL printed: %q %q", out.String(), errb.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	resp, err := http.Get(u + "hello")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "hi" {
		t.Fatalf("GET through the command's server: %q", b)
	}
	req, _ := http.NewRequest("PUT", u+"w", strings.NewReader("x"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("--ro not enforced by the command: %v", err)
	}
	cancel()
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit %d: %s", c, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command did not exit after cancel")
	}
	if _, err := http.Get(u + "hello"); err == nil {
		t.Fatal("server still up after the command exited")
	}
	// Usage errors.
	for _, args := range [][]string{
		{"--webdav", "--fuse", ":/x", t.TempDir()}, {"-o", "nomount", ":/x", "extra"}, {"-o", "nomount"},
	} {
		var b bytes.Buffer
		if c := cli.Lookup("mount").Run(context.Background(), &cli.Env{Target: "t", Stdout: &b, Stderr: &b}, args); c != cli.ExitUsage {
			t.Errorf("%v: exit %d (%s)", args, c, b.String())
		}
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// e2eEnabled gates the tests that mount with the operating system's own
// WebDAV client.
func e2eEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("REMOHOP_MOUNT_E2E") != "1" {
		t.Skip("set REMOHOP_MOUNT_E2E=1 to mount with this OS's WebDAV client")
	}
}

// e2eMount mounts a fresh remote directory on target with the OS client
// and returns the mount and the remote directory. It always unmounts, and
// prints the server's request log if the test failed.
func e2eMount(t *testing.T, target string) (*WebDAV, string) {
	t.Helper()
	remote := t.TempDir()
	conn, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var logs lockedBuffer
	d, err := NewWebDAV(ctx, conn, remote, target, Options{ReconnectFor: -1, FsName: "remohop e2e",
		Logf: func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) }})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("mount log:\n%s\nserver request log:\n%s", logs.String(), d.RequestLog())
		}
		if err := d.Unmount(); err != nil {
			t.Errorf("unmount: %v", err)
		}
		if d.Target != "" && davMounted(d.Target) {
			t.Errorf("%s is still mounted after Unmount", d.Target)
		}
	})
	return d, remote
}

// e2eExercise uses a mounted tree through ordinary file calls and checks
// every step against the remote directory. root is the mounted directory
// (or drive root).
func e2eExercise(t *testing.T, root, remote string) {
	t.Helper()
	l := func(p string) string { return filepath.Join(root, filepath.FromSlash(p)) }
	r := func(p string) string { return filepath.Join(remote, filepath.FromSlash(p)) }
	// eventually tolerates the client's own caching and write-behind.
	eventually := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(); time.Sleep(200 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within 30s", what)
			}
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Remote content is visible.
	must(os.WriteFile(r("pre.txt"), []byte("remote data"), 0o644))
	eventually("read a remote file", func() bool {
		b, err := os.ReadFile(l("pre.txt"))
		return err == nil && string(b) == "remote data"
	})
	// Create and read back.
	must(os.WriteFile(l("new.txt"), []byte("hello mount"), 0o644))
	eventually("created file on the host", func() bool {
		b, err := os.ReadFile(r("new.txt"))
		return err == nil && string(b) == "hello mount"
	})
	b, err := os.ReadFile(l("new.txt"))
	if err != nil || string(b) != "hello mount" {
		t.Fatalf("read back: %q %v", b, err)
	}
	// Directories and listing.
	must(os.Mkdir(l("dir"), 0o755))
	must(os.WriteFile(l("dir/inner.txt"), []byte("inner"), 0o644))
	eventually("directory on the host", func() bool {
		b, err := os.ReadFile(r("dir/inner.txt"))
		return err == nil && string(b) == "inner"
	})
	ents, err := os.ReadDir(root)
	must(err)
	var names []string
	for _, e := range ents {
		// "lost+found" is davfs2's own, local directory; the Windows redirector lists "." and "..".
		if !isShadowName(e.Name()) && e.Name() != "lost+found" && e.Name() != "." && e.Name() != ".." {
			names = append(names, e.Name())
		}
	}
	if got := strings.Join(names, " "); got != "dir new.txt pre.txt" {
		t.Fatalf("listing: %q", got)
	}
	// Rename a file and a directory.
	must(os.Rename(l("new.txt"), l("renamed.txt")))
	must(os.Rename(l("dir"), l("dir2")))
	eventually("renames on the host", func() bool {
		_, e1 := os.Stat(r("renamed.txt"))
		_, e2 := os.Stat(r("dir2/inner.txt"))
		_, e3 := os.Stat(r("new.txt"))
		return e1 == nil && e2 == nil && e3 != nil
	})
	// A 20 MB hash round trip, both ways.
	data := make([]byte, 20<<20)
	rand.Read(data)
	want := sha256.Sum256(data)
	must(os.WriteFile(l("big.bin"), data, 0o644))
	eventually("large file on the host", func() bool {
		b, err := os.ReadFile(r("big.bin"))
		return err == nil && sha256.Sum256(b) == want
	})
	must(os.WriteFile(r("big2.bin"), data, 0o644))
	eventually("large file read through the mount", func() bool {
		b, err := os.ReadFile(l("big2.bin"))
		return err == nil && sha256.Sum256(b) == want
	})
	// Delete.
	must(os.Remove(l("renamed.txt")))
	must(os.RemoveAll(l("dir2")))
	must(os.Remove(l("big.bin")))
	eventually("deletes on the host", func() bool {
		for _, p := range []string{"renamed.txt", "dir2", "big.bin"} {
			if _, err := os.Lstat(r(p)); err == nil {
				return false
			}
		}
		return true
	})
	// No desktop metadata was left on the host.
	filepath.WalkDir(remote, func(p string, _ os.DirEntry, _ error) error {
		if isShadowName(filepath.Base(p)) {
			t.Errorf("desktop metadata on the host: %s", p)
		}
		return nil
	})
}

// TestDavE2ELinux mounts with davfs2 where it is installed and FUSE works
// (the sandbox this was developed in); it is the Linux stand-in for the
// macOS and Windows end-to-end tests.
func TestDavE2ELinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("davfs2 is the Linux client")
	}
	e2eEnabled(t)
	if os.Geteuid() != 0 {
		t.Skip("mount.davfs needs root")
	}
	if _, err := exec.LookPath("mount.davfs"); err != nil {
		t.Skip("davfs2 (mount.davfs) is not installed")
	}
	d, remote := e2eMount(t, t.TempDir())
	e2eExercise(t, d.Target, remote)
}
