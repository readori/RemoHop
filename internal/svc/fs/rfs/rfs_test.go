package rfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// posix is false where the host filesystem has no POSIX permission bits,
// owners or inode numbers to report (Windows): assertions about those are
// made only where they exist.
const posix = runtime.GOOS != "windows"

// setHome points the "host" (this process) at a home directory.
func setHome(t *testing.T, dir string) {
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // what os.UserHomeDir reads on Windows
}

func newTestClient(t *testing.T) (*Client, string) {
	t.Helper()
	// The directory first: cleanups run last-in first-out, and the client
	// (whose host side holds files open) must be closed before Windows
	// will let the directory be removed.
	dir := t.TempDir()
	conn, _ := peertest.Pair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c, dir
}

func tctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wantErrno(t *testing.T, err error, want syscall.Errno) {
	t.Helper()
	// The expectation is printed by POSIX name, which reads the same on
	// every OS (the local errno text does not).
	name := errnoNames[want]
	if err == nil {
		t.Fatalf("want %s (%s), got nil", name, errnoText[name])
	}
	var en syscall.Errno
	if !errors.As(err, &en) || en != want {
		t.Fatalf("want %s (%s), got %v (%T)", name, errnoText[name], err, err)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("want *fs.PathError, got %T", err)
	}
}

func TestStatAndErrors(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1700000000, 123456700) // NTFS keeps 100 ns
	os.Chtimes(f, mt, mt)

	fi, err := c.Stat(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Name() != "f" || fi.Size() != 5 || (posix && fi.Mode() != 0o640) || !fi.ModTime().Equal(mt) || fi.IsDir() || !fi.IsRegular() {
		t.Fatalf("bad stat %+v mode %v", fi.Attr(), fi.Mode())
	}
	if posix && (fi.Attr().Nlink != 1 || fi.Attr().Ino == 0 || fi.Attr().Uid != uint32(os.Getuid())) {
		t.Fatalf("bad unix attrs %+v", fi.Attr())
	}

	_, err = c.Stat(ctx, filepath.Join(dir, "missing"))
	wantErrno(t, err, syscall.ENOENT)
	if !errors.Is(err, fs.ErrNotExist) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("errors.Is(ErrNotExist) false for %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error lacks path: %v", err)
	}
	_, err = c.Stat(ctx, filepath.Join(f, "x"))
	wantErrno(t, err, syscall.ENOTDIR)
	wantErrno(t, c.Mkdir(ctx, f, 0o755), syscall.EEXIST)
	if err := c.Mkdir(ctx, f, 0o755); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	wantErrno(t, c.Rmdir(ctx, f), syscall.ENOTDIR)
	wantErrno(t, c.Unlink(ctx, dir), syscall.EISDIR)

	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "x"), nil, 0o644)
	wantErrno(t, c.Rmdir(ctx, sub), syscall.ENOTEMPTY)

	if posix && os.Getuid() != 0 {
		locked := filepath.Join(dir, "locked")
		os.WriteFile(locked, nil, 0)
		_, err := c.Open(ctx, locked)
		wantErrno(t, err, syscall.EACCES)
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatal("want ErrPermission")
		}
	}

	_, err = c.Stat(ctx, "bad\x00path")
	wantErrno(t, err, syscall.EINVAL)
}

func TestHomeRelative(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	setHome(t, dir)
	os.WriteFile(filepath.Join(dir, "rel"), []byte("x"), 0o644)
	for _, p := range []string{"rel", "~/rel"} {
		fi, err := c.Stat(ctx, p)
		if err != nil || fi.Size() != 1 {
			t.Fatalf("%s: %v", p, err)
		}
	}
	info, err := c.Info(ctx)
	if err != nil || info.Home != dir {
		t.Fatalf("info %+v %v", info, err)
	}
	if rp, err := c.Realpath(ctx, "~"); err != nil || rp == "" {
		t.Fatal(rp, err)
	}
}

