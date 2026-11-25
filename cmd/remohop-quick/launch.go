package main

import (
	"path/filepath"
	"strings"
)

// One program, two double-click products, told apart by the file's name so
// that the person being helped never needs a flag:
//
//	RemoHop.exe, RemoHop.app, remohop-app                 the desktop app (`remohop app`)
//	RemoHop-QuickSupport.exe, "RemoHop Quick Support.app"
//	(Contents/MacOS/RemoHop-QuickSupport), remohop-quick  quick support (`remohop quick`)
//
// A copy keeps its signature when it is renamed (Windows Authenticode and
// the macOS bundle's code signature do not cover the file name), so the
// release ships the same signed program under both names.

// quickName reports whether the program was started under a quick-support
// name: its file name, without .exe, contains "quick".
func quickName(argv0 string) bool {
	base := strings.ToLower(filepath.Base(strings.ReplaceAll(argv0, `\`, "/")))
	base = strings.TrimSuffix(base, ".exe")
	return strings.Contains(base, "quick")
}

// launchArgs turns the arguments of a double-click into `app` or `quick`
// (by the program's name), and a lone --quick or --app into that mode.
// Finder on older macOS versions passes a process serial number
// (-psn_0_12345).
func launchArgs(argv0 string, args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	switch {
	case len(out) == 0 && quickName(argv0):
		return []string{"quick"}
	case len(out) == 0:
		return []string{"app"}
	case out[0] == "--quick":
		return append([]string{"quick"}, out[1:]...)
	case out[0] == "--app":
		return append([]string{"app"}, out[1:]...)
	}
	return out
}
