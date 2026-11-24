//go:build linux

package mount

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

type fixture struct {
	t      *testing.T
	conn   *peer.Conn
	mt     *Mount
	local  string // mountpoint
	remote string // backing directory ("remote" side)
}

func (f *fixture) l(p string) string { return filepath.Join(f.local, p) }
func (f *fixture) r(p string) string { return filepath.Join(f.remote, p) }

func canMount(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	if os.Geteuid() != 0 && !haveFusermount() {
		t.Skip("not root and no fusermount helper")
	}
}

func newFixture(t *testing.T, o Options) *fixture {
	t.Helper()
	canMount(t)
	conn, _ := peertest.Pair(t)
	f := &fixture{t: t, conn: conn, local: t.TempDir(), remote: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if o.ReconnectFor == 0 {
		o.ReconnectFor = -1
	}
	mt, err := New(ctx, conn, f.remote, f.local, o)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "permission") {
			t.Skipf("cannot mount FUSE here: %v", err)
		}
		t.Fatal(err)
	}
	f.mt = mt
	// Always unmount, even when the test fails half-way.
	t.Cleanup(func() {
		mt.Unmount()
		if isMounted(f.local) {
			unmountDir(f.local, true)
		}
	})
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestMountBasicOps(t *testing.T) {
	f := newFixture(t, Options{})
	if !isMounted(f.local) {
		t.Fatal("not a mountpoint")
	}
	// Pre-existing remote content is visible.
	must(t, os.WriteFile(f.r("pre.txt"), []byte("remote data"), 0o640))
	b, err := os.ReadFile(f.l("pre.txt"))
	if err != nil || string(b) != "remote data" {
		t.Fatalf("read: %q %v", b, err)
	}
	fi, err := os.Stat(f.l("pre.txt"))
	must(t, err)
	rfi, _ := os.Stat(f.r("pre.txt"))
	st := fi.Sys().(*syscall.Stat_t)
	rst := rfi.Sys().(*syscall.Stat_t)
	if fi.Mode() != 0o640 || fi.Size() != 11 || !fi.ModTime().Equal(rfi.ModTime()) ||
		st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Ino != rst.Ino {
		t.Fatalf("attrs: mode %v size %d mtime %v/%v nlink %d uid %d ino %d/%d",
			fi.Mode(), fi.Size(), fi.ModTime(), rfi.ModTime(), st.Nlink, st.Uid, st.Ino, rst.Ino)
	}

	// Create, write, read back on both sides.
	must(t, os.WriteFile(f.l("new.txt"), []byte("hello mount"), 0o600))
	if b, _ := os.ReadFile(f.r("new.txt")); string(b) != "hello mount" {
		t.Fatalf("remote sees %q", b)
	}
	if fi, _ := os.Stat(f.r("new.txt")); fi.Mode() != 0o600 {
		t.Fatalf("created mode %v", fi.Mode())
	}
	// O_EXCL.
	if _, err := os.OpenFile(f.l("new.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL: %v", err)
	}
	// O_APPEND.
	af, err := os.OpenFile(f.l("new.txt"), os.O_WRONLY|os.O_APPEND, 0)
	must(t, err)
	af.Write([]byte(" +1"))
	af.Write([]byte(" +2"))
	must(t, af.Close())
	if b, _ := os.ReadFile(f.l("new.txt")); string(b) != "hello mount +1 +2" {
		t.Fatalf("append: %q", b)
	}
	// Overwrite in the middle, truncate.
	wf, err := os.OpenFile(f.l("new.txt"), os.O_RDWR, 0)
	must(t, err)
	wf.WriteAt([]byte("HELLO"), 0)
	must(t, wf.Truncate(11))
	must(t, wf.Close())
	if b, _ := os.ReadFile(f.r("new.txt")); string(b) != "HELLO mount" {
		t.Fatalf("pwrite/truncate: %q", b)
	}
	must(t, os.Truncate(f.l("new.txt"), 3))
	if fi, _ := os.Stat(f.l("new.txt")); fi.Size() != 3 {
		t.Fatalf("truncate size %d", fi.Size())
	}

	// Directories.
	must(t, os.MkdirAll(f.l("a/b/c"), 0o750))
	if fi, err := os.Stat(f.r("a/b/c")); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750 {
		t.Fatalf("mkdir: %v %v", fi, err)
	}
	if err := os.Remove(f.l("a")); !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("rmdir non-empty: %v", err)
	}
	must(t, os.Remove(f.l("a/b/c")))
	if _, err := os.Stat(f.r("a/b/c")); !os.IsNotExist(err) {
		t.Fatal("rmdir not applied")
	}
	if _, err := os.Stat(f.l("nope")); !os.IsNotExist(err) {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.Mkdir(f.l("pre.txt"), 0o755); !os.IsExist(err) {
		t.Fatalf("mkdir over file: %v", err)
	}

	// Rename (also across directories, and over an existing file).
	must(t, os.Rename(f.l("new.txt"), f.l("a/b/moved.txt")))
	if _, err := os.Stat(f.r("a/b/moved.txt")); err != nil {
		t.Fatal(err)
	}
	must(t, os.Rename(f.l("a/b/moved.txt"), f.l("pre.txt")))
	if b, _ := os.ReadFile(f.l("pre.txt")); string(b) != "HEL" {
		t.Fatalf("rename over: %q", b)
	}

	// Symlinks and hard links.
	must(t, os.Symlink("pre.txt", f.l("link")))
	if tgt, err := os.Readlink(f.l("link")); err != nil || tgt != "pre.txt" {
		t.Fatalf("readlink %q %v", tgt, err)
	}
	if tgt, _ := os.Readlink(f.r("link")); tgt != "pre.txt" {
		t.Fatal("symlink not on remote")
	}
	if b, _ := os.ReadFile(f.l("link")); string(b) != "HEL" {
		t.Fatal("read through symlink")
	}
	must(t, os.Link(f.l("pre.txt"), f.l("hard")))
	if fi, _ := os.Stat(f.l("hard")); fi.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatal("hard link nlink")
	}

	// chmod and utimens.
	must(t, os.Chmod(f.l("pre.txt"), 0o755))
	if fi, _ := os.Stat(f.r("pre.txt")); fi.Mode() != 0o755 {
		t.Fatalf("chmod: %v", fi.Mode())
	}
	mt := time.Date(2020, 1, 2, 3, 4, 5, 6000, time.UTC)
	must(t, os.Chtimes(f.l("pre.txt"), mt, mt))
	if fi, _ := os.Stat(f.r("pre.txt")); !fi.ModTime().Equal(mt) {
		t.Fatalf("utimens: %v", fi.ModTime())
	}
	if fi, _ := os.Stat(f.l("pre.txt")); !fi.ModTime().Equal(mt) {
		t.Fatalf("utimens seen locally: %v", fi.ModTime())
	}
	if err := os.Chown(f.l("pre.txt"), os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("chown to self: %v", err)
	}

	// Unlink, statfs.
	must(t, os.Remove(f.l("hard")))
	if _, err := os.Lstat(f.r("hard")); !os.IsNotExist(err) {
		t.Fatal("unlink not applied")
	}
	var sfs syscall.Statfs_t
	must(t, syscall.Statfs(f.local, &sfs))
	if sfs.Blocks == 0 || sfs.Bsize == 0 {
		t.Fatalf("statfs %+v", sfs)
	}

	// Readdir with many entries.
	for i := 0; i < 1500; i++ {
		os.WriteFile(f.r(fmt.Sprintf("many/f%04d", i)), nil, 0o644)
	}
	os.Mkdir(f.r("many"), 0o755)
	for i := 0; i < 1500; i++ {
		os.WriteFile(f.r(fmt.Sprintf("many/f%04d", i)), []byte{byte(i)}, 0o644)
	}
	ents, err := os.ReadDir(f.l("many"))
	if err != nil || len(ents) != 1500 {
		t.Fatalf("readdir: %d %v", len(ents), err)
	}
	if info, _ := ents[7].Info(); info.Size() != 1 {
		t.Fatal("readdir attrs")
	}
}

