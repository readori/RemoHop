//go:build unix

package rfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

func TestDownloadsDirAndHashRange(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	t.Setenv("HOME", dir)
	t.Setenv("REMOHOP_DOWNLOADS_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	d, err := c.DownloadsDir(ctx)
	if err != nil || d != filepath.Join(dir, "Downloads") {
		t.Fatalf("fallback: %q %v", d, err)
	}
	os.MkdirAll(filepath.Join(dir, "cfg"), 0o755)
	os.WriteFile(filepath.Join(dir, "cfg", "user-dirs.dirs"), []byte("XDG_DOWNLOAD_DIR=\"/nonexistent-abs/dl\"\n"), 0o644)
	if xdgUserDir(filepath.Join(dir, "cfg", "user-dirs.dirs"), "XDG_DOWNLOAD_DIR", dir) != "/nonexistent-abs/dl" {
		t.Fatal("absolute XDG dir")
	}
	os.WriteFile(filepath.Join(dir, "cfg", "user-dirs.dirs"), []byte("XDG_DOWNLOAD_DIR=\"$HOME/Files\"\n"), 0o644)
	// XDG user dirs are a Linux/BSD desktop convention; macOS always
	// uses ~/Downloads.
	want := filepath.Join(dir, "Files")
	if runtime.GOOS == "darwin" {
		want = filepath.Join(dir, "Downloads")
	}
	if d, _ := c.DownloadsDir(ctx); d != want {
		t.Fatalf("xdg: %q, want %q", d, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatal("not created")
	}
	// The host's environment can override the lookup on every OS.
	t.Setenv("REMOHOP_DOWNLOADS_DIR", filepath.Join(dir, "Inbox"))
	if d, _ := c.DownloadsDir(ctx); d != filepath.Join(dir, "Inbox") {
		t.Fatalf("override: %q", d)
	}

	p := filepath.Join(dir, "f")
	os.WriteFile(p, []byte("0123456789"), 0o644)
	sum := sha256.Sum256([]byte("2345"))
	if h, err := c.HashRange(ctx, p, 2, 4); err != nil || h != fmt.Sprintf("%x", sum) {
		t.Fatalf("range: %s %v", h, err)
	}
	sum = sha256.Sum256([]byte("789"))
	if h, _ := c.HashRange(ctx, p, 7, 0); h != fmt.Sprintf("%x", sum) {
		t.Fatal("to end")
	}
	_, err = c.HashRange(ctx, p, 5, 6)
	wantErrno(t, err, syscall.EINVAL)
}

func TestPartialUploadIsLockedAndKept(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	dst := filepath.Join(dir, "f")
	part := filepath.Join(dir, PartialPrefix("f")+"x")
	// A broken upload keeps its partial file.
	ctx2, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		pw.Write(bytes.Repeat([]byte("a"), 3*bulkChunk))
		// Write returns once the client has read the bytes, not once the
		// host has written them: break the upload only when all of it is
		// on disk.
		waitForSize(part, 3*bulkChunk)
		cancel()
		pw.CloseWithError(errors.New("source broke"))
	}()
	if _, err := c.Upload(ctx2, dst, pr, PutOptions{Partial: part}); err == nil {
		t.Fatal("broken upload succeeded")
	}
	var size int64
	for i := 0; i < 50; i++ {
		if fi, err := os.Stat(part); err == nil && fi.Size() == 3*bulkChunk {
			size = fi.Size()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if size == 0 {
		t.Fatal("partial file not kept")
	}
	// While someone holds it, a resume is refused with EBUSY.
	f, _ := os.Open(part)
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	_, err := c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: part, Offset: size})
	wantErrno(t, err, syscall.EBUSY)
	f.Close()
	// Resume past the end, or a partial name not belonging to dst: EINVAL.
	_, err = c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: part, Offset: size + 1})
	wantErrno(t, err, syscall.EINVAL)
	_, err = c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: filepath.Join(dir, "other"), Offset: 0})
	wantErrno(t, err, syscall.EINVAL)
	// Resume completes and cleans up every partial of dst.
	os.WriteFile(filepath.Join(dir, PartialPrefix("f")+"stale"), []byte("old"), 0o600)
	if _, err := c.Upload(ctx, dst, strings.NewReader("bc"), PutOptions{Partial: part, Offset: size}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if int64(len(b)) != size+2 || string(b[size:]) != "bc" {
		t.Fatalf("resumed content: %d bytes", len(b))
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
}

// A client that vanished without closing its session leaves its upload
// waiting with the partial file locked; a retry takes it over instead of
// failing with EBUSY until the session times out.
func TestPartialTakeover(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	dst := filepath.Join(dir, "f")
	part := filepath.Join(dir, PartialPrefix("f")+"x")
	pr, pw := io.Pipe()
	defer pw.Close()
	first := make(chan error, 1)
	go func() {
		_, err := c.Upload(ctx, dst, pr, PutOptions{Partial: part})
		first <- err
	}()
	pw.Write(bytes.Repeat([]byte("a"), bulkChunk)) // then the client "hangs"
	for i := 0; ; i++ {
		if fi, err := os.Stat(part); err == nil && fi.Size() > 0 {
			break
		}
		if i > 100 {
			t.Fatal("no partial file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.Upload(ctx, dst, strings.NewReader("new"), PutOptions{Partial: part, Offset: bulkChunk}); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	pw.CloseWithError(errors.New("gone"))
	if err := <-first; err == nil {
		t.Fatal("the abandoned upload succeeded too")
	}
	b, _ := os.ReadFile(dst)
	if len(b) != bulkChunk+3 || string(b[bulkChunk:]) != "new" {
		t.Fatalf("content: %d bytes", len(b))
	}
}

// A client that opens an upload and goes away before sending anything
// leaves nothing behind: no temporary file, no empty partial file.
func TestAbandonedUploadCreatesNothing(t *testing.T) {
	conn, _ := peertest.Pair(t)
	ctx := tctx(t)
	dir := t.TempDir()
	for _, req := range []putReq{
		{Path: filepath.Join(dir, "f")},
		{Path: filepath.Join(dir, "f"), Partial: filepath.Join(dir, PartialPrefix("f")+"x")},
	} {
		st, err := conn.Open(ctx, ServicePut, req)
		if err != nil {
			t.Fatal(err)
		}
		st.Reset()
	}
	time.Sleep(300 * time.Millisecond) // give the handlers every chance
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("abandoned uploads left %v", ents)
	}
}

// A resumable upload must not write through a partial file that is a link
// to somewhere outside the session's grant (audit finding 3).
func TestGrantPartialUploadConfined(t *testing.T) {
	root, outside := grantDirs(t)
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	dst := filepath.Join(root, "f")
	victim := filepath.Join(outside, "victim")
	// A link named like a partial file, made by the session if it can,
	// or planted on the host.
	part := filepath.Join(root, PartialPrefix("f")+"x")
	if err := c.Symlink(ctx, victim, part); err == nil {
		t.Error("the session made a partial-named link to outside the root")
	} else if err := os.Symlink(victim, part); err != nil {
		t.Fatal(err)
	}
	c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: part, Offset: 0})
	c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: part, Offset: 3})
	// A hard link to the victim (same file system) is refused too.
	hard := filepath.Join(root, PartialPrefix("f")+"h")
	if err := os.Link(victim, hard); err == nil {
		c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: hard, Offset: 0})
	}
	// A partial file in another directory is refused whatever its name.
	c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: filepath.Join(outside, PartialPrefix("f")+"x")})
	wantUntouched(t, outside)
	if b, err := os.ReadFile(victim); err != nil || string(b) != "precious" {
		t.Fatalf("victim: %q %v", b, err)
	}
}

