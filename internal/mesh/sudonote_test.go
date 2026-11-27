package mesh

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// TestSudoNote: under sudo the mesh commands say whose configuration they
// used, the commonest reason for "not a member of an account".
func TestSudoNote(t *testing.T) {
	me, err := user.Current()
	if err != nil || me.HomeDir == "" {
		t.Skip("no current user")
	}
	t.Setenv("REMOHOP_CONFIG_DIR", "")
	t.Setenv("SUDO_USER", "")
	if n := sudoNote("/root/.config/remohop"); n != "" {
		t.Errorf("without sudo: %q", n)
	}
	t.Setenv("SUDO_USER", me.Username)
	if n := sudoNote(filepath.Join(me.HomeDir, ".config", "remohop")); !strings.Contains(n, "HOME still points to") || !strings.Contains(n, "sudo -H") {
		t.Errorf("HOME kept: %q", n)
	}
	if n := sudoNote("/elsewhere/remohop"); !strings.Contains(n, "root's configuration") || !strings.Contains(n, "sudo remohop login EMAIL") {
		t.Errorf("root's own: %q", n)
	}
	if n := dirNote("/elsewhere/remohop"); !strings.Contains(n, "configuration directory: /elsewhere/remohop") {
		t.Errorf("dirNote: %q", n)
	}
	t.Setenv("REMOHOP_CONFIG_DIR", "/elsewhere/remohop")
	if n := sudoNote("/elsewhere/remohop"); n != "" {
		t.Errorf("chosen directory: %q", n)
	}
}
