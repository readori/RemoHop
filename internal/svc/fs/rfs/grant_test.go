package rfs

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// testGuard is a grant.Guard that enforces a parsed allow list the way the
// host daemon's does, without approvals or audit.
type testGuard struct{ r *grant.Rules }

func (g testGuard) Name() string { return "test" }
func (g testGuard) Command(context.Context, string, string, bool, []string) error {
	return &grant.Refusal{Grant: "test", What: "running commands"}
}
func (g testGuard) WorkDir(context.Context, string, string) error {
	return &grant.Refusal{Grant: "test", What: "running commands"}
}
func (g testGuard) Terminal(context.Context, string) error {
	return &grant.Refusal{Grant: "test", What: "a terminal"}
}
func (g testGuard) File(_ context.Context, _ string, a grant.Access, p string) error {
	if err := g.r.CheckPath(p, a); err != nil {
		return &grant.Refusal{Grant: "test", What: a.String(), Why: err.Error()}
	}
	return nil
}
func (g testGuard) Log(string, ...string)                          {}
func (g testGuard) Record(string, string, int, int) grant.Recorder { return nil }
func (g testGuard) FileScope() (*grant.Scope, error)               { return g.r.Scope(), nil }

// newGrantedClient connects a client whose session stands on a grant with
// the given allow list.
func newGrantedClient(t *testing.T, allow ...string) *Client {
	t.Helper()
	r, err := grant.Parse(allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	return newGuardedClient(t, testGuard{r})
}

func newGuardedClient(t *testing.T, g grant.Guard) *Client {
	t.Helper()
	conn, host := peertest.Pair(t)
	grant.Attach(host, g)
	c, err := NewClient(tctx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// grantDirs makes base/A (the granted root, with a file in it) and
// base/outside (with a file that must stay untouched).
func grantDirs(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		// /var/folders -> /private/var/folders: keep paths canonical
		// so the expectations below compare like with like.
		base, _ = filepath.EvalSymlinks(base)
	}
	root, outside = filepath.Join(base, "A"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "in.txt"), []byte("inside"), 0o644)
	os.WriteFile(filepath.Join(outside, "victim"), []byte("precious"), 0o644)
	return root, outside
}

func wantUntouched(t *testing.T, outside string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(outside, "victim")); err != nil || string(b) != "precious" {
		t.Fatalf("file outside the root changed: %q %v", b, err)
	}
	ents, _ := os.ReadDir(outside)
	for _, e := range ents {
		if e.Name() != "victim" {
			t.Fatalf("file created outside the root: %s", e.Name())
		}
	}
}

// A restricted session must not be able to swap its grant root for a link
// to somewhere else and then write there (audit finding 2, first path).
func TestGrantRootCannotBeReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; see grant_windows_test.go")
	}
	root, outside := grantDirs(t)
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	// Normal work inside the root goes on.
	if err := c.WriteFile(ctx, filepath.Join(root, "new.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write inside root: %v", err)
	}
	if err := c.MkdirAll(ctx, filepath.Join(root, "sub", "deeper"), 0o755); err != nil {
		t.Fatalf("mkdir inside root: %v", err)
	}
	// The root entry itself may not be removed, renamed or replaced.
	if err := c.RemoveAll(ctx, root); err == nil {
		t.Error("removing the grant root was allowed")
	}
	if err := c.Rmdir(ctx, root); err == nil {
		t.Error("rmdir of the grant root was allowed")
	}
	if err := c.Rename(ctx, filepath.Join(root, "sub"), root); err == nil {
		t.Error("renaming onto the grant root was allowed")
	}
	if err := c.Symlink(ctx, outside, root); err == nil {
		t.Error("a symlink in place of the grant root was allowed")
	}
	// Whatever happened above, nothing may land outside.
	c.WriteFile(ctx, filepath.Join(root, "pwn"), []byte("x"), 0o644)
	if f, err := c.OpenFile(ctx, filepath.Join(root, "pwn2"), os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		f.WriteAt(ctx, []byte("x"), 0)
		f.Close()
	}
	wantUntouched(t, outside)
	if b, err := os.ReadFile(filepath.Join(root, "in.txt")); err != nil || string(b) != "inside" {
		t.Fatalf("root content lost: %q %v", b, err)
	}
}

