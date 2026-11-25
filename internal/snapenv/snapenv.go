// Package snapenv adapts RemoHop to running from its snap package
// (packaging/snap). The snap uses classic confinement, so the program sees
// the system as any other install does; two things still differ, and both
// are handled here:
//
//   - `snap run` adds SNAP, SNAP_NAME, SNAP_REVISION and a dozen other
//     SNAP_* variables to the environment. Every process the host starts
//     inherits that environment (remote shells, xdg-open, screen capture
//     helpers), and programs that see SNAP believe they run inside a snap
//     themselves (GTK and Electron apps switch to portals, scripts change
//     paths). Setup removes them, so a remote shell gets the user's real
//     environment, as with the other packages.
//   - The program lives in /snap/<name>/<revision>/, a directory that is
//     deleted a few refreshes later. StableExe turns such a path into
//     /snap/<name>/current/..., which snapd points at every new revision, for
//     files that outlive this process (the systemd unit of `remohop service
//     install`).
//
// Outside a snap both are no-ops, and so is everything on systems other than
// Linux, where snaps do not exist.
package snapenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	mu       sync.Mutex
	root     string // the revision directory as snapd names it ($SNAP, e.g. /snap/remohop/x1), or ""
	resolved string // root with symlinks resolved (/var/lib/snapd/snap/... on Fedora and Arch)
)

// Setup records whether this process runs from a snap and, if so, removes
// snapd's variables from its environment. Call it once, before anything
// starts a child process. A SNAP variable that does not name the directory
// this binary runs from (one leaked from another snap's terminal, say) is
// left alone.
func Setup() {
	if runtime.GOOS != "linux" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	r, rr := detect(exe, os.Getenv)
	if r == "" {
		return
	}
	mu.Lock()
	root, resolved = r, rr
	mu.Unlock()
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); isSnapVar(k) {
			os.Unsetenv(k)
		}
	}
}

// StableExe returns exe with the snap's revision directory replaced by its
// "current" link, when exe is inside the snap this process runs from;
// otherwise exe unchanged.
func StableExe(exe string) string {
	mu.Lock()
	r, rr := root, resolved
	mu.Unlock()
	return stable(exe, r, rr)
}

// detect returns $SNAP and its resolved form when exe lies inside it.
func detect(exe string, getenv func(string) string) (snapRoot, snapResolved string) {
	r := getenv("SNAP")
	if r == "" || !filepath.IsAbs(r) || getenv("SNAP_REVISION") == "" {
		return "", ""
	}
	r = filepath.Clean(r)
	rr := r
	if e, err := filepath.EvalSymlinks(r); err == nil {
		rr = e
	}
	if _, ok := within(exe, r); ok {
		return r, rr
	}
	if _, ok := within(exe, rr); ok {
		return r, rr
	}
	return "", ""
}

func stable(exe, r, rr string) string {
	if r == "" {
		return exe
	}
	rel, ok := within(exe, r)
	if !ok {
		if rel, ok = within(exe, rr); !ok {
			return exe
		}
	}
	return filepath.Join(filepath.Dir(r), "current", rel)
}

// within returns path relative to dir when path is strictly inside dir.
func within(path, dir string) (string, bool) {
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func isSnapVar(k string) bool { return k == "SNAP" || strings.HasPrefix(k, "SNAP_") }
