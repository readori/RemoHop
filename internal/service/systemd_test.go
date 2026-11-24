package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBin puts scripts named after the given commands first on PATH. Each
// appends its arguments to $FAKE_LOG; `show` prints $FAKE_SHOW. extra maps a
// command name to additional shell run before logging.
func fakeBin(t *testing.T, cmds ...string) (logPath string) {
	t.Helper()
	skipNoShellScripts(t)
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	for _, c := range cmds {
		script := fmt.Sprintf(`#!/bin/sh
echo "%s $*" >> "$FAKE_LOG"
case "$*" in
*" show "*|"show "*) cat "$FAKE_SHOW" ;;
*"show-user"*) echo "Linger=${FAKE_LINGER:-no}" ;;
*" print "*|"print "*) [ -n "$FAKE_PRINT" ] && cat "$FAKE_PRINT" || exit 113 ;;
esac
case "%s" in journalctl) printf 'line one\n\033]0;pwn\007line two\n' ;; esac
exit 0
`, c, c)
		if err := os.WriteFile(filepath.Join(dir, c), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", logPath)
	return logPath
}

// skipNoShellScripts skips tests that stand in for systemctl/launchctl with
// "#!/bin/sh" scripts on PATH.
func skipNoShellScripts(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs fake unix tools as #!/bin/sh scripts on PATH, which Windows cannot execute")
	}
}

func calls(t *testing.T, p string) []string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func newTestSystemd(t *testing.T, euid int) *systemd {
	return &systemd{home: t.TempDir(), systemDir: t.TempDir(), euid: euid, booted: func() bool { return true }}
}

func TestSystemdUserLifecycle(t *testing.T) {
	log := fakeBin(t, "systemctl", "loginctl", "journalctl")
	show := filepath.Join(t.TempDir(), "show")
	os.WriteFile(show, []byte("ActiveState=active\nSubState=running\nMainPID=777\nUnitFileState=enabled\nActiveEnterTimestamp=Thu 2026-11-19 10:00:00 UTC\n"), 0o600)
	t.Setenv("FAKE_SHOW", show)
	b := newTestSystemd(t, 1000)
	ctx := context.Background()
	o := Options{Exe: "/usr/bin/remohop", Signal: "rv.example:1"}

	inst, err := b.Install(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(b.home, ".config/systemd/user/remohop.service")
	if inst.Path != unit || inst.Scope != ScopeUser {
		t.Errorf("installed %+v", inst)
	}
	got, _ := os.ReadFile(unit)
	if !strings.Contains(string(got), "ExecStart=/usr/bin/remohop host --signal rv.example:1") {
		t.Errorf("unit:\n%s", got)
	}
	if fi, _ := os.Stat(unit); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
	if len(inst.Notes) != 1 || !strings.Contains(inst.Notes[0], "enable-linger") {
		t.Errorf("notes %v", inst.Notes)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable remohop.service",
		"systemctl --user restart remohop.service",
	}
	for i, w := range want {
		if c := calls(t, log); i >= len(c) || c[i] != w {
			t.Fatalf("calls %q, want prefix %q", c, want)
		}
	}

	st, err := b.Status(ctx, Options{})
	if err != nil || !st.Installed || !st.Running || !st.Enabled || st.PID != 777 || st.Scope != "user" {
		t.Errorf("status %+v %v", st, err)
	}
	if err := b.Control(ctx, Options{}, "stop"); err != nil {
		t.Error(err)
	}
	var buf strings.Builder
	if err := b.Logs(ctx, Options{}, 20, true, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\x1b") || !strings.Contains(buf.String(), "line two") {
		t.Errorf("logs %q", buf.String())
	}
	removed, err := b.Uninstall(ctx, Options{})
	if err != nil || !removed {
		t.Fatalf("uninstall %v %v", removed, err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Error("unit not removed")
	}
	all := strings.Join(calls(t, log), "\n")
	for _, w := range []string{
		"systemctl --user stop remohop.service",
		"systemctl --user disable --now remohop.service",
		"journalctl --user -u remohop.service -n 20 --no-pager -o short-iso -f",
	} {
		if !strings.Contains(all, w) {
			t.Errorf("missing call %q in\n%s", w, all)
		}
	}
}

func TestSystemdLinger(t *testing.T) {
	log := fakeBin(t, "systemctl", "loginctl")
	b := newTestSystemd(t, 1000)
	inst, err := b.Install(context.Background(), Options{Exe: "/usr/bin/remohop", Linger: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls(t, log), "\n"), "loginctl enable-linger") || !strings.Contains(inst.Notes[0], "linger enabled") {
		t.Errorf("calls %v notes %v", calls(t, log), inst.Notes)
	}

	t.Setenv("FAKE_LINGER", "yes")
	inst, err = b.Install(context.Background(), Options{Exe: "/usr/bin/remohop"})
	if err != nil || len(inst.Notes) != 0 {
		t.Errorf("linger already on: notes %v err %v", inst.Notes, err)
	}
}

func TestSystemdSystemInstall(t *testing.T) {
	log := fakeBin(t, "systemctl")
	b := newTestSystemd(t, 0)
	inst, err := b.Install(context.Background(), Options{Exe: "/usr/bin/remohop", RunAs: "remohop"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Scope != ScopeSystem || inst.ConfigDir != "/var/lib/remohop" || inst.RunAs != "remohop" || inst.Path != filepath.Join(b.systemDir, "remohop.service") {
		t.Errorf("%+v", inst)
	}
	if c := calls(t, log); c[0] != "systemctl daemon-reload" || c[2] != "systemctl restart remohop.service" {
		t.Errorf("calls %v", c)
	}
	// Control and detection find the system unit when no user unit exists.
	if err := b.Control(context.Background(), Options{}, "restart"); err != nil {
		t.Error(err)
	}
}

func TestSystemdRefusals(t *testing.T) {
	fakeBin(t, "systemctl")
	ctx := context.Background()
	if _, err := newTestSystemd(t, 1000).Install(ctx, Options{Scope: ScopeSystem, Exe: "/x/remohop"}); err == nil || !strings.Contains(err.Error(), "needs root") {
		t.Errorf("non-root --system: %v", err)
	}
	if _, err := newTestSystemd(t, 1000).Install(ctx, Options{Scope: ScopeUser, RunAs: "x", Exe: "/x/remohop"}); err == nil {
		t.Error("--run-as with --user accepted")
	}
	nb := newTestSystemd(t, 1000)
	nb.booted = func() bool { return false }
	if _, err := nb.Install(ctx, Options{Exe: "/x/remohop"}); err == nil || !strings.Contains(err.Error(), "systemd is not running") || !strings.Contains(err.Error(), "remohop service print") {
		t.Errorf("no systemd: %v", err)
	}
	// A system unit seen by a non-root user cannot be managed.
	sb := newTestSystemd(t, 1000)
	os.WriteFile(filepath.Join(sb.systemDir, "remohop.service"), []byte("x"), 0o644)
	if err := sb.Control(ctx, Options{}, "stop"); err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Errorf("non-root control of system unit: %v", err)
	}
}

func TestSystemctlFailureHasHint(t *testing.T) {
	skipNoShellScripts(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\necho 'Failed to connect to bus: No medium found' >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	b := newTestSystemd(t, 1000)
	_, err := b.Install(context.Background(), Options{Exe: "/x/remohop"})
	if err == nil || !strings.Contains(err.Error(), "no systemd user session") || !strings.Contains(err.Error(), "unit file was written") {
		t.Errorf("%v", err)
	}
}

// TestRealSystemdUser exercises a real user manager when the machine has
// one; containers normally do not.
func TestRealSystemdUser(t *testing.T) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		t.Skip("no systemctl")
	}
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("no systemd user manager in this environment")
	}
	b := newSystemd()
	if !b.booted() {
		t.Skip("systemd is not the running init")
	}
	script := filepath.Join(t.TempDir(), "remohop")
	os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 300\n"), 0o755)
	o := Options{Scope: ScopeUser, Name: fmt.Sprintf("remohop-test-%d", os.Getpid()), Exe: script}
	ctx := context.Background()
	t.Cleanup(func() { b.Uninstall(ctx, o) })
	if _, err := b.Install(ctx, o); err != nil {
		t.Fatal(err)
	}
	st, err := b.Status(ctx, o)
	if err != nil || !st.Running {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := b.Control(ctx, o, "stop"); err != nil {
		t.Fatal(err)
	}
}
