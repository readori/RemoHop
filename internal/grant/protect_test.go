package grant

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProtectedDir(t *testing.T) {
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		base, _ = filepath.EvalSymlinks(base)
	}
	cfg := filepath.Join(base, "home", ".config", "remohop")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	release := Protect(cfg)
	defer release()
	for _, r := range []*Rules{mustParse(t, []string{"files:write:" + home}), mustParse(t, []string{"files"})} {
		for _, a := range []Access{Meta, Read, Write} {
			if err := r.CheckPath(filepath.Join(cfg, "grants.json"), a); err == nil {
				t.Errorf("%v inside the config directory allowed", a)
			}
			if err := r.CheckPath(cfg, a); err == nil {
				t.Errorf("%v of the config directory allowed", a)
			}
		}
		// Above it: looking is fine, changing is not.
		if err := r.CheckPath(filepath.Join(home, ".config"), Read); err != nil {
			t.Errorf("reading a directory above: %v", err)
		}
		for _, p := range []string{filepath.Join(home, ".config"), home} {
			if err := r.CheckPath(p, Write); err == nil {
				t.Errorf("changing %s allowed", p)
			}
		}
		if err := r.CheckPath(filepath.Join(home, ".config", "other"), Write); err != nil {
			t.Errorf("a sibling: %v", err)
		}
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		r := mustParse(t, []string{"files"})
		if err := r.CheckPath(strings.ToUpper(filepath.Join(cfg, "grants.json")), Read); err == nil {
			t.Error("the config directory in other case allowed")
		}
	}
	if runtime.GOOS != "windows" {
		os.Symlink(cfg, filepath.Join(home, "link"))
		if err := mustParse(t, []string{"files:read"}).CheckPath(filepath.Join(home, "link", "host.json"), Read); err == nil {
			t.Error("through a link allowed")
		}
		for _, tc := range []struct{ link, target string }{
			{filepath.Join(home, "l1"), cfg},
			{filepath.Join(home, "l2"), "."},
			{filepath.Join(home, "l3"), ".config"},
			{filepath.Join(home, ".config", "l4"), "remohop/x"},
		} {
			if err := CheckLinkTarget(tc.link, tc.target); err == nil {
				t.Errorf("link %s -> %s allowed", tc.link, tc.target)
			}
		}
		if err := CheckLinkTarget(filepath.Join(home, "sub", "l"), "."); err != nil {
			t.Errorf("a harmless link: %v", err)
		}
	}
	release()
	if err := mustParse(t, []string{"files"}).CheckPath(filepath.Join(cfg, "grants.json"), Read); err != nil {
		t.Errorf("after release: %v", err)
	}
}