func TestMountUnlinkedOpenFile(t *testing.T) {
	f := newFixture(t, Options{})
	fh, err := os.Create(f.l("tmp"))
	must(t, err)
	defer fh.Close()
	fh.Write([]byte("still here"))
	must(t, os.Remove(f.l("tmp")))
	buf := make([]byte, 10)
	if n, err := fh.ReadAt(buf, 0); n != 10 || string(buf) != "still here" {
		t.Fatalf("read after unlink: %d %v", n, err)
	}
	if fi, err := fh.Stat(); err != nil || fi.Size() != 10 {
		t.Fatalf("fstat after unlink: %v %v", fi, err)
	}
}

func TestMountRemoteChangesVisible(t *testing.T) {
	f := newFixture(t, Options{})
	must(t, os.WriteFile(f.r("x"), []byte("v1"), 0o644))
	if b, _ := os.ReadFile(f.l("x")); string(b) != "v1" {
		t.Fatal("v1")
	}
	if _, err := os.Stat(f.l("later")); !os.IsNotExist(err) {
		t.Fatal("later exists")
	}
	must(t, os.WriteFile(f.r("x"), []byte("version 2"), 0o644))
	must(t, os.WriteFile(f.r("later"), []byte("l"), 0o644))
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, _ := os.ReadFile(f.l("x"))
		_, err := os.Stat(f.l("later"))
		if string(b) == "version 2" && err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote change not visible after 3s: %q %v", b, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fileHash(t *testing.T, p string) [32]byte {
	t.Helper()
	fh, err := os.Open(p)
	must(t, err)
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		t.Fatal(err)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func TestMountLargeFiles(t *testing.T) {
	f := newFixture(t, Options{})
	const size = 96 << 20
	data := make([]byte, size)
	rand.Read(data)
	want := sha256.Sum256(data)

	start := time.Now()
	must(t, os.WriteFile(f.l("big"), data, 0o644))
	wt := time.Since(start)
	if fileHash(t, f.r("big")) != want {
		t.Fatal("written file differs on the remote side")
	}
	must(t, os.WriteFile(f.r("big2"), data, 0o644))
	start = time.Now()
	if fileHash(t, f.l("big2")) != want {
		t.Fatal("read file differs")
	}
	rt := time.Since(start)
	t.Logf("through FUSE: write %.0f MB/s, read %.0f MB/s", size/wt.Seconds()/1e6, size/rt.Seconds()/1e6)

	// Offsets beyond 4 GiB.
	fh, err := os.OpenFile(f.l("sparse"), os.O_CREATE|os.O_RDWR, 0o644)
	must(t, err)
	off := int64(5<<30) + 12345
	if _, err := fh.WriteAt([]byte("far away"), off); err != nil {
		t.Fatal(err)
	}
	must(t, fh.Close())
	fi, _ := os.Stat(f.r("sparse"))
	if fi.Size() != off+8 {
		t.Fatalf("sparse size %d", fi.Size())
	}
	fh, _ = os.Open(f.l("sparse"))
	buf := make([]byte, 8)
	if _, err := fh.ReadAt(buf, off); err != nil || string(buf) != "far away" {
		t.Fatalf("read far: %q %v", buf, err)
	}
	fh.Close()
}

func TestMountConcurrent(t *testing.T) {
	f := newFixture(t, Options{})
	const n = 16
	payload := func(i int) []byte { return bytes.Repeat([]byte(fmt.Sprintf("%02d", i)), 100000+i*999) }
	var wg sync.WaitGroup
	errs := make(chan error, n*4)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := os.WriteFile(f.l(fmt.Sprintf("c%d", i)), payload(i), 0o644); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	// Many readers of every file at once, plus a writer appending to a
	// shared log.
	lf, err := os.OpenFile(f.l("log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	must(t, err)
	for i := 0; i < n; i++ {
		for r := 0; r < 3; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b, err := os.ReadFile(f.l(fmt.Sprintf("c%d", i)))
				if err != nil || !bytes.Equal(b, payload(i)) {
					errs <- fmt.Errorf("c%d: %d bytes %v", i, len(b), err)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := lf.Write([]byte(fmt.Sprintf("line %02d\n", i))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	must(t, lf.Close())
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	b, _ := os.ReadFile(f.r("log"))
	if strings.Count(string(b), "\n") != n || len(b) != n*8 {
		t.Fatalf("log: %q", b)
	}
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	// Change directory after exec, in the child: with cmd.Dir the forked
	// child would chdir into the mount before exec, while still holding
	// this process's fork lock, and this same process serves the mount.
	cmd := exec.Command("sh", append([]string{"-c", `cd "$0" && exec "$@"`, dir, name}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@e", "HOME="+dir, "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func TestMountWithTools(t *testing.T) {
	f := newFixture(t, Options{})
	for i := 0; i < 20; i++ {
		os.MkdirAll(f.r(fmt.Sprintf("src/pkg%d", i%4)), 0o755)
		os.WriteFile(f.r(fmt.Sprintf("src/pkg%d/file%d.go", i%4, i)), []byte(fmt.Sprintf("package p\n// needle %d\n", i)), 0o644)
	}
	out := run(t, f.local, "ls", "-l", "src/pkg0")
	if !strings.Contains(out, "file0.go") || !strings.Contains(out, "-rw-r--r--") {
		t.Fatalf("ls -l:\n%s", out)
	}
	out = run(t, f.local, "grep", "-r", "needle 13", ".")
	if !strings.Contains(out, "src/pkg1/file13.go") {
		t.Fatalf("grep -r:\n%s", out)
	}
	run(t, f.local, "cp", "-r", "src", "copy")
	if b, _ := os.ReadFile(f.r("copy/pkg2/file6.go")); !strings.Contains(string(b), "needle 6") {
		t.Fatal("cp -r through the mount")
	}
	run(t, f.local, "cp", "-p", "src/pkg0/file0.go", "kept.go")

	if _, err := exec.LookPath("git"); err != nil {
		t.Log("git not installed; skipping git checks")
		return
	}
	run(t, f.local, "git", "init", "-q")
	run(t, f.local, "git", "add", ".")
	run(t, f.local, "git", "commit", "-q", "-m", "init")
	out = run(t, f.local, "git", "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("git status after commit:\n%s", out)
	}
	os.WriteFile(f.l("src/pkg0/file0.go"), []byte("changed\n"), 0o644)
	os.WriteFile(f.r("remote-new.txt"), []byte("x"), 0o644)
	time.Sleep(1100 * time.Millisecond) // let the 1s caches expire
	out = run(t, f.local, "git", "status", "--porcelain")
	if !strings.Contains(out, " M src/pkg0/file0.go") || !strings.Contains(out, "?? remote-new.txt") {
		t.Fatalf("git status:\n%s", out)
	}
	// The repository is valid on the remote side too.
	out = run(t, f.remote, "git", "status", "--porcelain")
	if !strings.Contains(out, " M src/pkg0/file0.go") {
		t.Fatalf("remote git status:\n%s", out)
	}
}

func TestMountReadOnly(t *testing.T) {
	f := newFixture(t, Options{ReadOnly: true})
	os.WriteFile(f.r("r"), []byte("ro"), 0o644)
	if b, err := os.ReadFile(f.l("r")); err != nil || string(b) != "ro" {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.l("w"), []byte("x"), 0o644); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("write on ro mount: %v", err)
	}
	if err := os.Remove(f.l("r")); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("unlink on ro mount: %v", err)
	}
}

func TestMountSessionLossUnmounts(t *testing.T) {
	f := newFixture(t, Options{ReconnectFor: -1})
	os.WriteFile(f.r("x"), []byte("x"), 0o644)
	if _, err := os.ReadFile(f.l("x")); err != nil {
		t.Fatal(err)
	}
	f.conn.Close()
	select {
	case <-f.mt.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("mount still alive after the session ended")
	}
	if f.mt.Err() == nil {
		t.Fatal("no error reported for the lost session")
	}
	if isMounted(f.local) {
		t.Fatal("dead mountpoint left behind")
	}
}

func TestMountReconnects(t *testing.T) {
	canMount(t)
	redials := make(chan struct{}, 10)
	spare, _ := peertest.Pair(t) // the session the mount reconnects with
	first := true
	o := Options{
		ReconnectFor: 20 * time.Second,
		Redial: func(ctx context.Context) (*peer.Conn, error) {
			redials <- struct{}{}
			if first {
				// Fail once to exercise the backoff.
				first = false
				return nil, errors.New("host unreachable")
			}
			return spare, nil
		},
	}
	f := newFixture(t, o)
	os.WriteFile(f.r("x"), []byte("before"), 0o644)
	if b, _ := os.ReadFile(f.l("x")); string(b) != "before" {
		t.Fatal("before")
	}
	f.conn.Close()
	select {
	case <-redials:
	case <-time.After(10 * time.Second):
		t.Fatal("no redial")
	}
	os.WriteFile(f.r("y"), []byte("after"), 0o644)
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(f.l("y"))
		if err == nil && string(b) == "after" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mount did not recover: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestMountCommandForeground(t *testing.T) {
	canMount(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	conn, _ := peertest.Pair(t)
	local, remote := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(remote, "hello"), []byte("hi"), 0o644)
	var out, errb lockedBuf
	env := &cli.Env{
		Target: "test", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return conn, nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		if isMounted(local) {
			unmountDir(local, true)
		}
	})
	code := make(chan int, 1)
	go func() {
		code <- cli.Lookup("mount").Run(ctx, env, []string{"-f", "-o", "reconnect=0", ":" + remote, local})
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !isMounted(local) {
		if time.Now().After(deadline) {
			t.Fatalf("not mounted; stderr: %s", errb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(local, "hello")); err != nil || string(b) != "hi" {
		t.Fatalf("read through the command's mount: %q %v", b, err)
	}
	infos, _ := bg.List()
	if len(infos) != 1 || infos[0].Kind != "mount" || !strings.HasPrefix(infos[0].Detail, local+" <- :") {
		t.Fatalf("bg records: %+v", infos)
	}
	if !strings.Contains(out.String(), "mounted test:") {
		t.Fatalf("stdout: %q", out.String())
	}
	// SIGTERM / Ctrl-C cancel the context: the command unmounts.
	cancel()
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit %d: %s", c, errb.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("mount command did not exit")
	}
	if isMounted(local) {
		t.Fatal("still mounted after the command exited")
	}
	if infos, _ := bg.List(); len(infos) != 0 {
		t.Fatalf("bg record left: %+v", infos)
	}
}

func TestMountCommandUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {":/x"}, {"/x", "/y"}, {":/x", ":/y"}, {"-o", "bogus", ":/x", t.TempDir()},
		{"-o", "reconnect=xyz", ":/x", t.TempDir()},
	} {
		var b bytes.Buffer
		env := &cli.Env{Target: "test", Stdout: &b, Stderr: &b}
		if code := cli.Lookup("mount").Run(context.Background(), env, args); code != cli.ExitUsage {
			t.Errorf("%v: exit %d (%s)", args, code, b.String())
		}
	}
	var b bytes.Buffer
	env := &cli.Env{Target: "test", Stdout: &b, Stderr: &b}
	if code := cli.Lookup("mount").Run(context.Background(), env, []string{":/x", "/nonexistent-dir"}); code != cli.ExitUsage {
		t.Errorf("missing mountpoint: exit %d", code)
	}
}

func TestUmountCommand(t *testing.T) {
	canMount(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	f := newFixture(t, Options{})
	var b bytes.Buffer
	env := &cli.Env{Stdout: &b, Stderr: &b}
	// No background record: umount falls back to unmounting directly.
	if code := cli.Lookup("umount").Run(context.Background(), env, []string{f.local}); code != 0 {
		t.Fatalf("umount: %d %s", code, b.String())
	}
	select {
	case <-f.mt.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("server did not notice the unmount")
	}
	if isMounted(f.local) {
		t.Fatal("still mounted")
	}
	b.Reset()
	if code := cli.Lookup("umount").Run(context.Background(), env, []string{f.local}); code != cli.ExitError ||
		!strings.Contains(b.String(), "not mounted") {
		t.Fatalf("umount of a plain dir: %d %s", code, b.String())
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Errors of buffered writes must reach the application at close().
func TestMountWriteErrorOnClose(t *testing.T) {
	canMount(t)
	small := t.TempDir()
	if err := syscall.Mount("tmpfs", small, "tmpfs", 0, "size=256k"); err != nil {
		t.Skipf("cannot mount a small tmpfs to fill up: %v", err)
	}
	t.Cleanup(func() { syscall.Unmount(small, syscall.MNT_DETACH) })
	conn, _ := peertest.Pair(t)
	local := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mt, err := New(ctx, conn, small, local, Options{ReconnectFor: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mt.Unmount() })
	fh, err := os.Create(filepath.Join(local, "fill"))
	must(t, err)
	var werr error
	for i := 0; i < 8 && werr == nil; i++ {
		_, werr = fh.Write(make([]byte, 128<<10))
	}
	cerr := fh.Close()
	if !errors.Is(werr, syscall.ENOSPC) && !errors.Is(cerr, syscall.ENOSPC) {
		t.Fatalf("ENOSPC lost: write %v, close %v", werr, cerr)
	}
}

// withTimeout runs fn and fails the test if it does not return in time
// (the goroutine is abandoned; the mount cleanup unblocks it).
func withTimeout(t *testing.T, d time.Duration, what string, fn func() error) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- fn() }()
	select {
	case err := <-res:
		return err
	case <-time.After(d):
		t.Fatalf("%s did not return within %s", what, d)
		return nil
	}
}

// A hostile host gives a directory the identity of its ancestor. Before
// directory inode numbers were derived from paths, go-fuse linked the
// ancestor under its own descendant and the next path computation looped
// forever, eating memory.
func TestMountDirectoryAliasingHost(t *testing.T) {
	canMount(t)
	const dirMode = 0o040755
	dirs := map[string]bool{"/r": true, "/r/A": true, "/r/A/B": true, "/r/A/B/D": true}
	reg := peer.NewRegistry()
	reg.Handle(rfs.ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return rfs.ServeFake(s, func(op, p string) (*rfs.Attr, []rfs.Attr, []byte, string) {
			switch op {
			case "realpath":
				return nil, nil, []byte(p), ""
			case "stat", "lstat":
				if dirs[p] {
					return &rfs.Attr{Name: filepath.Base(p), Mode: dirMode, Ino: 5, Dev: 1, Nlink: 2}, nil, nil, ""
				}
				if p == "/r/A/C" {
					return &rfs.Attr{Name: "C", Mode: 0o100644, Ino: 5, Dev: 1, Nlink: 1}, nil, nil, ""
				}
				return nil, nil, nil, "ENOENT"
			case "readdir":
				var ents []rfs.Attr
				for d := range dirs {
					if filepath.Dir(d) == p {
						ents = append(ents, rfs.Attr{Name: filepath.Base(d), Mode: dirMode, Ino: 5, Dev: 1})
					}
				}
				return nil, ents, nil, ""
			}
			return nil, nil, nil, "ENOSYS"
		})
	})
	conn, _ := peertest.PairWith(t, reg)
	local := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mt, err := New(ctx, conn, "/r", local, Options{ReconnectFor: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mt.Unmount() })
	for _, p := range []string{"A/B", "A/C", "A/B/D", "A", "A/B/D"} {
		err := withTimeout(t, 5*time.Second, "stat "+p, func() error { _, err := os.Stat(filepath.Join(local, p)); return err })
		if err != nil {
			t.Errorf("stat %s: %v", p, err)
		}
	}
	err = withTimeout(t, 5*time.Second, "walk", func() error {
		return filepath.WalkDir(local, func(string, os.DirEntry, error) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The honest version: an ancestor bind-mounted inside the exported tree
// reports the same (dev, ino) as the ancestor itself.
func TestMountBindMountedAncestor(t *testing.T) {
	f := newFixture(t, Options{})
	must(t, os.MkdirAll(f.r("sub/loop"), 0o755))
	os.WriteFile(f.r("file"), []byte("x"), 0o644)
	if err := syscall.Mount(f.remote, f.r("sub/loop"), "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("cannot bind mount: %v", err)
	}
	t.Cleanup(func() { syscall.Unmount(f.r("sub/loop"), syscall.MNT_DETACH) })
	// The bind is not recursive: sub/loop shows the root again (same
	// dev and inode as the mount root), and sub/loop/sub/loop is the
	// plain directory underneath.
	deep := "sub/loop/file"
	err := withTimeout(t, 5*time.Second, "read through the loop", func() error {
		b, err := os.ReadFile(f.l(deep))
		if err == nil && string(b) != "x" {
			err = fmt.Errorf("content %q", b)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"sub", "sub/loop/sub", "sub/loop/sub/loop", "file", "sub/loop"} {
		if err := withTimeout(t, 5*time.Second, "stat", func() error { _, err := os.Stat(f.l(p)); return err }); err != nil {
			t.Errorf("stat %s: %v", p, err)
		}
	}
	err = withTimeout(t, 5*time.Second, "walk", func() error {
		return filepath.WalkDir(f.local, func(string, os.DirEntry, error) error { return nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	// Hard-linked files keep the host's identity (git relies on it).
	must(t, os.Link(f.r("file"), f.r("file2")))
	a, _ := os.Stat(f.l("file"))
	b, _ := os.Stat(f.l("file2"))
	if !os.SameFile(a, b) {
		t.Error("hard links lost their shared identity")
	}
}

// startForeground runs `remohop <ID> mount -f` against conn and waits until it
// is mounted.
func startForeground(t *testing.T, env *cli.Env, remote, local string, extra ...string) (cancel func(), code <-chan int) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	ch := make(chan int, 1)
	args := append(append([]string{"-f"}, extra...), ":"+remote, local)
	go func() { ch <- cli.Lookup("mount").Run(ctx, env, args) }()
	t.Cleanup(func() {
		cancelFn()
		if isMounted(local) {
			unmountDir(local, true)
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for !isMounted(local) {
		if time.Now().After(deadline) {
			t.Fatal("not mounted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cancelFn, ch
}

func waitExit(t *testing.T, code <-chan int, within time.Duration) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(within):
		t.Fatalf("mount command still running %s after SIGTERM", within)
		return -1
	}
}

// The host stops answering while a request is in flight: SIGTERM must
// still unmount and exit promptly (it used to wait for the stuck request).
func TestMountExitsWhenHostHangs(t *testing.T) {
	canMount(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	remote := t.TempDir()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	reg := peer.NewRegistry()
	reg.Handle(rfs.ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return rfs.ServeFake(s, func(op, p string) (*rfs.Attr, []rfs.Attr, []byte, string) {
			switch {
			case op == "realpath":
				return nil, nil, []byte(p), ""
			case p == remote:
				return &rfs.Attr{Mode: 0o040755, Ino: 2}, nil, nil, ""
			case strings.HasSuffix(p, "/hang"):
				<-hang // the host is wedged
			}
			return nil, nil, nil, "ENOENT"
		})
	})
	conn, _ := peertest.PairWith(t, reg)
	local := t.TempDir()
	env := &cli.Env{Target: "test", Stdout: io.Discard, Stderr: io.Discard,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return conn, nil }}
	cancel, code := startForeground(t, env, remote, local)
	go os.Stat(filepath.Join(local, "hang"))
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	cancel()
	waitExit(t, code, 4500*time.Millisecond)
	t.Logf("exited %s after SIGTERM", time.Since(start).Round(time.Millisecond))
	if isMounted(local) {
		t.Fatal("still mounted")
	}
}

// The session is gone and the mount is retrying: SIGTERM must interrupt
// the reconnect loop, and redialled sessions are not leaked.
func TestMountExitsWhileReconnecting(t *testing.T) {
	canMount(t)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	conn, _ := peertest.Pair(t)
	remote, local := t.TempDir(), t.TempDir()
	var dials atomic.Int64
	env := &cli.Env{Target: "test", Stdout: io.Discard, Stderr: io.Discard,
		DialFunc: func(ctx context.Context, _ *cli.Env) (*peer.Conn, error) {
			if dials.Add(1) == 1 {
				return conn, nil
			}
			<-ctx.Done() // the host is unreachable
			return nil, ctx.Err()
		}}
	cancel, code := startForeground(t, env, remote, local)
	conn.Close() // the host dies
	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect attempt")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	waitExit(t, code, 4500*time.Millisecond)
	if isMounted(local) {
		t.Fatal("still mounted")
	}
}

// remohop umount must clean up even when the recorded process is alive, no
// longer mounted, and ignores SIGTERM.
func TestUmountKillsWedgedProcess(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", cfg)
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 0.1; done`)
	must(t, cmd.Start())
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() { cmd.Process.Kill() })
	time.Sleep(100 * time.Millisecond) // let the trap be installed
	run := filepath.Join(cfg, "run")
	os.MkdirAll(run, 0o700)
	rec, _ := json.Marshal(bg.Info{Kind: "mount", Target: "test", Detail: bgDetail(dir, "/remote"), PID: cmd.Process.Pid, Started: time.Now()})
	must(t, os.WriteFile(filepath.Join(run, fmt.Sprintf("%d.json", cmd.Process.Pid)), rec, 0o600))

	var b bytes.Buffer
	env := &cli.Env{Stdout: &b, Stderr: &b}
	if code := cli.Lookup("umount").Run(context.Background(), env, []string{dir}); code != 0 {
		t.Fatalf("umount: %d %s", code, b.String())
	}
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		t.Fatal("wedged process still alive")
	}
}
