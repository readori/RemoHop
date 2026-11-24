package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The launchctl paths run against a fake launchctl, so they check the
// sequence of verbs, not launchd's behaviour; see the macOS CI snippet.
func TestLaunchdLifecycleWithFake(t *testing.T) {
	log := fakeBin(t, "launchctl")
	print := filepath.Join(t.TempDir(), "print")
	os.WriteFile(print, []byte("\tstate = running\n\tpid = 99\n"), 0o600)
	t.Setenv("FAKE_PRINT", print)

	b := &launchd{home: t.TempDir(), euid: 501, daemonDir: t.TempDir()}
	ctx := context.Background()
	inst, err := b.Install(ctx, Options{Exe: "/usr/local/bin/remohop"})
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(b.home, "Library/LaunchAgents/com.remohop.host.plist")
	if inst.Path != plist {
		t.Errorf("path %s", inst.Path)
	}
	if _, err := os.Stat(filepath.Join(b.home, "Library/Logs/remohop")); err != nil {
		t.Error("log dir missing")
	}
	all := strings.Join(calls(t, log), "\n")
	for _, w := range []string{
		"launchctl bootout gui/501/com.remohop.host", // already loaded in this fake
		"launchctl bootstrap gui/501 " + plist,
		"launchctl enable gui/501/com.remohop.host",
		"launchctl kickstart -k gui/501/com.remohop.host",
	} {
		if !strings.Contains(all, w) {
			t.Errorf("missing %q in\n%s", w, all)
		}
	}
	st, err := b.Status(ctx, Options{})
	if err != nil || !st.Running || st.PID != 99 {
		t.Errorf("status %+v %v", st, err)
	}
	if err := b.Control(ctx, Options{}, "stop"); err != nil {
		t.Error(err)
	}
	if removed, err := b.Uninstall(ctx, Options{}); err != nil || !removed {
		t.Errorf("uninstall %v %v", removed, err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist remains")
	}
	if _, err := (&launchd{home: t.TempDir(), euid: 0}).Install(ctx, Options{Scope: ScopeUser, Exe: "/x/remohop"}); err == nil {
		t.Error("root --user accepted on macOS")
	}
}
