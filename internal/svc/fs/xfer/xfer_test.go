package xfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// posix is false where files have no POSIX permission bits (Windows):
// tree comparisons and assertions leave modes out there.
const posix = runtime.GOOS != "windows"

var modeField = regexp.MustCompile(`^([df]) [0-7]+`)

// tm adapts a tree() literal such as "f 644 text" or "d 750" to the
// platform, dropping the mode where there is none.
func tm(s string) string {
	if posix {
		return s
	}
	return modeField.ReplaceAllString(s, "$1")
}

// setHome points the "host" (this process) at a home directory.
func setHome(t *testing.T, dir string) {
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // what os.UserHomeDir reads on Windows
}

type runner struct {
	t    *testing.T
	conn *peer.Conn
}

func newRunner(t *testing.T) *runner {
	conn, _ := peertest.Pair(t)
	return &runner{t: t, conn: conn}
}

func (r *runner) run(stdin string, args ...string) (code int, stdout, stderr string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	env := &cli.Env{
		Target: "test", Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return r.conn, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	code = cli.Lookup(args[0]).Run(ctx, env, args[1:])
	return code, out.String(), errb.String()
}

func (r *runner) must(args ...string) string {
	r.t.Helper()
	code, out, errs := r.run("", args...)
	if code != 0 {
		r.t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errs)
	}
	return out
}

func write(t *testing.T, p string, data string, mode fs.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, mode)
}

// tree describes a directory: rel path -> "d <mode>", "f <mode> <content>"
// or "l <target>". Times are not included.
// fent is the tree() description of a regular file: mode, size and a short
// content hash. Never the content itself: a failure message must stay
// small even when the file is megabytes of random data.
func fent(mode, content string) string {
	sum := sha256.Sum256([]byte(content))
	return tm(fmt.Sprintf("f %s %d:%x", mode, len(content), sum[:4]))
}

// brief renders a tree for a failure message, capped at a few KB.
func brief(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if b.Len() > 4000 {
			fmt.Fprintf(&b, "... (%d entries in all)", len(keys))
			break
		}
		fmt.Fprintf(&b, "%s=%q ", k, m[k])
	}
	return b.String()
}

func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel) // the same keys on every OS
		fi, _ := os.Lstat(p)
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			tg, _ := os.Readlink(p)
			m[rel] = "l " + tg
		case fi.IsDir():
			m[rel] = tm(fmt.Sprintf("d %o", fi.Mode().Perm()))
		default:
			b, _ := os.ReadFile(p)
			m[rel] = fent(fmt.Sprintf("%o", fi.Mode()&permBits), string(b))
		}
		return nil
	})
	return m
}

func sameTree(t *testing.T, a, b string) {
	t.Helper()
	ta, tb := tree(t, a), tree(t, b)
	var keys []string
	for k := range ta {
		keys = append(keys, k)
	}
	for k := range tb {
		if _, ok := ta[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ta[k] != tb[k] {
			t.Errorf("%s: %q vs %q", k, ta[k], tb[k])
		}
	}
}

func makeSample(t *testing.T, root string) {
	write(t, filepath.Join(root, "a.txt"), "alpha", 0o644)
	write(t, filepath.Join(root, "bin", "run.sh"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(root, "deep", "er", "est", "x"), "deep", 0o600)
	write(t, filepath.Join(root, "empty"), "", 0o644)
	os.MkdirAll(filepath.Join(root, "emptydir"), 0o750)
	os.Symlink("a.txt", filepath.Join(root, "link"))
	os.Symlink("/nonexistent/target", filepath.Join(root, "dangling"))
	big := make([]byte, 3*rfs.SmallFile+77)
	rand.Read(big)
	os.WriteFile(filepath.Join(root, "big.bin"), big, 0o640)
	os.Chmod(filepath.Join(root, "deep"), 0o700)
}

func TestCpUploadDownloadFile(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	write(t, src, "hello", 0o751)
	old := time.Unix(1500000000, 0)
	os.Chtimes(src, old, old)
	rdir := filepath.Join(dir, "remote")
	os.Mkdir(rdir, 0o755)

	// Into an existing remote directory, keeping the name and mode, not
	// the time (no -p).
	r.must("cp", src, ":"+rdir)
	fi, err := os.Stat(filepath.Join(rdir, "src.txt"))
	if err != nil || (posix && fi.Mode() != 0o751) || fi.ModTime().Equal(old) {
		t.Fatalf("upload: %v %v", fi, err)
	}
	// -p keeps the time; an explicit new name.
	r.must("cp", "-p", src, ":"+filepath.Join(rdir, "renamed"))
	if fi, _ := os.Stat(filepath.Join(rdir, "renamed")); !fi.ModTime().Equal(old) {
		t.Fatal("-p did not keep mtime")
	}
	// Download into "." style directory and to a file name; flags after
	// the paths work.
	ldir := filepath.Join(dir, "local")
	os.Mkdir(ldir, 0o755)
	r.must("cp", ":"+filepath.Join(rdir, "renamed"), ldir+"/", "-p")
	if b, _ := os.ReadFile(filepath.Join(ldir, "renamed")); string(b) != "hello" {
		t.Fatalf("download: %q", b)
	}
	if fi, _ := os.Stat(filepath.Join(ldir, "renamed")); (posix && fi.Mode() != 0o751) || !fi.ModTime().Equal(old) {
		t.Fatalf("download attrs %v %v", fi.Mode(), fi.ModTime())
	}
	// Overwrite an existing local file.
	write(t, filepath.Join(ldir, "over"), "old content that is longer", 0o644)
	r.must("cp", ":"+src, filepath.Join(ldir, "over"))
	if b, _ := os.ReadFile(filepath.Join(ldir, "over")); string(b) != "hello" {
		t.Fatalf("overwrite: %q", b)
	}
	// Multiple sources into a directory; JSON summary.
	write(t, filepath.Join(dir, "s2"), "two", 0o644)
	out := r.must("cp", "--json", src, filepath.Join(dir, "s2"), ":"+rdir)
	var res CopyResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Files != 2 || res.Bytes != 8 {
		t.Fatalf("json %q: %+v %v", out, res, err)
	}
	if !posix {
		return // no setuid bits to speak of
	}
	// Setuid bits survive an upload but never a download.
	write(t, filepath.Join(dir, "suid"), "s", 0o755)
	os.Chmod(filepath.Join(dir, "suid"), 0o755|fs.ModeSetuid)
	r.must("cp", filepath.Join(dir, "suid"), ":"+rdir)
	if fi, _ := os.Stat(filepath.Join(rdir, "suid")); fi.Mode() != 0o755|fs.ModeSetuid {
		t.Fatalf("upload suid: %v", fi.Mode())
	}
	r.must("cp", ":"+filepath.Join(rdir, "suid"), ldir)
	if fi, _ := os.Stat(filepath.Join(ldir, "suid")); fi.Mode() != 0o755 {
		t.Fatalf("download kept suid: %v", fi.Mode())
	}
}

func TestCpErrors(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "x", 0o644)
	os.Mkdir(filepath.Join(dir, "d"), 0o755)
	for _, tc := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"cp", filepath.Join(dir, "f")}, cli.ExitUsage, "need a source"},
		{[]string{"cp", filepath.Join(dir, "f"), filepath.Join(dir, "g")}, cli.ExitUsage, "remote"},
		{[]string{"cp", ":" + filepath.Join(dir, "f"), ":/tmp"}, cli.ExitUsage, "remote"},
		{[]string{"cp", filepath.Join(dir, "f"), filepath.Join(dir, "d"), ":" + filepath.Join(dir, "f")}, cli.ExitError, "not a directory"},
		{[]string{"cp", filepath.Join(dir, "d"), ":" + filepath.Join(dir, "x")}, cli.ExitError, "use -r"},
		{[]string{"cp", filepath.Join(dir, "missing"), ":" + dir}, cli.ExitError, map[bool]string{true: "no such file", false: "cannot find"}[posix]},
		{[]string{"cp", filepath.Join(dir, "f"), ":" + filepath.Join(dir, "nodir") + "/"}, cli.ExitError, "no such directory"},
		{[]string{"cp", "--bogus", "a", ":b"}, cli.ExitUsage, "bogus"},
	} {
		code, _, errs := r.run("", tc.args...)
		if code != tc.code || !strings.Contains(errs, tc.msg) {
			t.Errorf("%v: code %d stderr %q", tc.args, code, errs)
		}
	}
}

