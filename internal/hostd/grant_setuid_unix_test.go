//go:build unix

package hostd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// A write grant must not make setuid, setgid or sticky files: on a host
// that runs as root (the system service's default) a setuid file it wrote
// would be a setuid-root program for anyone on the machine.
func TestGrantCannotSetSetuidBits(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	root := t.TempDir()
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "drop", For: time.Hour, Allow: []string{"files:write:" + root}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	const special = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	check := func(p string) {
		t.Helper()
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&special != 0 {
			t.Fatalf("%s: mode %v under a grant", filepath.Base(p), fi.Mode())
		}
		if fi.Mode().Perm() != 0o755 {
			t.Fatalf("%s: permission bits %v, want 0755", filepath.Base(p), fi.Mode().Perm())
		}
	}
	// Whole-file write with a mode, then chmod.
	p := filepath.Join(root, "sh")
	if err := c.WriteFile(ctx, p, []byte("#!/bin/sh\n"), 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	check(p)
	if err := c.Chmod(ctx, p, 0o755|os.ModeSetuid|os.ModeSetgid|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	check(p)
	// Upload with a mode.
	up := filepath.Join(root, "up")
	if _, err := c.Upload(ctx, up, bytes.NewReader([]byte("x")), rfs.PutOptions{Mode: 0o755 | os.ModeSetuid}); err != nil {
		t.Fatal(err)
	}
	check(up)
	// Create through open.
	op := filepath.Join(root, "opened")
	f, err := c.OpenFile(ctx, op, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755|os.ModeSetuid)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	fi, _ := os.Stat(op)
	if fi.Mode()&special != 0 {
		t.Fatalf("opened: mode %v", fi.Mode())
	}
	// A setgid directory.
	d := filepath.Join(root, "d")
	if err := c.Mkdir(ctx, d, 0o755|os.ModeSetgid|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(d); fi.Mode()&special != 0 {
		t.Fatalf("mkdir: mode %v", fi.Mode())
	}
}
