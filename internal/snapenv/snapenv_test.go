package snapenv

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	snap := map[string]string{"SNAP": "/snap/remohop/x1", "SNAP_REVISION": "x1"}
	for _, c := range []struct {
		exe  string
		env  map[string]string
		want string
	}{
		{"/snap/remohop/x1/bin/remohop", snap, "/snap/remohop/x1"},
		{"/snap/remohop/x1/bin/remohop", map[string]string{"SNAP": "/snap/remohop/x1/", "SNAP_REVISION": "x1"}, "/snap/remohop/x1"},
		// Not from a snap, or SNAP leaked from another snap's terminal.
		{"/usr/bin/remohop", nil, ""},
		{"/usr/bin/remohop", map[string]string{"SNAP": "/snap/code/180", "SNAP_REVISION": "180"}, ""},
		{"/snap/remohop/x10/bin/remohop", snap, ""},
		{"/snap/remohop/x1", snap, ""},
		{"/snap/remohop/x1/bin/remohop", map[string]string{"SNAP": "/snap/remohop/x1"}, ""},
		{"/snap/remohop/x1/bin/remohop", map[string]string{"SNAP": "snap/remohop/x1", "SNAP_REVISION": "x1"}, ""},
	} {
		if got, _ := detect(c.exe, env(c.env)); got != c.want {
			t.Errorf("detect(%q, %v) = %q, want %q", c.exe, c.env, got, c.want)
		}
	}
}

// On Fedora and Arch /snap is a link to /var/lib/snapd/snap: $SNAP keeps the
// /snap spelling while os.Executable reports the resolved path.
func TestDetectResolvesLinks(t *testing.T) {
	dir := t.TempDir()
	realRev := filepath.Join(dir, "var", "lib", "snapd", "snap", "remohop", "7")
	if err := os.MkdirAll(filepath.Join(realRev, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "snap")
	if err := os.Symlink(filepath.Join(dir, "var", "lib", "snapd", "snap"), link); err != nil {
		t.Skip("no symlinks:", err)
	}
	r, rr := detect(filepath.Join(realRev, "bin", "remohop"), env(map[string]string{"SNAP": filepath.Join(link, "remohop", "7"), "SNAP_REVISION": "7"}))
	if r != filepath.Join(link, "remohop", "7") {
		t.Fatalf("detect = %q, %q", r, rr)
	}
	if got, want := stable(filepath.Join(realRev, "bin", "remohop"), r, rr), filepath.Join(link, "remohop", "current", "bin", "remohop"); got != want {
		t.Errorf("stable = %q, want %q", got, want)
	}
}

func TestStable(t *testing.T) {
	for _, c := range []struct{ exe, root, want string }{
		{"/snap/remohop/x1/bin/remohop", "/snap/remohop/x1", "/snap/remohop/current/bin/remohop"},
		{"/snap/remohop_beta/23/bin/remohop", "/snap/remohop_beta/23", "/snap/remohop_beta/current/bin/remohop"},
		{"/usr/local/bin/remohop", "/snap/remohop/x1", "/usr/local/bin/remohop"},
		{"/snap/remohop/x1/bin/remohop", "", "/snap/remohop/x1/bin/remohop"},
	} {
		if got := stable(c.exe, c.root, c.root); got != c.want {
			t.Errorf("stable(%q, %q) = %q, want %q", c.exe, c.root, got, c.want)
		}
	}
}

func TestIsSnapVar(t *testing.T) {
	for k, want := range map[string]bool{"SNAP": true, "SNAP_NAME": true, "SNAP_SAVED_TMPDIR": true, "SNAPSHOT": false, "XSNAP": false, "HOME": false} {
		if isSnapVar(k) != want {
			t.Errorf("isSnapVar(%q) = %v", k, !want)
		}
	}
}