func TestCpRecursive(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	makeSample(t, src)

	// Upload to a new name: the directory becomes it.
	r.must("cp", "-r", src, ":"+filepath.Join(dir, "up"))
	sameTree(t, src, filepath.Join(dir, "up"))
	// Into an existing directory: it lands under its own name.
	os.Mkdir(filepath.Join(dir, "into"), 0o755)
	r.must("cp", "-rp", src, ":"+filepath.Join(dir, "into"))
	sameTree(t, src, filepath.Join(dir, "into", "src"))
	if a, b := mtime(t, filepath.Join(src, "deep")), mtime(t, filepath.Join(dir, "into", "src", "deep")); !a.Equal(b) {
		t.Errorf("dir mtime not preserved with -p: %v vs %v", a, b)
	}
	// Download.
	r.must("cp", "-r", "-j", "3", ":"+src, filepath.Join(dir, "down"))
	sameTree(t, src, filepath.Join(dir, "down"))
	// Read-only directories are filled before being locked.
	ro := filepath.Join(dir, "ro")
	write(t, filepath.Join(ro, "sub", "f"), "x", 0o444)
	os.Chmod(filepath.Join(ro, "sub"), 0o555)
	defer os.Chmod(filepath.Join(ro, "sub"), 0o755)
	r.must("cp", "-r", ":"+ro, filepath.Join(dir, "ro2"))
	defer os.Chmod(filepath.Join(dir, "ro2", "sub"), 0o755)
	sameTree(t, ro, filepath.Join(dir, "ro2"))
}

func mtime(t *testing.T, p string) time.Time {
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

func TestCpStdio(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	code, _, errs := r.run("from stdin\n", "cp", "-", ":"+filepath.Join(dir, "in.txt"))
	if code != 0 {
		t.Fatal(errs)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "in.txt")); string(b) != "from stdin\n" {
		t.Fatalf("stdin: %q", b)
	}
	write(t, filepath.Join(dir, "b.txt"), "B", 0o644)
	out := r.must("cp", ":"+filepath.Join(dir, "in.txt"), ":"+filepath.Join(dir, "b.txt"), "-")
	if out != "from stdin\nB" {
		t.Fatalf("stdout: %q", out)
	}
	if code, _, _ := r.run("x", "cp", "-", ":"+dir); code != cli.ExitError {
		t.Fatal("stdin into a directory accepted")
	}
}

