//go:build windows

package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cmd.exe looks for a program in the working directory before PATH: under
// a command allow-list, a planted whoami.bat would run in place of the
// real whoami. The host's shells get NoDefaultCurrentDirectoryInExePath.
func TestShellsSkipCurrentDirectory(t *testing.T) {
	for _, sh := range []string{"cmd.exe", "powershell.exe"} {
		if _, err := exec.LookPath(sh); err != nil {
			continue
		}
		t.Setenv("REMOHOP_SHELL", sh)
		env := buildEnv("", `C:\`, nil)
		if !slices.ContainsFunc(env, func(kv string) bool {
			return strings.EqualFold(kv, "NoDefaultCurrentDirectoryInExePath=1")
		}) {
			t.Errorf("%s: NoDefaultCurrentDirectoryInExePath not set", sh)
		}
	}

	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("no cmd.exe")
	}
	t.Setenv("REMOHOP_SHELL", "cmd.exe")
	dir := t.TempDir()
	marker := filepath.Join(dir, "planted-ran")
	if err := os.WriteFile(filepath.Join(dir, "whoami.bat"), []byte("@echo off\r\necho x> \""+marker+"\"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := commandCmd("whoami")
	cmd.Dir = dir
	cmd.Env = buildEnv("", dir, nil)
	cmd.Run()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("cmd.exe ran a program planted in the working directory")
	}
}
