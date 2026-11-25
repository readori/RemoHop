package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/svc/clip"
)

// What a tool writes on this computer came from a remote machine, and a
// misled model chooses the path: writes are confined to the download
// directories (suspected item in the security audit: remohop_download to
// ~/.ssh/authorized_keys).
func TestLocalWritesStayInDownloadDirs(t *testing.T) {
	board := clip.NewMemoryBoard()
	clip.SetHostBoard(board)
	t.Cleanup(func() { clip.SetHostBoard(clip.Virtual()) })
	downloads, elsewhere := t.TempDir(), t.TempDir()
	h := newHarness(t, func(o *Options) { o.WriteDirs = []string{downloads} })
	h.ok("remohop_clipboard_set", map[string]any{"text": "ssh-ed25519 AAAA attacker"}, nil)
	remote := t.TempDir()
	remoteFile := filepath.Join(remote, "authorized_keys")
	os.WriteFile(remoteFile, []byte("ssh-ed25519 AAAA attacker\n"), 0o644)

	sshDir := filepath.Join(elsewhere, ".ssh")
	os.Mkdir(sshDir, 0o700)
	victim := filepath.Join(sshDir, "authorized_keys")
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"remohop_download", map[string]any{"remote_path": remoteFile, "local_path": victim}},
		{"remohop_download", map[string]any{"remote_path": remoteFile, "local_path": sshDir}},
		{"remohop_download", map[string]any{"remote_path": remoteFile, "local_path": "../" + filepath.Base(elsewhere) + "/x"}},
		{"remohop_clipboard_get", map[string]any{"save_path": victim}},
		{"remohop_screenshot", map[string]any{"save_path": victim}},
	} {
		msg := h.fail(c.tool, c.args)
		if !strings.Contains(msg, "outside the directories") && !strings.Contains(msg, "must stay inside") {
			t.Errorf("%s %v: %s", c.tool, c.args, msg)
		}
	}
	if _, err := os.Stat(victim); err == nil {
		t.Fatal("a tool wrote outside the download directories")
	}
	// A link inside the download directory does not lead out either.
	if os.Symlink(sshDir, filepath.Join(downloads, "ssh")) == nil {
		if msg := h.fail("remohop_download", map[string]any{"remote_path": remoteFile, "local_path": filepath.Join(downloads, "ssh", "authorized_keys")}); !strings.Contains(msg, "outside the directories") {
			t.Errorf("download through a link: %s", msg)
		}
	}

	// Inside, by absolute path or by bare name.
	h.ok("remohop_download", map[string]any{"remote_path": remoteFile, "local_path": filepath.Join(downloads, "a.txt")}, nil)
	var tr transferOut
	h.ok("remohop_download", map[string]any{"remote_path": remoteFile, "local_path": "b.txt"}, &tr)
	if tr.To != filepath.Join(downloads, "b.txt") {
		t.Fatalf("bare name saved to %s", tr.To)
	}
	if b, err := os.ReadFile(filepath.Join(downloads, "b.txt")); err != nil || len(b) == 0 {
		t.Fatalf("bare name: %q %v", b, err)
	}
}
