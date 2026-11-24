//go:build windows

package rfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// junction makes link a directory junction (a mount-point reparse point,
// which needs no privileges, unlike a symbolic link) to target.
func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
}

// Junctions are followed only inside the grant root, and a root swapped
// for a junction is refused (audit finding 2, Windows reparse points).
func TestGrantJunctions(t *testing.T) {
	root, outside := grantDirs(t)
	os.Mkdir(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "s.txt"), []byte("sub"), 0o644)
	junction(t, filepath.Join(root, "esc"), outside)
	junction(t, filepath.Join(root, "inner"), filepath.Join(root, "sub"))
	c := newGrantedClient(t, "files:write:"+root)
	ctx := tctx(t)
	if _, err := c.ReadFile(ctx, filepath.Join(root, "esc", "victim")); err == nil {
		t.Error("read through a junction out of the root")
	}
	c.WriteFile(ctx, filepath.Join(root, "esc", "victim"), []byte("x"), 0o644)
	c.WriteFile(ctx, filepath.Join(root, "esc", "new"), []byte("x"), 0o644)
	if f, err := c.OpenFile(ctx, filepath.Join(root, "esc", "new2"), os.O_WRONLY|os.O_CREATE, 0o644); err == nil {
		f.Close()
	}
	if _, err := c.ReadDir(ctx, filepath.Join(root, "esc")); err == nil {
		t.Error("listing through a junction out of the root")
	}
	wantUntouched(t, outside)
	// A junction that stays inside works.
	if b, err := c.ReadFile(ctx, filepath.Join(root, "inner", "s.txt")); err != nil || string(b) != "sub" {
		t.Errorf("read through an in-root junction: %q %v", b, err)
	}
	if err := c.WriteFile(ctx, filepath.Join(root, "inner", "w.txt"), []byte("w"), 0o644); err != nil {
		t.Errorf("write through an in-root junction: %v", err)
	}
	// The root itself cannot be removed, and a root replaced on the
	// host by a junction to elsewhere is refused.
	if err := c.RemoveAll(ctx, root); err == nil {
		t.Error("removing the grant root was allowed")
	}
	c.Close()
	c = newGrantedClient(t, "files:write:"+root)
	if _, err := c.ReadFile(ctx, filepath.Join(root, "in.txt")); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(root, "esc"))
	os.Remove(filepath.Join(root, "inner"))
	if err := os.Rename(root, root+".old"); err != nil {
		t.Skipf("cannot move the root: %v", err)
	}
	junction(t, root, outside)
	if _, err := c.ReadFile(ctx, filepath.Join(root, "victim")); err == nil {
		t.Error("read through a root replaced by a junction")
	}
	c.WriteFile(ctx, filepath.Join(root, "pwn"), []byte("x"), 0o644)
	wantUntouched(t, outside)
}
