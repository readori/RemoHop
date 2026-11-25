//go:build unix

package hostd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// File roots are pinned when the host loads a grant and the file service
// works through them (audit findings 2 and 3), end to end through the
// host daemon's own guard.
func TestGrantFileRootsPinned(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		base, _ = filepath.EvalSymlinks(base)
	}
	root, outside := filepath.Join(base, "A"), filepath.Join(base, "outside")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "victim"), []byte("precious"), 0o644)
	req := hostd.GrantRequest{Name: "agent", For: time.Hour, Allow: []string{"files:write:" + root}}
	conn, _, err := redeem(t, h, dir, req)
	if err != nil {
		t.Fatal(err)
	}
	c, err := rfs.NewClient(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := testCtx(t)

	// The root entry cannot be swapped for a link out.
	wantRefusal(t, c.RemoveAll(ctx, root), "grant root")
	if err := c.Symlink(ctx, outside, root); err == nil {
		t.Fatal("symlink in place of the root")
	}

	// A resumable upload, broken off.
	dst := filepath.Join(root, "big")
	part, err := c.PartialPath(ctx, dst, "src1")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	go func() {
		pw.Write(bytes.Repeat([]byte("a"), 1<<20))
		time.Sleep(200 * time.Millisecond)
		cancel()
		pw.CloseWithError(errors.New("broke"))
	}()
	c.Upload(ctx2, dst, pr, rfs.PutOptions{Partial: part, PartialKey: "src1"})
	for i := 0; i < 100; i++ {
		if fi, err := os.Stat(part); err == nil && fi.Size() == 1<<20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Reissued (re-signed) under the same name: the old session's file
	// operations are refused, and the new credential does not resume the
	// old credential's partial file.
	_, code, err := hostd.IssueGrant(dir, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stat(ctx, root); err == nil {
		t.Error("old session still works after the grant was reissued")
	}
	conn2, _, err := redeemAs(t, h, t.TempDir(), code)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := rfs.NewClient(testCtx(t), conn2)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if p2, err := c2.PartialPath(ctx, dst, "src1"); err != nil || p2 == part {
		t.Fatalf("reissued grant resumes the old partial file: %s %v", p2, err)
	}

	// The root is replaced on the host by a link to elsewhere: refused.
	os.Rename(root, root+".old")
	os.Symlink(outside, root)
	_, err = c2.ReadFile(ctx, filepath.Join(root, "victim"))
	wantRefusal(t, err, "no longer the directory the grant was issued for")
	c2.WriteFile(ctx, filepath.Join(root, "victim"), []byte("x"), 0o644)
	if b, _ := os.ReadFile(filepath.Join(outside, "victim")); string(b) != "precious" {
		t.Fatal("wrote through a replaced root")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "victim") {
		t.Fatalf("outside: %v", ents)
	}
}

// A session confined to a root cannot plant a link that leads out of it by
// making a link that leads inside and then moving it, or by replacing a
// directory that a link's ".." climbs out of (security audit finding 4).
func TestGrantLinksStayInsideAfterMoves(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		base, _ = filepath.EvalSymlinks(base)
	}
	root, outside := filepath.Join(base, "A"), filepath.Join(base, "outside")
	os.MkdirAll(root, 0o755)
	os.MkdirAll(outside, 0o755)
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent", For: time.Hour, Allow: []string{"files:write:" + root}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := rfs.NewClient(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := testCtx(t)

	// 1. A link to ../outside two levels down (inside), then moved to the
	// top, where it leads to the root's sibling.
	if err := c.MkdirAll(ctx, filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Symlink(ctx, "../outside", filepath.Join(root, "a", "b", "l3")); err == nil {
		c.Rename(ctx, filepath.Join(root, "a", "b", "l3"), filepath.Join(root, "l3"))
	}
	// 2. A link through a directory that is then replaced by a link.
	if err := c.MkdirAll(ctx, filepath.Join(root, "d", "e"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Symlink(ctx, "d/e/../..", filepath.Join(root, "l")); err == nil {
		c.Remove(ctx, filepath.Join(root, "d", "e"))
		c.Symlink(ctx, ".", filepath.Join(root, "d", "e"))
	}
	// Plain descendant targets are still allowed, and stay inside when
	// moved.
	if err := c.Symlink(ctx, "b", filepath.Join(root, "a", "lb")); err != nil {
		t.Fatalf("link to a subdirectory: %v", err)
	}
	if err := c.Rename(ctx, filepath.Join(root, "a", "lb"), filepath.Join(root, "lb")); err != nil {
		t.Fatal(err)
	}

	rr, _ := filepath.EvalSymlinks(root)
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink == 0 {
			return nil
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			if rel, _ := filepath.Rel(rr, real); rel == ".." || strings.HasPrefix(rel, "../") {
				target, _ := os.Readlink(p)
				t.Errorf("%s -> %s leads out of the root to %s", p, target, real)
			}
		}
		return nil
	})
}
