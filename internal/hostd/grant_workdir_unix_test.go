//go:build unix

package hostd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// With a command allow-list and a writable root, the agent must not run
// an allowed command where it can write: it would plant .git/config with
// core.fsmonitor and run the allowed `git status` there (the docs' old
// ci-bot example).
func TestAllowedCommandCannotRunInWritableRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	h, dir := ownedHost(t, hostd.Config{})
	out := t.TempDir()
	marker := filepath.Join(t.TempDir(), "pwned")
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "ci-bot", For: time.Hour,
		Commands: []string{"git status", "echo"}, Allow: []string{"files:write:" + out}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := func(p, s string, mode os.FileMode) {
		if err := c.WriteFile(ctx, filepath.Join(out, p), []byte(s), mode); err != nil {
			t.Fatal(p, err)
		}
	}
	for _, d := range []string{".git", ".git/objects", ".git/refs", ".git/refs/heads", "sub"} {
		if err := c.Mkdir(ctx, filepath.Join(out, d), 0o755); err != nil {
			t.Fatal(d, err)
		}
	}
	w(".git/HEAD", "ref: refs/heads/main\n", 0o644)
	w(".git/config", "[core]\n\trepositoryformatversion = 0\n\tfsmonitor = "+filepath.Join(out, "x.sh")+"\n", 0o644)
	w("x.sh", "#!/bin/sh\ntouch "+marker+"\n", 0o755)

	for _, cwd := range []string{out, filepath.Join(out, "sub"), filepath.Dir(out)} {
		_, err := shell.Capture(ctx, conn, shell.ExecRequest{Command: "git status", Cwd: cwd, Timeout: 20 * time.Second}, 0)
		wantRefusal(t, err, "writable root")
		if _, err := shell.Spawn(ctx, conn, shell.SpawnRequest{Command: "git status", Cwd: cwd}); err == nil || !strings.Contains(err.Error(), "writable root") {
			t.Fatalf("spawn in %s: %v", cwd, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the allowed git status ran a program the agent wrote")
	}
	// Elsewhere the allowed commands run.
	res, err := shell.Capture(ctx, conn, shell.ExecRequest{Command: "echo fine", Cwd: t.TempDir(), Timeout: 20 * time.Second}, 0)
	if err != nil || !strings.Contains(string(res.Stdout), "fine") {
		t.Fatalf("echo elsewhere: %+v %v", res, err)
	}
}
