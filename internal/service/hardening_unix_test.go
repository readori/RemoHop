//go:build !windows

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUntrustedExe(t *testing.T) {
	root := map[uint32]bool{0: true}
	// /bin/sh and everything on its way belong to root and nobody else may
	// write there, on every Unix this runs on.
	if why := untrustedExe("/bin/sh", root); why != "" {
		t.Errorf("/bin/sh refused: %s", why)
	}
	// A binary in a temporary directory: /tmp is writable by everybody, and
	// when not root the directory belongs to the test's user.
	exe := filepath.Join(t.TempDir(), "remohop")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	if why := untrustedExe(exe, root); why == "" {
		t.Errorf("%s accepted for a root service", exe)
	}
	// A symlink in a root-owned directory is judged by its target too.
	if why := untrustedExe(exe+"-missing", root); !strings.Contains(why, "cannot check") {
		t.Errorf("missing binary: %q", why)
	}
}

func TestCheckSystemExeMessage(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "remohop")
	os.WriteFile(exe, []byte("x"), 0o755)
	err := newSystemd().checkExe(exe, Options{})
	if err == nil || !strings.Contains(err.Error(), "as root") || !strings.Contains(err.Error(), "sudo install -m 755 ") ||
		!strings.Contains(err.Error(), "/usr/local/bin/remohop service install --system") {
		t.Errorf("systemd: %v", err)
	}
	err = newLaunchd().checkExe(exe, Options{})
	if err == nil || !strings.Contains(err.Error(), "/Library/RemoHop/remohop") {
		t.Errorf("launchd: %v", err)
	}
	if err := newSystemd().checkExe("/bin/sh", Options{}); err != nil {
		t.Errorf("/bin/sh: %v", err)
	}
}

func TestSystemInstallRefusesUserWritableExe(t *testing.T) {
	log := fakeBin(t, "systemctl")
	b := newTestSystemd(t, 0)
	b.checkExe = newSystemd().checkExe
	exe := filepath.Join(t.TempDir(), "remohop")
	os.WriteFile(exe, []byte("x"), 0o755)
	if _, err := b.Install(context.Background(), Options{Exe: exe}); err == nil {
		t.Fatal("system unit for a user-writable binary was installed")
	}
	if _, err := os.Stat(filepath.Join(b.systemDir, "remohop.service")); err == nil {
		t.Error("unit written although refused")
	}
	if b, _ := os.ReadFile(log); len(b) != 0 {
		t.Errorf("systemctl called: %s", b)
	}
	// A user service runs with the user's own rights: no check.
	b.euid = 1000
	if _, err := b.Install(context.Background(), Options{Exe: exe}); err != nil {
		t.Errorf("user unit: %v", err)
	}
}

func TestInvokedAs(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "Cellar", "remohop", "1.0.0", "bin", "remohop")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("x"), 0o755)
	link := filepath.Join(dir, "bin", "remohop")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink("../Cellar/remohop/1.0.0/bin/remohop", link); err != nil {
		t.Fatal(err)
	}
	if got := invokedAs(real, link); got != link {
		t.Errorf("invoked through the stable link: got %q", got)
	}
	t.Setenv("PATH", filepath.Dir(link))
	if got := invokedAs(real, "remohop"); got != link {
		t.Errorf("found on PATH: got %q", got)
	}
	other := filepath.Join(dir, "other")
	os.WriteFile(other, []byte("y"), 0o755)
	if got := invokedAs(real, other); got != "" {
		t.Errorf("another file: got %q", got)
	}
	if got := invokedAs(real, ""); got != "" {
		t.Errorf("empty argv[0]: got %q", got)
	}

	// The unit then names the link, which a package upgrade keeps.
	old := arg0
	arg0 = func() string { return link }
	t.Cleanup(func() { arg0 = old })
	exe, _ := os.Executable()
	if p, err := (Options{}).exePath(); err != nil || (p != exe && p != link) {
		t.Errorf("exePath: %q %v", p, err)
	}
}

func TestPrepareLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "remohop.log")
	if err := prepareLog(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("log %v %v", fi.Mode(), err)
	}
	// An existing, readable log is made private; a large one moves aside.
	os.Chmod(p, 0o644)
	if err := os.WriteFile(p, make([]byte, maxLaunchdLog+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareLog(p); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); fi.Size() != 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("after rotation: size %d mode %v", fi.Size(), fi.Mode())
	}
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != maxLaunchdLog+1 || fi.Mode().Perm() != 0o600 {
		t.Errorf("rotated file: %v %v", fi, err)
	}
	// A symlink planted in place of the log is not followed.
	os.Remove(p)
	os.Symlink("/etc/passwd", p)
	if err := prepareLog(p); err == nil {
		t.Error("followed a symlink")
	}
}

