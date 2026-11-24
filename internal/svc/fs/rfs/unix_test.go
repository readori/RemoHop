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
	"syscall"
	"testing"
	"time"

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
		time.Sleep(200 * time.Millisecond)
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
