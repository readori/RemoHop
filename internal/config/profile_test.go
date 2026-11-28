package config

import (
	"path/filepath"
	"testing"
)

func TestInProfile(t *testing.T) {
	home := filepath.Join(t.TempDir(), "Users", "Alice")
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{home, true},
		{filepath.Join(home, "AppData", "Roaming", "remohop"), true},
		{filepath.Join(filepath.Dir(home), "ALICE", "x"), true}, // compared without regard to case
		{filepath.Join(filepath.Dir(home), "Alice2", "remohop"), false},
		{filepath.Join(filepath.Dir(filepath.Dir(home)), "ProgramData", "RemoHop"), false},
		{"", false},
	} {
		if got := inProfile(c.dir, home); got != c.want {
			t.Errorf("inProfile(%q) = %v, want %v", c.dir, got, c.want)
		}
	}
	if inProfile(home, "") {
		t.Error("an empty profile contains something")
	}
}