func TestLaunchdSystemHardening(t *testing.T) {
	log := fakeBin(t, "launchctl")
	t.Setenv("FAKE_PRINT", filepath.Join(t.TempDir(), "none")) // print fails: nothing loaded
	b := &launchd{home: t.TempDir(), euid: 0, daemonDir: t.TempDir(), logDir: t.TempDir(),
		trustedDir: func(d string) bool { return d == "/usr/local/bin" }}
	plist := b.text(ScopeSystem, Options{}, "/Library/RemoHop/remohop")
	if !strings.Contains(plist, "<string>/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin</string>") {
		t.Errorf("system PATH keeps a directory others can write:\n%s", plist)
	}
	if user := b.text(ScopeUser, Options{}, "/x"); !strings.Contains(user, "/opt/homebrew/bin") {
		t.Error("user agent lost Homebrew on PATH")
	}

	b.checkExe = newLaunchd().checkExe
	exe := filepath.Join(t.TempDir(), "remohop")
	os.WriteFile(exe, []byte("x"), 0o755)
	if _, err := b.Install(context.Background(), Options{Exe: exe}); err == nil {
		t.Fatal("LaunchDaemon for a user-writable binary was installed")
	}
	b.checkExe = nil
	if _, err := b.Install(context.Background(), Options{Exe: "/Library/RemoHop/remohop", ConfigDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(b.logPath(ScopeSystem, Options{})); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("daemon log not created private: %v %v", fi, err)
	}
	c := calls(t, log)
	var order []string
	for _, l := range c {
		if f := strings.Fields(l); len(f) > 1 && (f[1] == "enable" || f[1] == "bootstrap") {
			order = append(order, f[1])
		}
	}
	if strings.Join(order, ",") != "enable,bootstrap" {
		t.Errorf("enable must come before bootstrap: %v", c)
	}
}

func TestSystemdPackagedUnit(t *testing.T) {
	log := fakeBin(t, "systemctl", "journalctl")
	vendor := t.TempDir()
	unit := filepath.Join(vendor, "remohop.service")
	os.WriteFile(unit, []byte("[Service]\nExecStart=/usr/bin/remohop host\n"), 0o644)
	show := filepath.Join(t.TempDir(), "show")
	t.Setenv("FAKE_SHOW", show)
	b := newTestSystemd(t, 0)
	b.vendorDirs = map[Scope][]string{ScopeSystem: {vendor}}

	// Shipped but not enabled: not installed.
	os.WriteFile(show, []byte("UnitFileState=disabled\nActiveState=inactive\nFragmentPath="+unit+"\n"), 0o600)
	if _, found := b.detect(Options{}); found {
		t.Error("a disabled packaged unit counts as installed")
	}
	// Enabled with systemctl, as the package's message says.
	os.WriteFile(show, []byte("UnitFileState=enabled\nActiveState=active\nSubState=running\nMainPID=42\nFragmentPath="+unit+"\n"), 0o600)
	st, err := b.Status(context.Background(), Options{})
	if err != nil || !st.Installed || st.Path != unit || st.Scope != "system" || st.ConfigDir != "/var/lib/remohop" {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := b.Control(context.Background(), Options{}, "restart"); err != nil {
		t.Error(err)
	}
	removed, err := b.Uninstall(context.Background(), Options{})
	if !removed || !errors.Is(err, errPackagedUnit) {
		t.Errorf("uninstall: %v %v", removed, err)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Error("the package's unit file was deleted")
	}
	all := strings.Join(calls(t, log), "\n")
	if !strings.Contains(all, "systemctl restart remohop.service") || !strings.Contains(all, "systemctl disable --now remohop.service") {
		t.Errorf("calls:\n%s", all)
	}
	// An own unit in /etc shadows the packaged one.
	inst, err := b.Install(context.Background(), Options{Exe: "/usr/bin/remohop"})
	if err != nil || len(inst.Notes) == 0 || !strings.Contains(inst.Notes[0], "takes the place of the remohop package's") {
		t.Errorf("install over a packaged unit: %+v %v", inst, err)
	}
}

func TestStatusReportsMissingBinary(t *testing.T) {
	for in, want := range map[string]string{
		"[Service]\nExecStart=/usr/bin/remohop host\n":         "/usr/bin/remohop",
		"ExecStart=\"/opt/a b/remohop\" host --signal x\n":     "/opt/a b/remohop",
		"ExecStart=\"/x/100%%/$$HOME/re\\\"mo\\\\hop\" host\n": `/x/100%/$HOME/re"mo\hop`,
		"ExecStart=-/usr/bin/remohop host\n":                   "/usr/bin/remohop",
		"[Unit]\nDescription=no exec\n":                        "",
	} {
		if got := execStartBinary(in); got != want {
			t.Errorf("execStartBinary(%q) = %q, want %q", in, got, want)
		}
	}
	// Round trip with the unit writer.
	exe := `/opt/odd "dir"/100%/$x/remohop`
	if got := execStartBinary((&systemd{}).text(ScopeUser, Options{}, exe)); got != exe {
		t.Errorf("round trip: %q", got)
	}
	if got := plistProgram((&launchd{home: "/Users/a"}).text(ScopeUser, Options{}, "/opt/a&b/remohop")); got != "/opt/a&b/remohop" {
		t.Errorf("plistProgram: %q", got)
	}

	fakeBin(t, "systemctl")
	show := filepath.Join(t.TempDir(), "show")
	os.WriteFile(show, []byte("ActiveState=activating\nSubState=auto-restart\n"), 0o600)
	t.Setenv("FAKE_SHOW", show)
	b := newTestSystemd(t, 1000)
	gone := filepath.Join(t.TempDir(), "remohop")
	if _, err := b.Install(context.Background(), Options{Exe: gone}); err != nil {
		t.Fatal(err)
	}
	st, err := b.Status(context.Background(), Options{})
	if err != nil || st.Exe != gone || !st.ExeMissing {
		t.Fatalf("status %+v %v", st, err)
	}
	var out strings.Builder
	printStatus(&out, st, false)
	if !strings.Contains(out.String(), "does not exist") {
		t.Errorf("status text:\n%s", out.String())
	}
}