// A root replaced on the host after the grant was loaded is not followed
// to its new place (audit finding 2: the root's identity is pinned).
func TestGrantRootIdentityPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; see grant_windows_test.go")
	}
	root, outside := grantDirs(t)
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	if _, err := c.ReadFile(ctx, filepath.Join(root, "in.txt")); err != nil {
		t.Fatalf("read inside root: %v", err)
	}
	// Someone on the host moves the root away and puts a link there.
	if err := os.Rename(root, root+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadFile(ctx, filepath.Join(root, "victim")); err == nil {
		t.Error("read through a replaced root was allowed")
	}
	c.WriteFile(ctx, filepath.Join(root, "pwn"), []byte("x"), 0o644)
	c.WriteFile(ctx, filepath.Join(root, "victim"), []byte("x"), 0o644)
	wantUntouched(t, outside)
}

// A dangling link inside the root that points outside must not let a
// create through it make a file outside (audit finding 2, second path).
func TestGrantDanglingLinkCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; see grant_windows_test.go")
	}
	root, outside := grantDirs(t)
	// Planted on the host (by anyone): the session cannot make it itself.
	if err := os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "newdir", "x"), filepath.Join(root, "dangling2")); err != nil {
		t.Fatal(err)
	}
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	if f, err := c.OpenFile(ctx, filepath.Join(root, "dangling"), os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		f.WriteAt(ctx, []byte("pwned"), 0)
		f.Close()
		t.Error("create through a dangling link to outside was allowed")
	}
	if f, err := c.OpenFile(ctx, filepath.Join(root, "dangling2"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644); err == nil {
		f.Close()
		t.Error("create through a dangling link to outside was allowed")
	}
	c.Mkdir(ctx, filepath.Join(root, "dangling", "d"), 0o755)
	wantUntouched(t, outside)

	// The session may not make such a link either.
	if err := c.Symlink(ctx, filepath.Join(outside, "other"), filepath.Join(root, "mine")); err == nil {
		t.Error("a symlink pointing out of the root was allowed")
	}
	if err := c.Symlink(ctx, "../outside/other", filepath.Join(root, "mine2")); err == nil {
		t.Error("a relative symlink pointing out of the root was allowed")
	}
	// Links that stay inside work, relative or absolute, and can be
	// followed.
	if err := c.Symlink(ctx, "in.txt", filepath.Join(root, "rel")); err != nil {
		t.Fatalf("relative link inside the root: %v", err)
	}
	if err := c.Symlink(ctx, filepath.Join(root, "in.txt"), filepath.Join(root, "abs")); err != nil {
		t.Fatalf("absolute link inside the root: %v", err)
	}
	for _, l := range []string{"rel", "abs"} {
		if b, err := c.ReadFile(ctx, filepath.Join(root, l)); err != nil || string(b) != "inside" {
			t.Errorf("read through %s link: %q %v", l, b, err)
		}
	}
	if err := c.WriteFile(ctx, filepath.Join(root, "abs"), []byte("via link"), 0o644); err != nil {
		t.Errorf("write through an in-root link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "in.txt")); string(b) != "via link" {
		t.Errorf("write through link landed elsewhere: %q", b)
	}
	// A dangling link that stays inside creates its target inside.
	os.Symlink("made.txt", filepath.Join(root, "inner"))
	if f, err := c.OpenFile(ctx, filepath.Join(root, "inner"), os.O_WRONLY|os.O_CREATE, 0o644); err != nil {
		t.Errorf("create through an in-root dangling link: %v", err)
	} else {
		f.Close()
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err != nil {
		t.Errorf("in-root dangling link target not created: %v", err)
	}
	wantUntouched(t, outside)
}

// Hard links made by a restricted session stay within its root.
func TestGrantHardLinks(t *testing.T) {
	root, outside := grantDirs(t)
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	if err := c.Link(ctx, filepath.Join(outside, "victim"), filepath.Join(root, "h")); err == nil {
		t.Error("hard link to a file outside the root was allowed")
	}
	if err := c.Link(ctx, filepath.Join(root, "in.txt"), filepath.Join(root, "h2")); err != nil {
		t.Errorf("hard link inside the root: %v", err)
	}
}
