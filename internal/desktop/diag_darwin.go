package desktop

import (
	"os/exec"
	"strings"
)

func osVersion() string {
	out, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return "macOS"
	}
	return "macOS " + strings.TrimSpace(string(out))
}

// revealFile shows the file in Finder.
func revealFile(path string) error { return exec.Command("/usr/bin/open", "-R", path).Start() }
