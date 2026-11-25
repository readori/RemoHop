//go:build unix

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakePrivateUnix(t *testing.T) {
	base := t.TempDir()
	mode := func(p string) os.FileMode {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode() & (os.ModePerm | os.ModeSticky)
	}

	loose := filepath.Join(base, "loose")
	os.Mkdir(loose, 0o700)
	os.Chmod(loose, 0o777)
	if err := MakePrivate(loose); err != nil || mode(loose) != 0o700 {
		t.Errorf("world-writable own directory: err %v, mode %v", err, mode(loose))
	}

	readable := filepath.Join(base, "readable")
	os.Mkdir(readable, 0o755)
	os.Chmod(readable, 0o755)
	if err := MakePrivate(readable); err != nil || mode(readable) != 0o755 {
		t.Errorf("readable own directory: err %v, mode %v (left as it is)", err, mode(readable))
	}

	shared := filepath.Join(base, "shared")
	os.Mkdir(shared, 0o700)
	os.Chmod(shared, 0o777|os.ModeSticky)
	if err := MakePrivate(shared); err == nil || !strings.Contains(err.Error(), "every user") || mode(shared) != 0o777|os.ModeSticky {
		t.Errorf("sticky shared directory: err %v, mode %v (refused, not changed)", err, mode(shared))
	}

	// A state directory set up this way makes Dir tighten it too.
	t.Setenv("REMOHOP_CONFIG_DIR", loose)
	os.Chmod(loose, 0o770)
	if _, err := Dir(); err != nil || mode(loose) != 0o700 {
		t.Errorf("Dir on a group-writable directory: err %v, mode %v", err, mode(loose))
	}

	if os.Geteuid() == 0 {
		other := filepath.Join(base, "other")
		os.Mkdir(other, 0o700)
		os.Chown(other, 65534, 65534)
		if err := MakePrivate(other); err == nil || !strings.Contains(err.Error(), "another user") {
			t.Errorf("another user's directory: %v", err)
		}
	}
}
