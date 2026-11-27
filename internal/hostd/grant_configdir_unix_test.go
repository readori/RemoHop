//go:build unix

package hostd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// A file grant whose root contains the host's config directory (for
// example files:write:~ with the config directory in ~/.config/remohop)
// must not reach into it: it could rewrite grants.json and widen its own
// grant (the file is re-read whenever it changes).
func TestGrantCannotRewriteItsOwnGrant(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	root := filepath.Dir(dir) // stands for "~": the config dir lies below it
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "agent", For: time.Hour,
		Allow: []string{"files:write:" + root}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := testCtx(t)
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	gpath := filepath.Join(dir, hostd.GrantsFile)
	before, err := os.ReadFile(gpath)
	if err != nil {
		t.Fatal(err)
	}
	const why = "configuration directory"

	_, err = c.ReadFile(ctx, gpath)
	wantRefusal(t, err, why)
	_, err = c.Stat(ctx, gpath)
	wantRefusal(t, err, why)
	_, err = c.ReadDir(ctx, dir)
	wantRefusal(t, err, why)
	_, err = c.Download(ctx, gpath, 0)
	wantRefusal(t, err, why)

	// What the agent would write: its own grant, widened.
	var gs []grant.Grant
	json.Unmarshal(before, &gs)
	gs[0].Allow = []string{"shell", "files"}
	nb, _ := json.Marshal(gs)
	wantRefusal(t, c.WriteFile(ctx, gpath, nb, 0o600), why)
	_, err = c.Upload(ctx, gpath, bytes.NewReader(nb), rfs.PutOptions{})
	wantRefusal(t, err, why)
	wantRefusal(t, c.Remove(ctx, gpath), why)
	wantRefusal(t, c.Rename(ctx, filepath.Join(root, "x"), gpath), why)

	// Nor around it: moving the config directory away to edit it under
	// another name, replacing it, or leading a link into it.
	wantRefusal(t, c.Rename(ctx, dir, filepath.Join(root, "moved")), why)
	if err := c.Mkdir(ctx, filepath.Join(root, "decoy"), 0o700); err != nil {
		t.Fatal(err)
	}
	wantRefusal(t, c.Rename(ctx, filepath.Join(root, "decoy"), dir), why)
	wantRefusal(t, c.Symlink(ctx, dir, filepath.Join(root, "cfg")), why)
	wantRefusal(t, c.Symlink(ctx, ".", filepath.Join(root, "self")), why)
	// A relative link that is harmless where it is made may not be moved
	// to where it leads to a directory above the config directory.
	if err := c.Mkdir(ctx, filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.Symlink(ctx, ".", filepath.Join(root, "sub", "self")); err != nil {
		t.Fatalf("an in-root link: %v", err)
	}
	wantRefusal(t, c.Rename(ctx, filepath.Join(root, "sub", "self"), filepath.Join(root, "self")), why)
	// A link the owner made into it is not followed.
	if err := os.Symlink(dir, filepath.Join(root, "owners-link")); err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadFile(ctx, filepath.Join(root, "owners-link", hostd.GrantsFile))
	wantRefusal(t, err, why)

	// Everything else in the root still works.
	if err := c.WriteFile(ctx, filepath.Join(root, "notes"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(gpath); !bytes.Equal(before, after) {
		t.Fatal("grants.json changed")
	}
	if _, err := run(t, conn, "echo escalated", false); err == nil {
		t.Fatal("the grant runs commands")
	}
}

// A read grant (files:read, or a read root containing the config
// directory) must not read the secrets in it: the temporary password in
// host.json, the identity key, the pairing tokens.
func TestGrantCannotReadConfigDir(t *testing.T) {
	for _, allow := range []string{"files:read", "files:read:ROOT", "files"} {
		t.Run(allow, func(t *testing.T) {
			h, dir := ownedHost(t, hostd.Config{})
			allow := strings.Replace(allow, "ROOT", filepath.Dir(dir), 1)
			conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "reader", For: time.Hour, Allow: []string{allow}})
			if err != nil {
				t.Fatal(err)
			}
			// What `remohop host` does at start and on every change.
			sb, _ := json.Marshal(h.Status())
			os.WriteFile(filepath.Join(dir, "host.json"), sb, 0o600)
			ctx := testCtx(t)
			c, err := rfs.NewClient(ctx, conn)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			for _, name := range []string{"host.json", "identity.key", "paired.json", hostd.GrantsFile} {
				_, err := c.ReadFile(ctx, filepath.Join(dir, name))
				wantRefusal(t, err, "configuration directory")
				if r, err := c.Download(ctx, filepath.Join(dir, name), 0); err == nil {
					b, _ := io.ReadAll(r)
					r.Close()
					if len(b) > 0 {
						t.Fatalf("downloaded %s", name)
					}
				}
			}
			// Spelled with "..", from elsewhere.
			_, err = c.ReadFile(ctx, filepath.Join(dir, "..", filepath.Base(dir), "host.json"))
			if err == nil {
				t.Fatal("read host.json through ..")
			}
		})
	}
}

// `remohop host grant` warns when a root contains the config directory.
func TestGrantWarnsAboutConfigDir(t *testing.T) {
	_, dir := ownedHost(t, hostd.Config{})
	code, _, errs := runCmd("host", "", "grant", "wide", "--allow", "files:read:"+filepath.Dir(dir))
	if code != 0 || !strings.Contains(errs, "warning") || !strings.Contains(errs, dir) {
		t.Fatalf("grant over the config directory: %d %q", code, errs)
	}
	code, _, errs = runCmd("host", "", "grant", "narrow", "--allow", "files:read:"+t.TempDir())
	if code != 0 || strings.Contains(errs, "warning") {
		t.Fatalf("grant elsewhere: %d %q", code, errs)
	}
}
