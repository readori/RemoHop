//go:build windows

package grant

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func junction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
}

// Junctions are resolved like links by RealPath, so CheckPath sees where
// they lead (filepath.EvalSymlinks no longer follows them).
func TestCheckPathJunction(t *testing.T) {
	root, outside := scopeDirs(t)
	junction(t, filepath.Join(root, "esc"), outside)
	r := mustParse(t, []string{"files:write:" + root})
	for _, a := range []Access{Read, Write} {
		if err := r.CheckPath(filepath.Join(root, "esc", "x"), a); err == nil {
			t.Errorf("%v through a junction out of the root allowed", a)
		}
	}
	if got, err := RealPath(filepath.Join(root, "esc", "x")); err != nil || !within(got, outside) {
		t.Errorf("RealPath through a junction: %s %v", got, err)
	}
	os.Mkdir(filepath.Join(root, "sub"), 0o755)
	junction(t, filepath.Join(root, "inner"), filepath.Join(root, "sub"))
	if err := r.CheckPath(filepath.Join(root, "inner", "x"), Write); err != nil {
		t.Errorf("in-root junction: %v", err)
	}
}