// TestPathOpsPortable covers the operations every host supports, with no
// assumption about permission bits or link semantics.
func TestPathOpsPortable(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	p := func(n string) string { return filepath.Join(dir, n) }
	if err := c.MkdirAll(ctx, p("a/b/c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := c.Stat(ctx, p("a/b/c")); err != nil || !fi.IsDir() {
		t.Fatalf("mkdirall: %v", err)
	}
	if err := c.WriteFile(ctx, p("a/f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := c.ReadFile(ctx, p("a/f")); err != nil || string(b) != "data" {
		t.Fatalf("readfile: %q %v", b, err)
	}
	// Replacing an existing file, twice (Windows: no open handle may be
	// left on either the old or the new file).
	for _, v := range []string{"v2", "v3"} {
		if err := c.WriteFile(ctx, p("a/f"), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Truncate(ctx, p("a/f"), 1); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1600000000, 0)
	if err := c.Utimes(ctx, p("a/f"), mt, mt); err != nil {
		t.Fatal(err)
	}
	if fi, err := c.Stat(ctx, p("a/f")); err != nil || fi.Size() != 1 || !fi.ModTime().Equal(mt) {
		t.Fatalf("truncate/utimes: %v", err)
	}
	if err := c.Rename(ctx, p("a/f"), p("a/g")); err != nil {
		t.Fatal(err)
	}
	c.WriteFile(ctx, p("a/h"), []byte("h"), 0o644)
	wantErrno(t, c.RenameFlags(ctx, p("a/g"), p("a/h"), RenameNoReplace), syscall.EEXIST)
	sum := sha256.Sum256([]byte("v"))
	if h, err := c.Hash(ctx, p("a/g")); err != nil || h != fmt.Sprintf("%x", sum) {
		t.Fatalf("hash %s %v", h, err)
	}
	wantErrno(t, c.Rmdir(ctx, p("a")), syscall.ENOTEMPTY)
	wantErrno(t, c.Unlink(ctx, p("a/b")), syscall.EISDIR)
	if err := c.Unlink(ctx, p("a/g")); err != nil {
		t.Fatal(err)
	}
	if err := c.Rmdir(ctx, p("a/b/c")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, p("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, p("a")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a still exists: %v", err)
	}
}

func TestPathOps(t *testing.T) {
	if !posix {
		t.Skip("exercises POSIX semantics a Windows host does not have: permission bits, setgid, hard-link counts, symlink times, atomic exchange, statfs")
	}
	c, dir := newTestClient(t)
	ctx := tctx(t)
	p := func(n string) string { return filepath.Join(dir, n) }

	if err := c.MkdirAll(ctx, p("a/b/c"), 0o750); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p("a/b/c")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750 {
		t.Fatalf("mkdirall: %v %v", fi, err)
	}
	if err := c.Mkdir(ctx, p("d"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFile(ctx, p("d/f"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Symlink(ctx, "d/f", p("link")); err != nil {
		t.Fatal(err)
	}
	if tgt, err := c.Readlink(ctx, p("link")); err != nil || tgt != "d/f" {
		t.Fatalf("readlink %q %v", tgt, err)
	}
	lfi, err := c.Lstat(ctx, p("link"))
	if err != nil || !lfi.IsSymlink() || lfi.Target() != "d/f" {
		t.Fatalf("lstat %+v %v", lfi, err)
	}
	if fi, err := c.Stat(ctx, p("link")); err != nil || !fi.IsRegular() || fi.Size() != 4 {
		t.Fatalf("stat through link %v", err)
	}
	if err := c.Link(ctx, p("d/f"), p("hard")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := c.Stat(ctx, p("hard")); fi.Attr().Nlink != 2 {
		t.Fatalf("nlink %d", fi.Attr().Nlink)
	}
	if err := c.Chmod(ctx, p("hard"), 0o755|fs.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p("d/f")); fi.Mode() != 0o755|fs.ModeSetgid {
		t.Fatalf("chmod: %v", fi.Mode())
	}
	if err := c.Truncate(ctx, p("hard"), 2); err != nil {
		t.Fatal(err)
	}
	mt := time.Unix(1600000000, 5)
	at := time.Unix(1500000000, 7)
	if err := c.Utimes(ctx, p("hard"), at, mt); err != nil {
		t.Fatal(err)
	}
	fi, _ := c.Stat(ctx, p("d/f"))
	if fi.Size() != 2 || !fi.ModTime().Equal(mt) || fi.Attr().Atime != at.UnixNano() {
		t.Fatalf("truncate/utimes: %+v", fi.Attr())
	}
	// A zero time leaves that time alone.
	if err := c.Utimes(ctx, p("hard"), time.Time{}, time.Unix(1650000000, 0)); err != nil {
		t.Fatal(err)
	}
	fi, _ = c.Stat(ctx, p("d/f"))
	if fi.Attr().Atime != at.UnixNano() || fi.ModTime().Unix() != 1650000000 {
		t.Fatalf("partial utimes: %+v", fi.Attr())
	}
	if err := c.Lutimes(ctx, p("link"), time.Time{}, mt); err != nil {
		t.Fatal(err)
	}
	if lfi, _ := os.Lstat(p("link")); !lfi.ModTime().Equal(mt) {
		t.Fatalf("lutimes: %v", lfi.ModTime())
	}

	if err := c.Rename(ctx, p("hard"), p("moved")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p("other"), []byte("o"), 0o644)
	wantErrno(t, c.RenameFlags(ctx, p("moved"), p("other"), RenameNoReplace), syscall.EEXIST)
	if err := c.RenameFlags(ctx, p("moved"), p("other"), RenameExchange); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p("moved")); string(b) != "o" {
		t.Fatalf("exchange: %q", b)
	}

	sf, err := c.StatFS(ctx, dir)
	if err != nil || sf.Blocks == 0 || sf.Bsize == 0 || sf.NameLen == 0 {
		t.Fatalf("statfs %+v %v", sf, err)
	}

	if err := c.Unlink(ctx, p("moved")); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(ctx, p("other")); err != nil {
		t.Fatal(err)
	}
	if err := c.Rmdir(ctx, p("a/b/c")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, p("a")); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveAll(ctx, p("nothing")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p("a")); !os.IsNotExist(err) {
		t.Fatal("a still exists")
	}
	sum := sha256.Sum256([]byte("da"))
	if h, err := c.Hash(ctx, p("d/f")); err != nil || h != fmt.Sprintf("%x", sum) {
		t.Fatalf("hash %s %v", h, err)
	}
}

func TestReadDir(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	if ents, err := c.ReadDir(ctx, dir); err != nil || len(ents) != 0 {
		t.Fatalf("empty dir: %v %v", ents, err)
	}
	// More than one batch.
	const n = dirBatch*2 + 17
	var want []string
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file-%05d", i)
		want = append(want, name)
		os.WriteFile(filepath.Join(dir, name), bytes.Repeat([]byte{'x'}, i%7), 0o644)
	}
	os.Symlink("file-00001", filepath.Join(dir, "zlink"))
	want = append(want, "zlink")
	ents, err := c.ReadDir(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if !sort.StringsAreSorted(got) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	if ents[3].Size() != 3 || !ents[n].IsSymlink() || ents[n].Target() != "file-00001" {
		t.Fatalf("attrs: %+v %+v", ents[3].Attr(), ents[n].Attr())
	}
	_, err = c.ReadDir(ctx, filepath.Join(dir, "file-00001"))
	wantErrno(t, err, syscall.ENOTDIR)
	_, err = c.ReadDir(ctx, filepath.Join(dir, "nope"))
	wantErrno(t, err, syscall.ENOENT)
}

func TestFileHandles(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	p := filepath.Join(dir, "f")

	f, err := c.OpenFile(ctx, p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if f.Info() == nil || !f.Info().IsRegular() {
		t.Fatal("no attrs from open")
	}
	_, err = c.OpenFile(ctx, p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	wantErrno(t, err, syscall.EEXIST)

	// Beyond 4 GiB offsets (sparse). NTFS files are not sparse by default:
	// there the gap would really be written, so a small offset is used.
	big := int64(5 << 30)
	if !posix {
		big = 5 << 20
	}
	if n, err := f.WriteAt(ctx, []byte("tail"), big); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	if _, err := f.WriteAt(ctx, []byte("head"), 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	n, err := f.ReadAt(ctx, buf, big)
	if n != 4 || err != io.EOF || string(buf[:4]) != "tail" {
		t.Fatalf("read at big: %d %v %q", n, err, buf[:n])
	}
	n, err = f.ReadAt(ctx, buf[:4], 0)
	if n != 4 || err != nil || string(buf[:4]) != "head" {
		t.Fatalf("read head: %d %v", n, err)
	}
	fi, err := f.Stat(ctx)
	if err != nil || fi.Size() != big+4 {
		t.Fatalf("fstat: %v %v", fi, err)
	}
	if err := f.Truncate(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal("double close:", err)
	}
	// The handle is gone on the host.
	_, err = f.ReadAt(ctx, buf, 0)
	wantErrno(t, err, syscall.EBADF)

	// O_APPEND appends regardless of offset.
	a, err := c.OpenFile(ctx, p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	a.WriteAt(ctx, []byte("-1"), 0)
	a.WriteAt(ctx, []byte("-2"), 0)
	a.Close()
	if b, _ := os.ReadFile(p); string(b) != "head-1-2" {
		t.Fatalf("append: %q", b)
	}
	// Reading a directory handle fails with EISDIR.
	d, err := c.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.ReadAt(ctx, buf, 0)
	wantErrno(t, err, syscall.EISDIR)
	d.Close()
	// Create truncates.
	cf, err := c.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	cf.Close()
	if fi, _ := os.Stat(p); fi.Size() != 0 {
		t.Fatal("create did not truncate")
	}
}

func randFile(t *testing.T, p string, size int) []byte {
	t.Helper()
	b := make([]byte, size)
	rand.Read(b)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLargeIO(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	src := filepath.Join(dir, "src")
	data := randFile(t, src, 9*MaxIO+12345)

	// Pipelined ReadAt across many chunks.
	f, err := c.Open(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(data)+100)
	n, err := f.ReadAt(ctx, buf, 0)
	if n != len(data) || err != io.EOF || !bytes.Equal(buf[:n], data) {
		t.Fatalf("readat: %d %v", n, err)
	}
	f.Close()

	// Pipelined WriteAt.
	dst := filepath.Join(dir, "dst")
	w, err := c.Create(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.WriteAt(ctx, data, 0); err != nil || n != len(data) {
		t.Fatal(n, err)
	}
	w.Close()
	if b, _ := os.ReadFile(dst); !bytes.Equal(b, data) {
		t.Fatal("writeat content mismatch")
	}

	// Bulk streams.
	got, err := c.ReadFile(ctx, src)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("readfile big: %v", err)
	}
	up := filepath.Join(dir, "up")
	mt := time.Unix(1234567890, 0)
	fi, err := c.Upload(ctx, up, bytes.NewReader(data), PutOptions{Mode: 0o751, ModTime: mt})
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != int64(len(data)) {
		t.Fatalf("upload size %d", fi.Size())
	}
	lfi, _ := os.Stat(up)
	if (posix && lfi.Mode() != 0o751) || !lfi.ModTime().Equal(mt) {
		t.Fatalf("upload attrs %v %v", lfi.Mode(), lfi.ModTime())
	}
	if b, _ := os.ReadFile(up); sha256.Sum256(b) != sha256.Sum256(data) {
		t.Fatal("upload hash mismatch")
	}
	rd, err := c.Download(ctx, src, int64(len(data)-10))
	if err != nil {
		t.Fatal(err)
	}
	tail, err := io.ReadAll(rd)
	rd.Close()
	if err != nil || !bytes.Equal(tail, data[len(data)-10:]) {
		t.Fatalf("download offset: %v", err)
	}
	_, err = c.Download(ctx, dir, 0)
	wantErrno(t, err, syscall.EISDIR)
	_, err = c.Download(ctx, filepath.Join(dir, "nope"), 0)
	wantErrno(t, err, syscall.ENOENT)
}

func TestWriteFileAtomicAndModes(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	p := filepath.Join(dir, "f")
	if err := c.WriteFile(ctx, p, []byte("one"), 0); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); posix && fi.Mode() != 0o666&^Umask() {
		t.Fatalf("default mode %v", fi.Mode())
	}
	os.Chmod(p, 0o600)
	if err := c.WriteFile(ctx, p, []byte("two"), 0); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); posix && fi.Mode() != 0o600 {
		t.Fatalf("replace kept mode? %v", fi.Mode())
	}
	// Writing through a symlink replaces the target, not the link...
	os.Symlink("f", filepath.Join(dir, "l"))
	if err := c.WriteFile(ctx, filepath.Join(dir, "l"), []byte("three"), 0); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "three" {
		t.Fatalf("through link: %q", b)
	}
	// ...unless NoFollow.
	if _, err := c.Put(ctx, filepath.Join(dir, "l"), []byte("four"), PutOptions{NoFollow: true}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "l")); fi.Mode()&fs.ModeSymlink != 0 {
		t.Fatal("NoFollow kept the link")
	}
	if b, _ := os.ReadFile(p); string(b) != "three" {
		t.Fatalf("NoFollow wrote the target: %q", b)
	}
	err := c.WriteFile(ctx, dir, []byte("x"), 0)
	wantErrno(t, err, syscall.EISDIR)
	err = c.WriteFile(ctx, filepath.Join(dir, "nodir", "x"), []byte("x"), 0)
	wantErrno(t, err, syscall.ENOENT)

	// An aborted upload leaves neither the final name nor a temp file.
	ctx2, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		pw.Write(bytes.Repeat([]byte("z"), 3*bulkChunk))
		cancel()
		time.Sleep(100 * time.Millisecond)
		pw.CloseWithError(errors.New("source broke"))
	}()
	if _, err := c.Upload(ctx2, filepath.Join(dir, "partial"), pr, PutOptions{}); err == nil {
		t.Fatal("aborted upload succeeded")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ents, _ := os.ReadDir(dir)
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		if strings.Join(names, ",") == "f,l" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("leftovers after aborted upload: %v", names)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// A failing reader aborts too.
	if _, err := c.Upload(ctx, filepath.Join(dir, "bad"), io.MultiReader(strings.NewReader("x"), errReader{}), PutOptions{}); err == nil {
		t.Fatal("upload with failing reader succeeded")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestConcurrent(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := filepath.Join(dir, fmt.Sprintf("f%d", i))
			data := bytes.Repeat([]byte{byte(i)}, 1000+i*517)
			if err := c.WriteFile(ctx, p, data, 0o644); err != nil {
				errs <- err
				return
			}
			f, err := c.Open(ctx, p)
			if err != nil {
				errs <- err
				return
			}
			defer f.Close()
			buf := make([]byte, len(data))
			if _, err := f.ReadAt(ctx, buf, 0); err != nil && err != io.EOF {
				errs <- err
				return
			}
			if !bytes.Equal(buf, data) {
				errs <- fmt.Errorf("f%d mismatch", i)
			}
			if _, err := c.Stat(ctx, filepath.Join(dir, "nope")); !errors.Is(err, fs.ErrNotExist) {
				errs <- fmt.Errorf("stat nope: %v", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	ents, err := c.ReadDir(ctx, dir)
	if err != nil || len(ents) != 100 {
		t.Fatalf("%d entries %v", len(ents), err)
	}
}

func TestContextCancel(t *testing.T) {
	c, dir := newTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Stat(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
	// The client is still usable.
	if _, err := c.Stat(tctx(t), dir); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLoss(t *testing.T) {
	conn, _ := peertest.Pair(t)
	ctx := tctx(t)
	c, err := NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, "/"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client not done after session close")
	}
	_, err = c.Stat(ctx, "/")
	if !errors.Is(err, syscall.EIO) || Errno(err) != syscall.EIO {
		t.Fatalf("want EIO after session loss, got %v", err)
	}
}

// rawCall writes one raw control frame and reads one reply.
func rawStream(t *testing.T) peer.Stream {
	t.Helper()
	conn, _ := peertest.Pair(t)
	st, err := conn.Open(tctx(t), ServiceControl, openReq{Version: protoVersion})
	if err != nil {
		t.Fatal(err)
	}
	st.SetDeadline(time.Now().Add(10 * time.Second))
	return st
}

func readReply(t *testing.T, st peer.Stream) (byte, uint32, reply) {
	t.Helper()
	typ, payload, err := wire.ReadFrame(st, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, meta, _, err := parseMsg(payload)
	if err != nil {
		t.Fatal(err)
	}
	var rep reply
	if len(meta) > 0 {
		json.Unmarshal(meta, &rep)
	}
	return typ, id, rep
}

func TestMalformedRequests(t *testing.T) {
	st := rawStream(t)
	cases := []struct {
		op   byte
		meta string
		code string
	}{
		{200, `{}`, "ENOSYS"},
		{opStat, `{not json`, "EINVAL"},
		{opStat, `{"p":123}`, "EINVAL"},
		{opRead, `{"h":999,"n":10}`, "EBADF"},
		{opClose, `{"h":5}`, "EBADF"},
		{opOpen, `{"p":"/","f":3}`, "EINVAL"},
		{opOpen, `{"p":"/","f":1024}`, "EINVAL"},
		{opRemove, `{"p":"/nonexistent-x","k":"weird"}`, "EINVAL"},
		{opTruncate, `{"p":"/nonexistent-x","s":-1}`, "EINVAL"},
	}
	for i, tc := range cases {
		id := uint32(i + 1)
		if _, err := st.Write(appendMsg(tc.op, id, []byte(tc.meta), nil)); err != nil {
			t.Fatal(err)
		}
		typ, gotID, rep := readReply(t, st)
		if typ != stErr || gotID != id || rep.Code != tc.code {
			t.Errorf("case %d: typ %d id %d code %q, want %q", i, typ, gotID, rep.Code, tc.code)
		}
	}
	// Oversized read request on a real handle.
	st.Write(appendMsg(opOpen, 100, []byte(`{"p":"/"}`), nil))
	_, _, rep := readReply(t, st)
	st.Write(appendMsg(opRead, 101, []byte(fmt.Sprintf(`{"h":%d,"n":%d}`, rep.Handle, MaxIO+1)), nil))
	if _, _, rep := readReply(t, st); rep.Code != "EINVAL" {
		t.Errorf("oversized read: %q", rep.Code)
	}
	// Oversized write payload.
	st.Write(appendMsg(opWrite, 102, []byte(`{"h":1}`), make([]byte, MaxIO+1)))
	if _, _, rep := readReply(t, st); rep.Code != "E2BIG" {
		t.Errorf("oversized write: %q", rep.Code)
	}
	// A frame too short to hold an id kills the stream, without
	// crashing the host.
	wire.WriteFrame(st, opStat, []byte{1, 2})
	if _, _, err := wire.ReadFrame(st, nil); err == nil {
		t.Fatal("stream survived a malformed frame")
	}
	// A meta length beyond the frame is rejected the same way.
	st2 := rawStream(t)
	bad := make([]byte, 8)
	binary.BigEndian.PutUint32(bad[4:], 1000)
	wire.WriteFrame(st2, opStat, bad)
	if _, _, err := wire.ReadFrame(st2, nil); err == nil {
		t.Fatal("stream survived a bad meta length")
	}
}

func TestBadReplyFromHost(t *testing.T) {
	// A host that answers garbage must not crash or hang the client.
	reg := peer.NewRegistry()
	reg.Handle(ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		typ, payload, err := wire.ReadFrame(s, nil)
		if err != nil {
			return err
		}
		id, _, _, _ := parseMsg(payload)
		_ = typ
		// An oversized read reply, then garbage JSON.
		s.Write(appendMsg(stOK, id, nil, make([]byte, 100)))
		s.Write(appendMsg(stOK, id+1, []byte("{garbage"), nil))
		io.Copy(io.Discard, s)
		return nil
	})
	conn, _ := peertest.PairWith(t, reg)
	ctx := tctx(t)
	c, err := NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	f := &File{c: c, h: 1, name: "x"}
	if _, err := f.ReadAt(ctx, make([]byte, 10), 0); err == nil {
		t.Fatal("oversized reply accepted")
	}
	if _, err := c.Stat(ctx, "x"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("want EIO after garbage reply, got %v", err)
	}
}

func TestReadDirDropsHostileNames(t *testing.T) {
	reg := peer.NewRegistry()
	reg.Handle(ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		for {
			_, payload, err := wire.ReadFrame(s, nil)
			if err != nil {
				return nil
			}
			id, _, _, _ := parseMsg(payload)
			rep := reply{EOF: true, Entries: []Attr{
				{Name: "ok", Mode: sIFREG}, {Name: "../evil", Mode: sIFREG}, {Name: "/etc", Mode: sIFDIR},
				{Name: "..", Mode: sIFDIR}, {Name: "", Mode: sIFREG}, {Name: "a\\b", Mode: sIFREG},
			}}
			meta, _ := json.Marshal(rep)
			s.Write(appendMsg(stOK, id, meta, nil))
		}
	})
	conn, _ := peertest.PairWith(t, reg)
	ctx := tctx(t)
	c, err := NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	ents, err := c.ReadDir(ctx, "/x")
	if err != nil || len(ents) != 1 || ents[0].Name() != "ok" {
		t.Fatalf("got %v %v", ents, err)
	}
}

func TestModeConversion(t *testing.T) {
	for _, m := range []fs.FileMode{
		0o644, 0o755 | fs.ModeDir, 0o777 | fs.ModeSymlink, fs.ModeNamedPipe | 0o600,
		fs.ModeSocket | 0o700, fs.ModeDevice | fs.ModeCharDevice | 0o620, fs.ModeDevice | 0o660,
		0o755 | fs.ModeSetuid, fs.ModeSetgid | fs.ModeSticky | fs.ModeDir | 0o777,
	} {
		if got := ModeFromUnix(ModeToUnix(m)); got != m {
			t.Errorf("%v -> %o -> %v", m, ModeToUnix(m), got)
		}
	}
}

// TestThroughput measures a large up- and download over loopback QUIC. Set
// REMOHOP_FS_BENCH_MB to choose the size (default 64 MiB, kept small so the test
// suite stays fast).
func TestThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	mb := 64
	if v := os.Getenv("REMOHOP_FS_BENCH_MB"); v != "" {
		fmt.Sscan(v, &mb)
	}
	c, dir := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	src := filepath.Join(dir, "src")
	f, _ := os.Create(src)
	chunk := make([]byte, 1<<20)
	rand.Read(chunk)
	h := sha256.New()
	for i := 0; i < mb; i++ {
		chunk[0] = byte(i)
		f.Write(chunk)
		h.Write(chunk)
	}
	f.Close()
	want := h.Sum(nil)

	start := time.Now()
	rd, err := c.Download(ctx, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.Reset()
	n, err := io.Copy(h, rd)
	rd.Close()
	if err != nil || n != int64(mb)<<20 || !bytes.Equal(h.Sum(nil), want) {
		t.Fatalf("download: %d %v", n, err)
	}
	down := time.Since(start)

	in, _ := os.Open(src)
	defer in.Close()
	start = time.Now()
	if _, err := c.Upload(ctx, filepath.Join(dir, "dst"), in, PutOptions{}); err != nil {
		t.Fatal(err)
	}
	up := time.Since(start)
	b, _ := os.ReadFile(filepath.Join(dir, "dst"))
	if sum := sha256.Sum256(b); !bytes.Equal(sum[:], want) {
		t.Fatal("upload hash mismatch")
	}
	t.Logf("%d MiB: download %.0f MB/s, upload %.0f MB/s", mb,
		float64(mb<<20)/down.Seconds()/1e6, float64(mb<<20)/up.Seconds()/1e6)
}

// hostileRegistry answers every control request with reply(op) and streams
// fs.get downloads from get.
func hostileRegistry(reply func(op byte) (reply, []byte), get func(s peer.Stream)) *peer.Registry {
	reg := peer.NewRegistry()
	reg.Handle(ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		for {
			typ, payload, err := wire.ReadFrame(s, nil)
			if err != nil {
				return nil
			}
			id, _, _, _ := parseMsg(payload)
			rep, data := reply(typ)
			meta, _ := json.Marshal(rep)
			st := stOK
			if rep.Code != "" {
				st = stErr
			}
			if _, err := s.Write(appendMsg(st, id, meta, data)); err != nil {
				return nil
			}
		}
	})
	if get != nil {
		reg.Handle(ServiceGet, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
			get(s)
			return nil
		})
	}
	return reg
}

func TestReadFileIsBounded(t *testing.T) {
	// A host that calls every file "too big" and then streams forever.
	reg := hostileRegistry(func(op byte) (reply, []byte) {
		return reply{TooBig: true, Attr: &Attr{Mode: sIFREG, Size: 5}}, nil
	}, func(s peer.Stream) {
		hdr, _ := json.Marshal(Attr{Mode: sIFREG, Size: 5})
		wire.WriteFrame(s, bulkHeader, hdr)
		chunk := make([]byte, bulkChunk)
		for wire.WriteFrame(s, bulkData, chunk) == nil {
		}
	})
	conn, _ := peertest.PairWith(t, reg)
	ctx := tctx(t)
	c, err := NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadFileLimit(ctx, "x", 3<<20)
	wantErrno(t, err, syscall.EFBIG)
	if _, ok, err := c.ReadSmall(ctx, "x"); ok || err != nil {
		t.Fatalf("ReadSmall of a big file: ok=%v err=%v", ok, err)
	}

	// A one-shot reply carrying more than SmallFile bytes is refused.
	reg = hostileRegistry(func(op byte) (reply, []byte) {
		return reply{Attr: &Attr{Mode: sIFREG}}, make([]byte, SmallFile+1)
	}, nil)
	conn, _ = peertest.PairWith(t, reg)
	c, _ = NewClient(ctx, conn)
	if _, _, err := c.ReadSmall(ctx, "x"); err == nil {
		t.Fatal("oversized one-shot read accepted")
	}
}

func TestReadDirIsBounded(t *testing.T) {
	defer func(old int) { maxDirEntries = old }(maxDirEntries)
	maxDirEntries = 16 * dirBatch
	ents := make([]Attr, dirBatch)
	for i := range ents {
		ents[i] = Attr{Name: fmt.Sprintf("same%d", i), Mode: sIFREG}
	}
	// An endless listing, and one that stalls with empty batches.
	for _, batch := range [][]Attr{ents, {}} {
		var calls atomic.Int64
		reg := hostileRegistry(func(op byte) (reply, []byte) {
			calls.Add(1)
			return reply{Entries: batch, Handle: 7}, nil
		}, nil)
		conn, _ := peertest.PairWith(t, reg)
		ctx := tctx(t)
		c, err := NewClient(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.ReadDir(ctx, "/d")
		wantErrno(t, err, syscall.E2BIG)
		if n := calls.Load(); n > int64(maxDirEntries/dirBatch+3) {
			t.Fatalf("%d requests before giving up", n)
		}
	}
}

func TestNewFileModeHonoursUmask(t *testing.T) {
	c, dir := newTestClient(t)
	ctx := tctx(t)
	p := filepath.Join(dir, "new")
	if err := c.WriteFile(ctx, p, []byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Mode() != 0o666&^Umask() {
		t.Fatalf("new file mode %v with umask %v", fi.Mode(), Umask())
	}
	if info, _ := c.Info(ctx); fs.FileMode(info.Umask) != Umask() {
		t.Fatalf("info umask %o", info.Umask)
	}
}

func TestUniqueName(t *testing.T) {
	for _, tc := range [][3]string{
		{"a.txt", "1", "a (1).txt"}, {"a", "2", "a (2)"}, {".bashrc", "1", ".bashrc (1)"},
		{"x.tar.gz", "3", "x (3).tar.gz"}, {".tar.gz", "1", ".tar (1).gz"}, {"a.b.c", "1", "a.b (1).c"},
	} {
		var i int
		fmt.Sscan(tc[1], &i)
		if got := UniqueName(tc[0], i); got != tc[2] {
			t.Errorf("UniqueName(%q, %d) = %q, want %q", tc[0], i, got, tc[2])
		}
	}
	if UniqueName("a.txt", 0) != "a.txt" {
		t.Fatal("0")
	}
}
