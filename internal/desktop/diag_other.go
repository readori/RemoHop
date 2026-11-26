//go:build !windows && !darwin

package desktop

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func osVersion() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

// revealFile opens the file's folder.
func revealFile(path string) error {
	cmd := exec.Command("xdg-open", filepath.Dir(path))
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
