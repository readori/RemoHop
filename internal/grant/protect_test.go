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

func TestCheckWorkDir(t *testing.T) {
	base := t.TempDir()
	if runtime.GOOS == "darwin" {
		base, _ = filepath.EvalSymlinks(base)
	}
	src, out := filepath.Join(base, "src"), filepath.Join(base, "src", "out")
	other := filepath.Join(base, "other")
	for _, d := range []string{out, other} {
		os.MkdirAll(d, 0o755)
	}
	r := mustParse(t, []string{"shell:cmd=git", "files:write:" + out, "files:read:" + src})
	for _, d := range []string{out, filepath.Join(out, "x"), src, base} {
		if err := r.CheckWorkDir(d); err == nil {
			t.Errorf("work dir %s allowed", d)
		}
	}
	if err := r.CheckWorkDir(other); err != nil {
		t.Errorf("work dir %s: %v", other, err)
	}
	if runtime.GOOS != "windows" {
		os.Symlink(out, filepath.Join(other, "link"))
		if err := r.CheckWorkDir(filepath.Join(other, "link")); err == nil {
			t.Error("work dir through a link into the writable root allowed")
		}
	}
	// No allow-list: not this check's business. Write anywhere: nowhere.
	if err := mustParse(t, []string{"shell:run-only", "files:write:" + out}).CheckWorkDir(out); err != nil {
		t.Errorf("without an allow-list: %v", err)
	}
	if err := mustParse(t, []string{"shell:cmd=git", "files"}).CheckWorkDir(other); err == nil {
		t.Error("an allow-list with files allowed a work dir")
	}
}
