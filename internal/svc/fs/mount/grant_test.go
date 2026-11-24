//go:build linux

package mount

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

type rulesGuard struct{ r *grant.Rules }

func (g rulesGuard) Name() string { return "test" }
func (g rulesGuard) Command(context.Context, string, string, bool, []string) error {
	return errors.New("no")
}
func (g rulesGuard) Terminal(context.Context, string) error { return errors.New("no") }
func (g rulesGuard) File(_ context.Context, _ string, a grant.Access, p string) error {
	if err := g.r.CheckPath(p, a); err != nil {
		return &grant.Refusal{Grant: "test", What: a.String(), Why: err.Error()}
	}
	return nil
}
func (g rulesGuard) Log(string, ...string)                          {}
func (g rulesGuard) Record(string, string, int, int) grant.Recorder { return nil }
func (g rulesGuard) FileScope() (*grant.Scope, error)               { return g.r.Scope(), nil }

// A mount of a session confined to a grant root works for ordinary tools,
// with everything going through the root's handle (audit finding 2).
func TestMountUnderGrant(t *testing.T) {
	canMount(t)
	base := t.TempDir()
	remote, outside := filepath.Join(base, "remote"), filepath.Join(base, "outside")
	os.Mkdir(remote, 0o755)
	os.Mkdir(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "victim"), []byte("precious"), 0o644)
	r, err := grant.Parse([]string{"files:write:" + remote}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, host := peertest.Pair(t)
	grant.Attach(host, rulesGuard{r})
	f := &fixture{t: t, conn: conn, local: t.TempDir(), remote: remote}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	mt, err := New(ctx, conn, remote, f.local, Options{ReconnectFor: -1})
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || strings.Contains(err.Error(), "permission") {
			t.Skipf("cannot mount FUSE here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		mt.Unmount()
		if isMounted(f.local) {
			unmountDir(f.local, true)
		}
	})
	must(t, os.MkdirAll(f.l("src/pkg"), 0o755))
	must(t, os.WriteFile(f.l("src/pkg/a.go"), []byte("package a\n"), 0o644))
	must(t, os.Rename(f.l("src/pkg/a.go"), f.l("src/pkg/b.go")))
	must(t, os.Symlink("pkg/b.go", f.l("src/link")))
	if b, err := os.ReadFile(f.l("src/link")); err != nil || string(b) != "package a\n" {
		t.Fatalf("read through in-root link: %q %v", b, err)
	}
	must(t, os.Chmod(f.l("src/pkg/b.go"), 0o600))
	now := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(f.l("src/pkg/b.go"), now, now))
	must(t, os.Truncate(f.l("src/pkg/b.go"), 3))
	run(t, f.local, "cp", "-a", "src", "copy")
	run(t, f.local, "mv", "-n", "copy/pkg/b.go", "copy/pkg/c.go")
	if _, err := os.Stat(f.r("copy/pkg/c.go")); err != nil {
		t.Fatalf("mv -n: %v", err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		run(t, f.local, "git", "init", "-q")
		run(t, f.local, "git", "add", ".")
		run(t, f.local, "git", "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "-m", "x")
	}
	// Links out of the root cannot be made, nor followed if planted.
	if err := os.Symlink(outside, f.l("esc")); err == nil {
		t.Error("symlink out of the root through the mount")
	}
	os.Symlink(filepath.Join(outside, "victim"), f.r("planted"))
	os.Symlink(filepath.Join(outside, "new"), f.r("dangling"))
	time.Sleep(1100 * time.Millisecond) // let the 1s caches expire
	if _, err := os.ReadFile(f.l("planted")); err == nil {
		t.Error("read through a planted link out of the root")
	}
	os.WriteFile(f.l("planted"), []byte("x"), 0o644)
	os.WriteFile(f.l("dangling"), []byte("x"), 0o644)
	if b, _ := os.ReadFile(filepath.Join(outside, "victim")); string(b) != "precious" {
		t.Fatal("victim changed")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("file created outside the root")
	}
	must(t, os.RemoveAll(f.l("copy")))
}
