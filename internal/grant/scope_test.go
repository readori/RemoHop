package grant

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func scopeDirs(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		base, _ = filepath.EvalSymlinks(base)
	}
	root, outside = filepath.Join(base, "A"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "in.txt"), []byte("in"), 0o644)
	return root, outside
}

// A link whose target does not exist is resolved to where a create through
// it would land (audit finding 2: RealPath used to treat it as a missing
// path inside the root).
func TestCheckPathDanglingLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; see scope_windows_test.go")
	}
	root, outside := scopeDirs(t)
	r := mustParse(t, []string{"files:write:" + root})
	os.Symlink(filepath.Join(outside, "new.txt"), filepath.Join(root, "dangling"))
	os.Symlink("../outside/rel.txt", filepath.Join(root, "dangling-rel"))
	os.Symlink("dangling", filepath.Join(root, "chain"))
	for _, n := range []string{"dangling", "dangling-rel", "chain"} {
		if err := r.CheckPath(filepath.Join(root, n), Write); err == nil {
			t.Errorf("write through dangling link %s allowed", n)
		}
	}
	if got, _ := RealPath(filepath.Join(root, "chain")); got != filepath.Join(outside, "new.txt") {
		t.Errorf("RealPath of a dangling chain: %s", got)
	}
	// One that stays inside is fine.
	os.Symlink("made.txt", filepath.Join(root, "inner"))
	if err := r.CheckPath(filepath.Join(root, "inner"), Write); err != nil {
		t.Errorf("in-root dangling link: %v", err)
	}
	// Loops end.
	os.Symlink("loop2", filepath.Join(root, "loop1"))
	os.Symlink("loop1", filepath.Join(root, "loop2"))
	if err := r.CheckPath(filepath.Join(root, "loop1"), Read); err == nil {
		t.Error("link loop allowed")
	}
}

// The root's identity is pinned when the rules are first used: a root
// replaced afterwards refuses everything below it.
func TestRootIdentityPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows; see scope_windows_test.go")
	}
	t.Run("kept-open", testRootIdentity)
	// As on Windows: pinned by identity, opened for each operation.
	keep := keepRoots
	keepRoots = false
	defer func() { keepRoots = keep }()
	t.Run("reopened", testRootIdentity)
}

func testRootIdentity(t *testing.T) {
	root, outside := scopeDirs(t)
	g := Grant{Name: "g-" + t.Name(), TokenID: "01", Allow: []string{"files:write:" + root}, Expires: time.Now().Add(time.Hour)}
	r, err := g.Rules()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckPath(filepath.Join(root, "in.txt"), Write); err != nil {
		t.Fatal(err)
	}
	os.Rename(root, root+".old")
	os.Symlink(outside, root)
	err = r.CheckPath(filepath.Join(root, "x"), Write)
	var rc *RootChangedError
	if !errors.As(err, &rc) {
		t.Fatalf("replaced root: %v", err)
	}
	// Re-reading the grants file keeps the pin...
	r2, _ := g.Rules()
	if r2.Scope() != r.Scope() || r2.CheckPath(filepath.Join(root, "x"), Write) == nil {
		t.Fatal("a reload re-pinned the replaced root")
	}
	// ... a reissued grant (new credential) pins what is there now.
	g.TokenID = "02"
	r3, _ := g.Rules()
	if r3.Scope() == r.Scope() || r3.Scope().ID() == r.Scope().ID() {
		t.Fatal("reissued grant shares the old scope")
	}
	if err := r3.CheckPath(filepath.Join(root, "x"), Write); err != nil {
		t.Fatalf("reissued grant: %v", err)
	}
	// A root removed after pinning refuses too.
	os.Remove(root)
	if err := r3.CheckPath(filepath.Join(root, "x"), Write); !errors.As(err, &rc) {
		t.Fatalf("removed root: %v", err)
	}
}

// A root that does not exist yet is allowed by the policy (nothing is
// found below it) and pinned when it appears.
func TestRootMissingThenCreated(t *testing.T) {
	root, _ := scopeDirs(t)
	later := filepath.Join(root, "later")
	r := mustParse(t, []string{"files:write:" + later})
	if err := r.CheckPath(filepath.Join(later, "x"), Write); err != nil {
		t.Fatalf("missing root: %v", err)
	}
	if _, err := r.Scope().Resolve(filepath.Join(later, "x"), Write, true); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolve under a missing root: %v", err)
	}
	os.Mkdir(later, 0o755)
	pl, err := r.Scope().Resolve(filepath.Join(later, "x"), Write, true)
	defer pl.Release()
	if err != nil || pl.Root == nil || pl.Rel != "x" {
		t.Fatalf("resolve after creation: %+v %v", pl, err)
	}
	fi, _ := os.Lstat(later)
	if p, ok := r.Scope().IsRoot(fi); !ok || p != later {
		t.Fatal("IsRoot")
	}
}

func TestResolveAndCheckLink(t *testing.T) {
	root, outside := scopeDirs(t)
	sub := filepath.Join(root, "sub")
	os.Mkdir(sub, 0o755)
	r := mustParse(t, []string{"files:read:" + root, "files:write:" + sub})
	s := r.Scope()
	pl, err := s.Resolve(filepath.Join(sub, "f"), Write, false)
	pl.Release()
	if err != nil || pl.Root == nil || pl.Rel != "f" || pl.RootPath != sub {
		t.Fatalf("write place: %+v %v", pl, err)
	}
	pl, err = s.Resolve(filepath.Join(sub, "f"), Read, true)
	pl.Release()
	if err != nil || pl.RootPath != sub {
		t.Fatalf("most specific root first: %+v %v", pl, err)
	}
	pl, err = s.Resolve(sub, Write, false)
	pl.Release()
	if err != nil || pl.Rel != "." {
		t.Fatalf("root itself: %+v %v", pl, err)
	}
	if pl, err := s.Resolve(filepath.Dir(root), Meta, true); err != nil || pl.Root != nil {
		t.Fatalf("ancestor meta: %+v %v", pl, err)
	}
	link, _ := s.Resolve(filepath.Join(sub, "l"), Write, false)
	defer link.Release()
	for _, ok := range []string{"f", "./x/f", filepath.Join(sub, "f")} {
		if err := s.CheckLink(link, ok); err != nil {
			t.Errorf("link to %s: %v", ok, err)
		}
	}
	// ".." is refused even when it stays inside: moving the link (or a
	// directory above it) would change where it leads.
	for _, bad := range []string{"../in.txt", outside, filepath.Join(outside, "x"), "../../outside", "./x/../f", "x/..", sub + string(filepath.Separator) + ".." + string(filepath.Separator) + "sub"} {
		if err := s.CheckLink(link, bad); err == nil || !strings.Contains(err.Error(), "must point inside") {
			t.Errorf("link to %s: %v", bad, err)
		}
	}
	if runtime.GOOS != "windows" {
		// ".." after a link goes to the link target's parent, as the
		// OS resolves it, not lexically.
		os.Symlink(filepath.Join(outside), filepath.Join(sub, "esc"))
		if err := s.CheckLink(link, "esc/../outside"); err == nil {
			t.Error("link through an escaping link allowed")
		}
	}
	all := mustParse(t, []string{"files"})
	if pl, err := all.Scope().Resolve(filepath.Join(outside, "x"), Write, true); err != nil || pl.Root != nil {
		t.Fatalf("unconfined: %+v %v", pl, err)
	}
}
