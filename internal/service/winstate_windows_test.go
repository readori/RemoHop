package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func needElevation(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated test process (making the Administrators an owner)")
	}
}

func ownerPath(t *testing.T, p string) *windows.SID {
	t.Helper()
	h, _, err := openNoFollow(p, windows.READ_CONTROL)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	o, err := ownerOf(h)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPrepareStateDir(t *testing.T) {
	needElevation(t)
	base := t.TempDir()

	fresh := filepath.Join(base, "fresh")
	if err := prepareStateDir(fresh); err != nil {
		t.Fatal(err)
	}
	if o := ownerPath(t, fresh); !o.Equals(adminsSID) {
		t.Errorf("owner of a new state directory: %s", accountName(o))
	}

	// One the installing administrator made, with a file in it: adopted,
	// and everything in it handed to the Administrators.
	mine := filepath.Join(base, "mine")
	os.MkdirAll(filepath.Join(mine, "sub"), 0o700)
	os.WriteFile(filepath.Join(mine, "sub", "config.json"), []byte("{}"), 0o600)
	if err := prepareStateDir(mine); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mine, filepath.Join(mine, "sub"), filepath.Join(mine, "sub", "config.json")} {
		if o := ownerPath(t, p); !o.Equals(adminsSID) {
			t.Errorf("owner of %s: %s", p, accountName(o))
		}
	}
	if ok, err := adoptStateDir(mine); err != nil || ok != "" {
		t.Errorf("adopting a prepared directory: note %q err %v", ok, err)
	}

	// A junction inside: refused, and its target left alone.
	target := filepath.Join(base, "elsewhere")
	os.MkdirAll(target, 0o755)
	planted := filepath.Join(base, "planted")
	os.MkdirAll(planted, 0o700)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(planted, "j"), target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	before := ownerPath(t, target)
	err := prepareStateDir(planted)
	if err == nil || !strings.Contains(err.Error(), "link or junction") {
		t.Errorf("a state directory with a junction: %v", err)
	}
	if after := ownerPath(t, target); !after.Equals(before) {
		t.Error("the junction's target was changed")
	}
}

func TestInstallBinaryCopies(t *testing.T) {
	pf := t.TempDir()
	t.Setenv("ProgramFiles", pf)
	src := filepath.Join(t.TempDir(), "remohop.exe")
	os.WriteFile(src, []byte("one"), 0o755)
	exe, copied, err := installBinary(src, "RemoHop")
	if err != nil || !copied || exe != filepath.Join(pf, "RemoHop", "remohop.exe") {
		t.Fatalf("installBinary: %q %v %v", exe, copied, err)
	}
	os.WriteFile(src, []byte("two"), 0o755)
	if exe2, _, err := installBinary(src, "RemoHop"); err != nil || exe2 != exe {
		t.Fatalf("second install: %q %v", exe2, err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "two" {
		t.Errorf("not replaced: %q", b)
	}
	if named, _, _ := installBinary(src, "lab"); filepath.Base(named) != "remohop-lab.exe" {
		t.Errorf("named instance: %s", named)
	}
	// Already under Program Files: used where it is.
	if again, copied, err := installBinary(exe, "RemoHop"); err != nil || copied || again != exe {
		t.Errorf("binary already in Program Files: %q %v %v", again, copied, err)
	}
	removeInstalledBinary(exe)
	if _, err := os.Stat(exe); !os.IsNotExist(err) {
		t.Errorf("copy left after uninstall: %v", err)
	}
	if got := commandLine(`C:\Program Files\RemoHop\remohop.exe`, []string{"service", "run", "--config-dir", `C:\a b`}); got !=
		`"C:\Program Files\RemoHop\remohop.exe" service run --config-dir "C:\a b"` {
		t.Errorf("commandLine: %s", got)
	}
}