// swapGuard is a testGuard whose rules can be replaced, as reissuing a
// grant replaces them on the host.
type swapGuard struct {
	testGuard
	cur *atomic.Pointer[grant.Rules]
}

func (g swapGuard) File(ctx context.Context, op string, a grant.Access, p string) error {
	return testGuard{g.cur.Load()}.File(ctx, op, a, p)
}
func (g swapGuard) FileScope() (*grant.Scope, error) { return g.cur.Load().Scope(), nil }

// waitForSize waits (up to 20 s, for slow and loaded machines) until the
// file at p has size bytes; the caller's own check reports a failure.
func waitForSize(p string, size int64) {
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if fi, err := os.Stat(p); err == nil && fi.Size() >= size {
			return
		}
	}
}

// breakUpload sends n bytes to dst through the partial file of key and
// then breaks the upload, and returns the partial file's path once the
// host has written all of it.
func breakUpload(t *testing.T, c *Client, dst, key string, n int) string {
	t.Helper()
	ctx := tctx(t)
	part, err := c.PartialPath(ctx, dst, key)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		pw.Write(bytes.Repeat([]byte("a"), n))
		waitForSize(part, int64(n)) // see TestPartialUploadIsLockedAndKept
		cancel()
		pw.CloseWithError(errors.New("source broke"))
	}()
	if _, err := c.Upload(ctx2, dst, pr, PutOptions{Partial: part, PartialKey: key}); err == nil {
		t.Fatal("broken upload succeeded")
	}
	for i := 0; i < 100; i++ {
		if fi, err := os.Stat(part); err == nil && fi.Size() == int64(n) {
			return part
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("partial file not kept")
	return ""
}

// Resumable uploads under a grant: the host names the partial file, binds
// it to the grant credential and opens it only as a regular file inside
// the root (audit finding 3).
func TestGrantPartialResume(t *testing.T) {
	root, outside := grantDirs(t)
	r1, _ := grant.Parse([]string{"files:write:" + root}, nil)
	var cur atomic.Pointer[grant.Rules]
	cur.Store(r1)
	g := swapGuard{cur: &cur}
	c := newGuardedClient(t, g)
	ctx := tctx(t)
	dst := filepath.Join(root, "f")
	n := 2 * bulkChunk

	// Disconnect and resume, from a new session on the same grant.
	part := breakUpload(t, c, dst, "k1", n)
	if !strings.HasPrefix(filepath.Base(part), PartialPrefix("f")) || filepath.Dir(part) != root {
		t.Fatalf("partial path %s", part)
	}
	c.Close()
	c = newGuardedClient(t, g)
	if p2, _ := c.PartialPath(ctx, dst, "k1"); p2 != part {
		t.Fatalf("same key, same grant, different file: %s vs %s", p2, part)
	}
	if p2, _ := c.PartialPath(ctx, dst, "k2"); p2 == part {
		t.Fatal("different key, same file")
	}
	// A client-chosen name is not accepted under a grant (old clients
	// then upload normally), nor a key with a name not derived from it.
	_, err := c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: part, Offset: int64(n)})
	wantErrno(t, err, syscall.ENOTSUP)
	_, err = c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: part, PartialKey: "k2", Offset: int64(n)})
	wantErrno(t, err, syscall.EINVAL)
	_, err = c.Upload(ctx, dst, strings.NewReader("b"), PutOptions{Partial: part, PartialKey: "../x", Offset: int64(n)})
	wantErrno(t, err, syscall.EINVAL)
	os.WriteFile(filepath.Join(root, PartialPrefix("f")+"stale"), []byte("old"), 0o600)
	if _, err := c.Upload(ctx, dst, strings.NewReader("bc"), PutOptions{Partial: part, PartialKey: "k1", Offset: int64(n)}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if b, _ := os.ReadFile(dst); len(b) != n+2 || string(b[n:]) != "bc" {
		t.Fatalf("resumed content: %d bytes", len(b))
	}
	if ents, _ := os.ReadDir(root); len(ents) != 2 { // in.txt, f
		t.Fatalf("partials left: %v", ents)
	}

	// A link (symbolic or hard) where the host's partial name is, to a
	// file outside: refused, the outside file untouched.
	victim := filepath.Join(outside, "victim")
	part, _ = c.PartialPath(ctx, dst, "k3")
	os.Symlink(victim, part)
	_, err = c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: part, PartialKey: "k3"})
	wantErrno(t, err, syscall.EINVAL)
	os.Remove(part)
	if os.Link(victim, part) == nil {
		_, err = c.Upload(ctx, dst, strings.NewReader("overwritten"), PutOptions{Partial: part, PartialKey: "k3"})
		wantErrno(t, err, syscall.EINVAL)
		os.Remove(part)
	}
	wantUntouched(t, outside)

	// Concurrent writers: the second takes the file over (the first is
	// aborted), never both writing it.
	pr, pw := io.Pipe()
	part, _ = c.PartialPath(ctx, dst, "k4")
	first := make(chan error, 1)
	go func() {
		_, err := c.Upload(ctx, dst, pr, PutOptions{Partial: part, PartialKey: "k4"})
		first <- err
	}()
	pw.Write(bytes.Repeat([]byte("a"), bulkChunk))
	for i := 0; ; i++ {
		if fi, err := os.Stat(part); err == nil && fi.Size() > 0 {
			break
		}
		if i > 100 {
			t.Fatal("no partial file")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.Upload(ctx, dst, strings.NewReader("new"), PutOptions{Partial: part, PartialKey: "k4", Offset: bulkChunk}); err != nil {
		t.Fatalf("takeover: %v", err)
	}
	pw.CloseWithError(errors.New("gone"))
	if err := <-first; err == nil {
		t.Fatal("the abandoned upload succeeded too")
	}

	// The grant is reissued: the old partial file is not the new
	// credential's, and the session from before is refused.
	part = breakUpload(t, c, dst, "k5", n)
	r2, _ := grant.Parse([]string{"files:write:" + root}, nil)
	cur.Store(r2)
	if _, err := c.Stat(ctx, dst); err == nil || !strings.Contains(err.Error(), "reissued") {
		t.Fatalf("old session after reissue: %v", err)
	}
	c = newGuardedClient(t, g)
	p2, err := c.PartialPath(ctx, dst, "k5")
	if err != nil || p2 == part {
		t.Fatalf("reissued grant resumes the old partial file: %s %v", p2, err)
	}
	if _, err := c.Upload(ctx, dst, strings.NewReader("fresh"), PutOptions{Partial: p2, PartialKey: "k5"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "fresh" {
		t.Fatalf("content after reissue: %d bytes", len(b))
	}
	// ... and the install cleaned up the old credential's partial file.
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("old partial file left: %v", err)
	}

	// An upload in flight when the grant is reissued is not installed.
	pr, pw = io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := c.Upload(ctx, filepath.Join(root, "g"), pr, PutOptions{})
		done <- err
	}()
	pw.Write([]byte("data"))
	time.Sleep(100 * time.Millisecond)
	r3, _ := grant.Parse([]string{"files:write:" + root}, nil)
	cur.Store(r3)
	pw.Close()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "reissued") {
		t.Fatalf("upload across a reissue: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "g")); !os.IsNotExist(err) {
		t.Fatal("upload across a reissue was installed")
	}
}