// trackedHost serves the real fs handlers and counts the transfer
// handlers (fs.get, fs.put) running, so a test can wait until the host has
// finished reacting to an aborted transfer: host handlers run
// asynchronously to the client, which cannot (and does not) wait for them.
// A handler whose stream the client already reset may still be about to
// start, so "idle" means none ran for a while. (Such a late handler
// creates nothing: the host only touches the disk after a data frame, and
// a reset stream delivers none.)
func trackedHost(t *testing.T) (*peer.Conn, func()) {
	var running atomic.Int64
	var last atomic.Int64 // unix nanos of the last handler exit
	reg := peer.NewRegistry()
	for name, h := range rfs.Handlers() {
		if name == rfs.ServiceControl {
			reg.Handle(name, h)
			continue
		}
		reg.Handle(name, func(ctx context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
			running.Add(1)
			defer func() {
				last.Store(time.Now().UnixNano())
				running.Add(-1)
			}()
			return h(ctx, c, s, req)
		})
	}
	conn, _ := peertest.PairWith(t, reg)
	idle := func() {
		deadline := time.Now().Add(10 * time.Second)
		for running.Load() != 0 || time.Since(time.Unix(0, last.Load())) < 250*time.Millisecond {
			if time.Now().After(deadline) {
				t.Fatal("host transfer handlers still running 10s after the client gave up")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return conn, idle
}

func TestCpInterruptedIsAtomic(t *testing.T) {
	conn, hostIdle := trackedHost(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	big := make([]byte, 64<<20)
	os.WriteFile(filepath.Join(dir, "big"), big, 0o644)
	dst := filepath.Join(dir, "out")
	os.Mkdir(dst, 0o755)
	write(t, filepath.Join(dst, "big"), "previous version", 0o644)

	for _, tc := range []struct{ dl, noResume bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		dl := tc.dl
		ctx, cancel := context.WithCancel(ctx)
		p := &Progress{}
		go func() {
			for p.BytesDone.Load() == 0 {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		src, d := Path{Remote: true, Path: filepath.Join(dir, "big")}, Path{Path: dst}
		if !dl {
			src, d = Path{Path: filepath.Join(dir, "big")}, Path{Remote: true, Path: dst}
		}
		_, err := Copy(ctx, c, []Path{src}, d, CopyOptions{Progress: p, NoResume: tc.noResume})
		if err == nil {
			t.Fatalf("download=%v: interrupted copy reported success", dl)
		}
		// Copy returns only after its own writers stopped; wait for the
		// host's too. Then the old file is intact and no temporary file
		// is left; a resumable copy may keep only its hidden partial file.
		hostIdle()
		ents, _ := os.ReadDir(dst)
		var extra []string
		for _, e := range ents {
			if e.Name() != "big" && (tc.noResume || !strings.HasPrefix(e.Name(), rfs.PartialPrefix("big"))) {
				extra = append(extra, e.Name())
			}
		}
		b, _ := os.ReadFile(filepath.Join(dst, "big"))
		if len(extra) != 0 || string(b) != "previous version" {
			t.Fatalf("download=%v noResume=%v: leftovers %v, content %d bytes", dl, tc.noResume, extra, len(b))
		}
	}
	// Nothing may still be writing into the test's directories when they
	// are removed.
	hostIdle()
}

func TestSyncBothWays(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	makeSample(t, src)
	write(t, filepath.Join(src, "app.log"), "log", 0o644)
	write(t, filepath.Join(src, "node_modules", "pkg", "i.js"), "js", 0o644)
	write(t, filepath.Join(src, "sub", "node_modules"), "a file named node_modules", 0o644)
	write(t, filepath.Join(src, "build", "out.o"), "obj", 0o644)
	write(t, filepath.Join(src, "keep", "build"), "file named build", 0o644)

	for _, down := range []bool{false, true} {
		dst := filepath.Join(dir, fmt.Sprintf("dst-%v", down))
		s, d := src, ":"+dst
		if down {
			s, d = ":"+src, dst
		}
		ex := []string{"--exclude", "*.log", "--exclude", "node_modules", "--exclude", "build/"}
		out := r.must(append([]string{"sync", "--json"}, append(ex, s, d)...)...)
		var res SyncResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		got := tree(t, dst)
		for _, k := range []string{"app.log", "node_modules", "sub/node_modules", "build"} {
			if _, ok := got[k]; ok {
				t.Errorf("down=%v: %s not excluded", down, k)
			}
		}
		if got["keep/build"] == "" || got["a.txt"] == "" || got["link"] != "l a.txt" || got["emptydir"] != tm("d 750") {
			t.Errorf("down=%v: missing entries: %s", down, brief(got))
		}
		if len(res.Transferred) != 8 || res.Unchanged != 0 {
			t.Errorf("down=%v: first run %+v", down, res)
		}
		// Times are preserved, so a second run changes nothing.
		if !mtime(t, filepath.Join(src, "deep")).Equal(mtime(t, filepath.Join(dst, "deep"))) {
			t.Errorf("down=%v: dir mtime differs", down)
		}
		if !mtime(t, filepath.Join(src, "big.bin")).Equal(mtime(t, filepath.Join(dst, "big.bin"))) {
			t.Errorf("down=%v: file mtime differs", down)
		}
		out = r.must(append([]string{"sync", "--json"}, append(ex, s, d)...)...)
		res = SyncResult{}
		json.Unmarshal([]byte(out), &res)
		if len(res.Transferred)+len(res.Deleted)+len(res.Updated)+len(res.CreatedDirs) != 0 || res.Bytes != 0 {
			t.Errorf("down=%v: second run not idempotent: %+v", down, res)
		}

		// Change things on the source; dry run reports, changes nothing.
		write(t, filepath.Join(src, "a.txt"), "ALPHA!", 0o644)
		os.Chmod(filepath.Join(src, "empty"), 0o600)
		os.Remove(filepath.Join(src, "bin", "run.sh"))
		write(t, filepath.Join(src, "new", "n.txt"), "new", 0o644)
		write(t, filepath.Join(dst, "extra.txt"), "extra", 0o644)
		write(t, filepath.Join(dst, "extradir", "e"), "e", 0o644)
		write(t, filepath.Join(dst, "x.log"), "excluded on dst", 0o644)
		before := tree(t, dst)
		code, out, _ := r.run("", append([]string{"sync", "-n", "--delete"}, append(ex, s, d)...)...)
		if code != 0 {
			t.Fatal(out)
		}
		wants := []string{"+ a.txt", "+ new/n.txt", "d new/", "- bin/run.sh", "- extra.txt", "- extradir/e", "- extradir", "dry run"}
		if posix {
			wants = append(wants, "~ empty") // a mode-only change
		}
		for _, want := range wants {
			if !strings.Contains(out, want+"\n") && !strings.Contains(out, want) {
				t.Errorf("down=%v: dry run output lacks %q:\n%s", down, want, out)
			}
		}
		if strings.Contains(out, "x.log") {
			t.Errorf("dry run would delete an excluded file:\n%s", out)
		}
		if after := tree(t, dst); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Errorf("down=%v: dry run changed the destination", down)
		}
		// Real run with --delete.
		r.must(append([]string{"sync", "--delete"}, append(ex, s, d)...)...)
		got = tree(t, dst)
		if got["a.txt"] != fent("644", "ALPHA!") || got["empty"] != fent("600", "") || got["new/n.txt"] == "" {
			t.Errorf("down=%v: updates missing: %s", down, brief(got))
		}
		for _, k := range []string{"bin/run.sh", "extra.txt", "extradir"} {
			if _, ok := got[k]; ok {
				t.Errorf("down=%v: %s not deleted", down, k)
			}
		}
		if got["x.log"] == "" {
			t.Errorf("down=%v: excluded file deleted", down)
		}
		// Restore the source for the other direction.
		os.RemoveAll(src)
		makeSample(t, src)
		write(t, filepath.Join(src, "app.log"), "log", 0o644)
		write(t, filepath.Join(src, "node_modules", "pkg", "i.js"), "js", 0o644)
		write(t, filepath.Join(src, "sub", "node_modules"), "a file named node_modules", 0o644)
		write(t, filepath.Join(src, "build", "out.o"), "obj", 0o644)
		write(t, filepath.Join(src, "keep", "build"), "file named build", 0o644)
	}
}

func TestSyncTypeChangesAndChecksum(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "dst")
	write(t, filepath.Join(src, "was-dir"), "now a file", 0o644)
	os.MkdirAll(filepath.Join(src, "was-file"), 0o755)
	write(t, filepath.Join(src, "was-file", "inner"), "i", 0o644)
	os.Symlink("target", filepath.Join(src, "was-link-changed"))
	write(t, filepath.Join(dst, "was-dir", "child"), "c", 0o644)
	write(t, filepath.Join(dst, "was-file"), "f", 0o644)
	os.Symlink("old", filepath.Join(dst, "was-link-changed"))
	r.must("sync", ":"+src, dst)
	sameTree(t, src, dst)

	// Same size and mtime, different content: only --checksum notices.
	write(t, filepath.Join(src, "same"), "aaaa", 0o644)
	write(t, filepath.Join(dst, "same"), "bbbb", 0o644)
	t0 := time.Unix(1600000000, 0)
	os.Chtimes(filepath.Join(src, "same"), t0, t0)
	os.Chtimes(filepath.Join(dst, "same"), t0, t0)
	out := r.must("sync", "-v", src, ":"+dst)
	if strings.Contains(out, "+ same") {
		t.Fatalf("size+mtime sync transferred an unchanged-looking file:\n%s", out)
	}
	out = r.must("sync", "-v", "--checksum", src, ":"+dst)
	if !strings.Contains(out, "+ same") {
		t.Fatalf("--checksum missed a content change:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "same")); string(b) != "aaaa" {
		t.Fatal("content not synced")
	}
	// Equal content, different mtime: --checksum only fixes the time.
	os.Chtimes(filepath.Join(dst, "same"), time.Unix(1, 0), time.Unix(1, 0))
	out = r.must("sync", "-v", "--checksum", ":"+src, dst)
	if !strings.Contains(out, "~ same") || strings.Contains(out, "+ same") {
		t.Fatalf("checksum mtime fix:\n%s", out)
	}
	if !mtime(t, filepath.Join(dst, "same")).Equal(t0) {
		t.Fatal("mtime not fixed")
	}
	// Usage errors.
	if code, _, _ := r.run("", "sync", src, dst); code != cli.ExitUsage {
		t.Fatal("local-local sync accepted")
	}
	if code, _, _ := r.run("", "sync", filepath.Join(src, "same"), ":"+dst); code != cli.ExitError {
		t.Fatal("file source accepted")
	}
	if code, _, _ := r.run("", "sync", "--exclude", "!x", src, ":"+dst); code != cli.ExitUsage {
		t.Fatal("negated exclude accepted")
	}
}

func TestExcluder(t *testing.T) {
	x, err := NewExcluder([]string{"*.log", "node_modules", "build/", "/top", "docs/*.pdf", "**/tmp", "a/**/z", "[ab]?.c", "# comment", ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rel   string
		dir   bool
		match bool
	}{
		{"x.log", false, true}, {"a/b/x.log", false, true}, {"x.logs", false, false},
		{"node_modules", true, true}, {"a/node_modules", false, true},
		{"build", true, true}, {"build", false, false}, {"x/build", true, true},
		{"top", false, true}, {"a/top", false, false},
		{"docs/a.pdf", false, true}, {"x/docs/a.pdf", false, false}, {"docs/sub/a.pdf", false, false},
		{"tmp", true, true}, {"a/b/tmp", true, true},
		{"a/z", false, true}, {"a/b/c/z", false, true}, {"b/z", false, false},
		{"a1.c", false, true}, {"c1.c", false, false},
		{"plain.txt", false, false},
	} {
		if got := x.Match(tc.rel, tc.dir); got != tc.match {
			t.Errorf("Match(%q, dir=%v) = %v", tc.rel, tc.dir, got)
		}
	}
}

// ---- malicious host ----

// evilHost serves a fake filesystem over the real wire protocol. Its
// directory listings try every trick to make a download escape the
// destination.
type evilHost struct {
	attrs   map[string]rfs.Attr
	entries map[string][]rfs.Attr
	content map[string]string
}

func (h *evilHost) registry() *peer.Registry {
	reg := peer.NewRegistry()
	reg.Handle(rfs.ServiceControl, h.serve)
	return reg
}

func (h *evilHost) serve(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
	return rfs.ServeFake(s, func(op string, path string) (*rfs.Attr, []rfs.Attr, []byte, string) {
		if len(path) > 1 {
			path = strings.TrimSuffix(path, "/")
		}
		switch op {
		case "stat", "lstat":
			if a, ok := h.attrs[path]; ok {
				return &a, nil, nil, ""
			}
			return nil, nil, nil, "ENOENT"
		case "readdir":
			return nil, h.entries[path], nil, ""
		case "readfile":
			if c, ok := h.content[path]; ok {
				a := rfs.Attr{Mode: 0o100644, Size: int64(len(c))}
				return &a, nil, []byte(c), ""
			}
			return nil, nil, nil, "ENOENT"
		}
		return nil, nil, nil, "ENOSYS"
	})
}

func TestMaliciousHostCannotEscape(t *testing.T) {
	const dirMode, fileMode, linkMode = 0o040755, 0o100644, 0o120777
	h := &evilHost{
		attrs: map[string]rfs.Attr{
			"/evil": {Name: "evil", Mode: dirMode},
		},
		entries: map[string][]rfs.Attr{
			"/evil": {
				{Name: "../escape", Mode: fileMode, Size: 1},
				{Name: "/abs", Mode: fileMode, Size: 1},
				{Name: "..", Mode: dirMode},
				{Name: ".", Mode: dirMode},
				{Name: "a\\..\\b", Mode: fileMode, Size: 1},
				{Name: "x/../../y", Mode: fileMode, Size: 1},
				// Same name twice: first a symlink out, then a
				// directory whose files would land through it.
				{Name: "dup", Mode: linkMode, Target: "OUTSIDE"},
				{Name: "dup", Mode: dirMode},
				// A directory where the local side already has a
				// symlink pointing outside.
				{Name: "pre", Mode: dirMode},
				{Name: "ok", Mode: fileMode, Size: 2},
			},
			"/evil/dup": {{Name: "pwned", Mode: fileMode, Size: 1}},
			"/evil/pre": {{Name: "pwned", Mode: fileMode, Size: 1}},
		},
		content: map[string]string{
			"/evil/ok": "ok", "/evil/dup/pwned": "X", "/evil/pre/pwned": "X",
			"/evil/../escape": "X", "/abs": "X", "/evil/x/../../y": "X",
		},
	}
	conn, _ := peertest.PairWith(t, h.registry())
	r := &runner{t: t, conn: conn}

	for _, cmd := range []string{"cp", "sync"} {
		base := t.TempDir()
		outside := filepath.Join(base, "OUTSIDE")
		os.Mkdir(outside, 0o755)
		dst := filepath.Join(base, "dst")
		os.Mkdir(dst, 0o755)
		os.Symlink(outside, filepath.Join(dst, "pre"))
		// The symlink target named by the "dup" entry, relative to dst.
		os.Symlink(outside, filepath.Join(dst, "OUTSIDE"))

		args := []string{"sync", ":/evil", dst}
		if cmd == "cp" {
			// Copied into dst/x, the tree lands in the existing
			// dst/x/evil, which holds the hostile "pre" symlink.
			args = []string{"cp", "-r", ":/evil/", dst + "/x"}
			os.MkdirAll(filepath.Join(dst, "x", "evil"), 0o755)
			os.Rename(filepath.Join(dst, "pre"), filepath.Join(dst, "x", "evil", "pre"))
			os.Symlink(outside, filepath.Join(dst, "x", "evil", "OUTSIDE"))
		}
		_, _, errs := r.run("", args...)
		t.Logf("%s stderr:\n%s", cmd, errs)

		ents, _ := os.ReadDir(outside)
		if len(ents) != 0 {
			t.Errorf("%s: wrote outside the destination: %v", cmd, ents)
		}
		filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if d != nil && (d.Name() == "escape" || d.Name() == "abs" || d.Name() == "y") {
				t.Errorf("%s: hostile name materialised at %s", cmd, p)
			}
			return nil
		})
		okPath := filepath.Join(dst, "ok")
		if cmd == "cp" {
			okPath = filepath.Join(dst, "x", "evil", "ok")
		}
		if b, _ := os.ReadFile(okPath); string(b) != "ok" {
			t.Errorf("%s: legitimate file missing (%q)", cmd, b)
		}
	}
}

func TestProgressAndHumanBytes(t *testing.T) {
	if humanBytes(999) != "999 B" || humanBytes(1500) != "1.5 kB" || humanBytes(2_500_000_000) != "2.5 GB" {
		t.Fatal(humanBytes(1500), humanBytes(2_500_000_000))
	}
	var buf bytes.Buffer
	p := &Progress{}
	p.FilesTotal.Store(2)
	stop := showProgress(&lockedWriter{w: &buf}, p)
	time.Sleep(300 * time.Millisecond)
	stop()
	if !strings.Contains(buf.String(), "0/2 files") {
		t.Fatalf("progress output %q", buf.String())
	}
}

type lockedWriter struct {
	w io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) { return l.w.Write(p) }

// A file listed as tiny but too big when fetched (a lying host, or a file
// that grew) must stream to disk, not be buffered.
func TestDownloadStreamsWhenListingLies(t *testing.T) {
	dir := t.TempDir()
	big := make([]byte, 3<<20+5)
	rand.Read(big)
	real := filepath.Join(dir, "srv", "f")
	write(t, real, string(big), 0o644)
	srv := filepath.Join(dir, "srv")
	reg := peer.NewRegistry()
	rfs.Register(reg) // real fs.get
	reg.Handle(rfs.ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return rfs.ServeFake(s, func(op, p string) (*rfs.Attr, []rfs.Attr, []byte, string) {
			p = strings.TrimSuffix(p, "/")
			switch {
			case (op == "stat" || op == "lstat") && p == srv:
				return &rfs.Attr{Name: "srv", Mode: 0o040755}, nil, nil, ""
			case op == "readdir" && p == srv:
				return nil, []rfs.Attr{{Name: "f", Mode: 0o100644, Size: 5}}, nil, ""
			case op == "readfile":
				return &rfs.Attr{Mode: 0o100644, Size: int64(len(big))}, nil, nil, ""
			}
			return nil, nil, nil, "ENOENT"
		})
	})
	conn, _ := peertest.PairWith(t, reg)
	r := &runner{t: t, conn: conn}
	r.must("cp", "-r", ":"+srv, filepath.Join(dir, "out"))
	if b, _ := os.ReadFile(filepath.Join(dir, "out", "f")); !bytes.Equal(b, big) {
		t.Fatalf("got %d bytes, want %d", len(b), len(big))
	}
}

func TestModesHonourUmask(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	write(t, filepath.Join(src, "f"), "x", 0o777)
	os.Chmod(src, 0o777)
	mask := rfs.Umask()
	if mask&0o022 == 0 {
		t.Skipf("umask %o masks no write bits", mask)
	}
	perm := func(p string) fs.FileMode {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	// cp: minus the umask, both ways; -p keeps modes exactly.
	r.must("cp", "-r", ":"+src, filepath.Join(dir, "down"))
	r.must("cp", "-r", src, ":"+filepath.Join(dir, "up"))
	r.must("cp", "-rp", ":"+src, filepath.Join(dir, "exact"))
	for _, d := range []string{"down", "up"} {
		if perm(filepath.Join(dir, d)) != 0o777&^mask || perm(filepath.Join(dir, d, "f")) != 0o777&^mask {
			t.Errorf("cp %s: %v %v", d, perm(filepath.Join(dir, d)), perm(filepath.Join(dir, d, "f")))
		}
	}
	if perm(filepath.Join(dir, "exact", "f")) != 0o777 {
		t.Errorf("cp -p: %v", perm(filepath.Join(dir, "exact", "f")))
	}
	// sync: never grants group/other write beyond the umask, including on
	// the root; --perms copies exactly. A second run is still a no-op.
	want := 0o777 &^ (mask & 0o022)
	r.must("sync", ":"+src, filepath.Join(dir, "sdown"))
	if perm(filepath.Join(dir, "sdown")) != want || perm(filepath.Join(dir, "sdown", "f")) != want {
		t.Errorf("sync: %v %v", perm(filepath.Join(dir, "sdown")), perm(filepath.Join(dir, "sdown", "f")))
	}
	if out := r.must("sync", "-v", ":"+src, filepath.Join(dir, "sdown")); !strings.Contains(out, "transferred 0") || strings.Contains(out, "~ ") {
		t.Errorf("second sync not a no-op:\n%s", out)
	}
	r.must("sync", "--perms", src, ":"+filepath.Join(dir, "sexact"))
	if perm(filepath.Join(dir, "sexact", "f")) != 0o777 {
		t.Errorf("sync --perms: %v", perm(filepath.Join(dir, "sexact", "f")))
	}
}

func TestLocalChmodChtimesDoNotFollow(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	write(t, victim, "secret", 0o600)
	old := mtime(t, victim)
	vfi, _ := os.Stat(victim)
	oldMode := vfi.Mode()
	link := filepath.Join(dir, "link")
	os.Symlink(victim, link)
	ctx := context.Background()
	if err := (localFS{}).chmod(ctx, link, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := (localFS{}).chtimes(ctx, link, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(victim); fi.Mode() != oldMode || !fi.ModTime().Equal(old) {
		t.Fatalf("followed the link: %v (was %v) %v (was %v)", fi.Mode(), oldMode, fi.ModTime(), old)
	}
	// Where a link's own time can be set it is; elsewhere (Windows) the
	// link is simply left alone.
	if posix && !mtime(t, link).Equal(time.Unix(1, 0)) {
		t.Fatal("link time not set")
	}
}

// fakeListing serves a directory /evil holding the given names (regular
// files with content "x") through rfs.ServeFake, so that names no real
// filesystem would accept everywhere can still be listed.
func fakeListing(t *testing.T, names ...string) *runner {
	reg := peer.NewRegistry()
	reg.Handle(rfs.ServiceControl, func(ctx context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return rfs.ServeFake(s, func(op, p string) (*rfs.Attr, []rfs.Attr, []byte, string) {
			switch {
			case (op == "stat" || op == "lstat") && p == "/evil":
				return &rfs.Attr{Name: "evil", Mode: 0o040755}, nil, nil, ""
			case op == "readdir" && p == "/evil":
				var ents []rfs.Attr
				for _, n := range names {
					ents = append(ents, rfs.Attr{Name: n, Mode: 0o100644, Size: 1, Mtime: 1e18})
				}
				return nil, ents, nil, ""
			case op == "readfile" && strings.HasPrefix(p, "/evil/"):
				return &rfs.Attr{Mode: 0o100644, Size: 1}, nil, []byte("x"), ""
			}
			return nil, nil, nil, "ENOENT"
		})
	})
	conn, _ := peertest.PairWith(t, reg)
	return &runner{t: t, conn: conn}
}

func TestHostileNamesAreSanitized(t *testing.T) {
	evil := "a\x1b]0;pwned\x07\x1b[2Jb\u009b31m"
	r := fakeListing(t, evil)
	dir := t.TempDir()
	cases := [][]string{
		{"sync", "-n", ":/evil", filepath.Join(dir, "d1")},
		{"sync", "--json", "-n", ":/evil", filepath.Join(dir, "d3")},
		{"ls", ":/evil"},
		{"ls", "-l", ":/evil"},
		{"ls", "--json", ":/evil"},
	}
	if posix { // NTFS cannot hold such a name, so nothing can be written
		cases = append(cases, []string{"sync", "-v", ":/evil", filepath.Join(dir, "d2")})
	}
	for _, args := range cases {
		code, out, errs := r.run("", args...)
		if code != 0 {
			t.Errorf("%v: exit %d: %s", args, code, errs)
		}
		if strings.ContainsAny(out+errs, "\x1b\x07\u009b") {
			t.Errorf("%v printed raw control characters: %q", args, out)
		}
		if !strings.Contains(out, "pwned") {
			t.Errorf("%v: name missing from output: %q", args, out)
		}
		switch {
		case args[0] == "sync" && args[1] == "--json":
			var res SyncResult
			if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Transferred) != 1 || res.Transferred[0] != evil {
				t.Errorf("JSON does not round-trip the name: %v %q", err, res.Transferred)
			}
		case args[0] == "ls" && args[1] == "--json":
			var ents []LsEntry
			if err := json.Unmarshal([]byte(out), &ents); err != nil || len(ents) != 1 || ents[0].Name != evil {
				t.Errorf("ls JSON does not round-trip the name: %v %+v", err, ents)
			}
		}
	}
}

func partials(t *testing.T, dir, base string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), rfs.PartialPrefix(base)) {
			out = append(out, e.Name())
		}
	}
	return out
}

// interruptedCopy copies src to dst over a fresh session and kills the
// session once cut bytes have crossed it.
func interruptedCopy(t *testing.T, src, dst Path, cut int64) {
	t.Helper()
	conn := testPair(t)
	ctx := context.Background()
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	p := &Progress{}
	go func() {
		for p.BytesDone.Load() < cut {
			time.Sleep(time.Millisecond)
		}
		conn.Close() // the network goes away mid-transfer
	}()
	if _, err := Copy(ctx, c, []Path{src}, dst, CopyOptions{Progress: p, Parallel: 1}); err == nil {
		t.Fatal("interrupted copy succeeded")
	}
}

// hostGuard, if set, is attached to the host side of every testPair: the
// sessions then stand on a grant.
var hostGuard grant.Guard

func testPair(t *testing.T) *peer.Conn {
	conn, host := peertest.Pair(t)
	if hostGuard != nil {
		grant.Attach(host, hostGuard)
	}
	return conn
}

func copyWith(t *testing.T, src, dst Path, o CopyOptions) *Progress {
	t.Helper()
	conn := testPair(t)
	ctx := context.Background()
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	o.Progress = &Progress{}
	if _, err := Copy(ctx, c, []Path{src}, dst, o); err != nil {
		t.Fatal(err)
	}
	return o.Progress
}

func TestResumableCopy(t *testing.T) {
	const size = 48 << 20
	for _, upload := range []bool{true, false} {
		t.Run(fmt.Sprintf("upload=%v", upload), func(t *testing.T) {
			// The directory is reached through a symlink, as every
			// temporary directory is on macOS (/var -> /private/var):
			// resuming must not depend on how a path is spelled.
			dir := t.TempDir()
			if via := filepath.Join(t.TempDir(), "via"); os.Symlink(dir, via) == nil {
				dir = via
			}
			data := make([]byte, size)
			rand.Read(data)
			srcPath := filepath.Join(dir, "src.bin")
			os.WriteFile(srcPath, data, 0o644)
			outDir := filepath.Join(dir, "out")
			os.Mkdir(outDir, 0o755)
			src, dst := Path{Path: srcPath}, Path{Remote: true, Path: outDir}
			if !upload {
				src, dst = Path{Remote: true, Path: srcPath}, Path{Path: outDir}
			}
			final := filepath.Join(outDir, "src.bin")

			interruptedCopy(t, src, dst, 16<<20)
			if !posix {
				// Without file locks (Windows, as host or as client)
				// transfers are not resumable by design: the copy
				// starts over, and still nothing partial is exposed
				// or left behind.
				if _, err := os.Stat(final); err == nil {
					t.Fatal("partial data exposed under the final name")
				}
				p := copyWith(t, src, dst, CopyOptions{})
				if p.BytesResumed.Load() != 0 || p.BytesDone.Load() != size {
					t.Fatalf("unexpected resume: %d + %d", p.BytesDone.Load(), p.BytesResumed.Load())
				}
				if b, _ := os.ReadFile(final); !bytes.Equal(b, data) {
					t.Fatal("restarted file differs")
				}
				if ps := partials(t, outDir, "src.bin"); len(ps) != 0 {
					t.Fatalf("partials on a platform without resume: %v", ps)
				}
				return
			}
			waitStablePartial(t, outDir, "src.bin")
			if _, err := os.Stat(final); err == nil {
				t.Fatal("partial data exposed under the final name")
			}
			p := copyWith(t, src, dst, CopyOptions{})
			done, resumed := p.BytesDone.Load(), p.BytesResumed.Load()
			t.Logf("second run: %d bytes on the wire, %d resumed", done, resumed)
			if resumed < 8<<20 || done+resumed != size {
				t.Fatalf("did not resume: %d transferred + %d resumed of %d", done, resumed, size)
			}
			if b, _ := os.ReadFile(final); !bytes.Equal(b, data) {
				t.Fatal("resumed file differs")
			}
			if ps := partials(t, outDir, "src.bin"); len(ps) != 0 {
				t.Fatalf("partials left: %v", ps)
			}

			// The source changes between attempts: start over, and the
			// stale partial goes away.
			interruptedCopy(t, src, dst, 16<<20)
			waitStablePartial(t, outDir, "src.bin")
			data[size/2] ^= 0xff
			os.WriteFile(srcPath, data, 0o644)
			os.Chtimes(srcPath, time.Unix(1700000000, 0), time.Unix(1700000000, 0))
			p = copyWith(t, src, dst, CopyOptions{})
			if p.BytesResumed.Load() != 0 || p.BytesDone.Load() != size {
				t.Fatalf("changed source resumed: %d + %d", p.BytesDone.Load(), p.BytesResumed.Load())
			}
			if b, _ := os.ReadFile(final); !bytes.Equal(b, data) {
				t.Fatal("restarted file differs")
			}
			if ps := partials(t, outDir, "src.bin"); len(ps) != 0 {
				t.Fatalf("stale partials left: %v", ps)
			}

			// Kept data that no longer matches the source (corrupted
			// partial, same source identity) is not trusted.
			interruptedCopy(t, src, dst, 16<<20)
			ps := waitStablePartial(t, outDir, "src.bin")
			pf, _ := os.OpenFile(filepath.Join(outDir, ps[0]), os.O_WRONLY, 0)
			pf.WriteAt([]byte("garbage"), 100)
			pf.Close()
			p = copyWith(t, src, dst, CopyOptions{})
			if p.BytesResumed.Load() != 0 {
				t.Fatal("resumed from corrupted partial data")
			}
			if b, _ := os.ReadFile(final); !bytes.Equal(b, data) {
				t.Fatal("file differs after corrupted partial")
			}

			// --no-resume ignores partial data.
			interruptedCopy(t, src, dst, 16<<20)
			waitStablePartial(t, outDir, "src.bin")
			if p := copyWith(t, src, dst, CopyOptions{NoResume: true}); p.BytesResumed.Load() != 0 || p.BytesDone.Load() != size {
				t.Fatal("--no-resume resumed")
			}
		})
	}
}

// waitStablePartial waits until the partial file of base exists and has
// stopped growing (the host side of a broken upload finishes writing).
func waitStablePartial(t *testing.T, dir, base string) []string {
	t.Helper()
	var last int64 = -1
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ps := partials(t, dir, base)
		if len(ps) == 1 {
			fi, err := os.Stat(filepath.Join(dir, ps[0]))
			if err == nil && fi.Size() == last && last > 0 {
				time.Sleep(100 * time.Millisecond) // let the host close (unlock) it
				return ps
			}
			if err == nil {
				last = fi.Size()
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no partial file for %s: %v", base, partials(t, dir, base))
	return nil
}

func TestLs(t *testing.T) {
	r := newRunner(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b.txt"), "hello", 0o640)
	write(t, filepath.Join(dir, ".hidden"), "", 0o600)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	os.Chmod(filepath.Join(dir, "sub"), 0o755|fs.ModeSetgid)
	os.Symlink("b.txt", filepath.Join(dir, "lnk"))
	mt := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	os.Chtimes(filepath.Join(dir, "b.txt"), mt, mt)

	out := r.must("ls", ":"+dir)
	if out != "b.txt\nlnk\nsub/\n" {
		t.Fatalf("ls:\n%q", out)
	}
	out = r.must("ls", "-a", "-l", dir)
	wants := []string{
		"            5 " + mt.Local().Format("2006-01-02 15:04") + " b.txt\n",
		" sub/\n", " lnk -> b.txt\n", " .hidden\n", "\nl", "\nd",
	}
	if posix {
		// Symlink permissions are not asserted: Linux always reports
		// rwxrwxrwx, macOS the link's real mode.
		wants = append(wants, "-rw-r-----            5 ", "drwxr-sr-x ")
	}
	for _, want := range wants {
		if !strings.Contains("\n"+out, want) {
			t.Errorf("ls -l lacks %q:\n%s", want, out)
		}
	}
	var ents []LsEntry
	if err := json.Unmarshal([]byte(r.must("ls", "--json", ":"+dir)), &ents); err != nil {
		t.Fatal(err)
	}
	if len(ents) != 3 || ents[0].Name != "b.txt" || ents[0].Type != "file" || ents[0].Size != 5 ||
		ents[0].Mtime != "2021-03-04T05:06:07Z" ||
		ents[1].Type != "symlink" || ents[1].Target != "b.txt" || ents[2].Type != "dir" {
		t.Fatalf("json: %+v", ents)
	}
	if posix && (ents[0].Mode != "0640" || ents[2].Mode != "2755") {
		t.Fatalf("json modes: %+v", ents)
	}
	// A file path lists the file, even a dot file; home is the default.
	if out := r.must("ls", ":"+filepath.Join(dir, ".hidden")); out != ".hidden\n" {
		t.Fatalf("ls file: %q", out)
	}
	setHome(t, dir)
	if out := r.must("ls"); !strings.Contains(out, "sub/") {
		t.Fatalf("ls home: %q", out)
	}
	if code, _, errs := r.run("", "ls", ":"+filepath.Join(dir, "nope")); code != cli.ExitError || !strings.Contains(errs, "no such file") {
		t.Fatalf("missing: %d %s", code, errs)
	}
}

func TestSend(t *testing.T) {
	r := newRunner(t)
	home := t.TempDir()
	setHome(t, home)
	// Where the Downloads directory is differs per OS (XDG user dirs,
	// ~/Downloads, a Windows known folder; the lookups are tested in
	// rfs); here the host is simply told, which also checks the override.
	dl := filepath.Join(home, "Incoming")
	t.Setenv("REMOHOP_DOWNLOADS_DIR", dl)
	local := t.TempDir()
	write(t, filepath.Join(local, "a.tar.gz"), "archive", 0o640)
	big := make([]byte, 2*rfs.SmallFile+3)
	rand.Read(big)
	os.WriteFile(filepath.Join(local, "big.bin"), big, 0o644)

	out := r.must("send", filepath.Join(local, "a.tar.gz"), filepath.Join(local, "big.bin"))
	want := filepath.Join(local, "a.tar.gz") + " -> " + filepath.Join(dl, "a.tar.gz") + "\n" +
		filepath.Join(local, "big.bin") + " -> " + filepath.Join(dl, "big.bin") + "\n"
	if out != want {
		t.Fatalf("send output:\n%s\nwant:\n%s", out, want)
	}
	var sent []Sent
	json.Unmarshal([]byte(r.must("send", "--json", filepath.Join(local, "a.tar.gz"), filepath.Join(local, "big.bin"))), &sent)
	if len(sent) != 2 || sent[0].Path != filepath.Join(dl, "a (1).tar.gz") || sent[1].Path != filepath.Join(dl, "big (1).bin") || sent[1].Bytes != int64(len(big)) {
		t.Fatalf("second send: %+v", sent)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "big (1).bin")); !bytes.Equal(b, big) {
		t.Fatal("big content")
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "a.tar.gz")); string(b) != "archive" {
		t.Fatal("first file overwritten")
	}

	// Concurrent sends of one name never collide.
	conn, _ := peertest.Pair(t)
	c, _ := rfs.NewClient(context.Background(), conn)
	var wg sync.WaitGroup
	paths := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Send(context.Background(), c, []string{filepath.Join(local, "a.tar.gz")}, SendOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			paths <- s[0].Path
		}()
	}
	wg.Wait()
	close(paths)
	seen := map[string]bool{}
	for p := range paths {
		if seen[p] {
			t.Fatalf("two sends chose %s", p)
		}
		seen[p] = true
	}
	if len(seen) != 20 {
		t.Fatalf("%d distinct paths", len(seen))
	}
	if ents, _ := os.ReadDir(dl); len(ents) != 24 {
		t.Fatalf("%d files in Downloads (temporary files left?)", len(ents))
	}

	// Directories: -r required, fresh names.
	write(t, filepath.Join(local, "proj", "x", "y.txt"), "y", 0o644)
	if code, _, _ := r.run("", "send", filepath.Join(local, "proj")); code != cli.ExitError {
		t.Fatal("directory sent without -r")
	}
	r.must("send", "-r", filepath.Join(local, "proj"))
	out = r.must("send", "-r", filepath.Join(local, "proj"))
	if !strings.HasSuffix(out, filepath.Join(dl, "proj (1)")+"\n") {
		t.Fatalf("second dir send: %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dl, "proj (1)", "x", "y.txt")); string(b) != "y" {
		t.Fatal("dir content")
	}
	if code, _, _ := r.run("", "send", ":/etc/passwd"); code != cli.ExitUsage {
		t.Fatal("remote path accepted")
	}
}
