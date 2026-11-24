package quick

import (
	"os"
	"path/filepath"
)

// systemAppBrowsers lists where Microsoft Edge lives. Edge is part of
// Windows 10 and 11, so its app mode gives RemoHop a window of its own
// without shipping a browser engine; where it has been removed, the page
// opens in the default browser.
func systemAppBrowsers() []string {
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			out = append(out, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	return out
}
