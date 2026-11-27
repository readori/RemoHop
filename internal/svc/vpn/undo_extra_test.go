package vpn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUndoRemove(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	keep := filepath.Join(t.TempDir(), "keep")
	gone := filepath.Join(t.TempDir(), "gone")
	for _, p := range []string{keep, gone} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removableFiles[gone] = true
	defer delete(removableFiles, gone)

	st, err := newState("test", "dev0")
	if err != nil {
		t.Fatal(err)
	}
	c := &Changes{st: st}
	if err := c.RecordRemove(keep); err == nil {
		t.Fatal("recorded the removal of a file that is not removable")
	}
	if err := c.RecordRemove(gone); err != nil {
		t.Fatal(err)
	}
	// A state file edited to name another file deletes nothing.
	st.record(undoStep{Remove: keep})
	st.record(undoStep{NRPT: "x'; Remove-Item C:\\ -Recurse; '"})
	c.Undo()
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Error("the removable file is still there")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("a file outside the list was removed")
	}
}

func TestNRPTRemoval(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	st, err := newState("test", "dev0")
	if err != nil {
		t.Fatal(err)
	}
	c := &Changes{st: st}
	for _, bad := range []string{"", "net", "remohop-", "remohop-net'; x", "REMOHOP-NET"} {
		if c.RecordNRPTRemoval(bad) == nil {
			t.Errorf("accepted comment %q", bad)
		}
	}
	if err := c.RecordNRPTRemoval("remohop-net"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(st.path)
	var saved state
	if json.Unmarshal(b, &saved) != nil || len(saved.Steps) != 1 || saved.Steps[0].NRPT != "remohop-net" {
		t.Fatalf("state file: %s", b)
	}
	cmd := NRPTRemoveCmd("remohop-net")
	if cmd[0] != "powershell.exe" || !strings.Contains(cmd[len(cmd)-1], "$_.Comment -eq 'remohop-net'") {
		t.Fatalf("%q", cmd)
	}
	if s := NRPTRemoveCmd("x'; evil")[len(cmd)-1]; strings.Contains(s, "evil") {
		t.Fatalf("an invalid comment reached the script: %s", s)
	}
}

func TestCommandTimeout(t *testing.T) {
	if commandTimeout("powershell.exe") < 30*time.Second || commandTimeout(`C:\Windows\System32\WindowsPowerShell\v1.0\PowerShell.exe`) < 30*time.Second {
		t.Error("PowerShell gets the short timeout")
	}
	if commandTimeout("netsh") != 10*time.Second || commandTimeout("ip") != 10*time.Second {
		t.Error("other tools' timeout changed")
	}
}
