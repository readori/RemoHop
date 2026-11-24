package desktop

import (
	"errors"
	"os/exec"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/handoff"
)

const (
	// handoffLife is how long the redirect file stays on disk after the
	// browser was started; a cold browser may take a while to read it.
	handoffLife = 30 * time.Second
	// minGrace is the least time the file survives after the opener started.
	minGrace = 6 * time.Second
)

// browserCommand builds the platform command that opens target (a file
// path) in the default browser. A variable for tests.
var browserCommand = func(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		// Not `start`: cmd.exe would interpret characters in the target.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	}
	return exec.Command("xdg-open", target)
}

// openBrowser opens the viewer URL in the user's default browser, best
// effort. The URL carries the viewer's access token, and the opener and the
// browser it starts would show it in their argument lists to every local
// user, so the browser is pointed at a private temporary HTML file that only
// redirects to the URL. The returned function removes that file; it also
// runs on its own after handoffLife.
func openBrowser(env *cli.Env, url string) (cleanup func()) {
	cleanup = func() {}
	dir, err := handoff.Dir("")
	if err != nil {
		env.Errorf("desktop: opening browser: %v; open the URL above manually", err)
		return
	}
	handoff.Sweep(dir)
	path, remove, werr := handoff.Write(dir, "desktop", handoff.Page(url, "Opening the remote desktop viewer..."))
	if werr != nil {
		env.Errorf("desktop: opening browser: %v; open the URL above manually", werr)
		return
	}
	timer := time.AfterFunc(handoffLife, remove)
	removeNow := func() { timer.Stop(); remove() }
	cleanup = removeNow

	cmd := browserCommand(path)
	if err := cmd.Start(); err != nil {
		removeNow()
		if errors.Is(err, exec.ErrNotFound) {
			env.Errorf("desktop: could not open a browser; open the URL above manually")
		} else {
			env.Errorf("desktop: opening browser: %v; open the URL above manually", err)
		}
		return func() {}
	}
	go cmd.Wait()
	started := time.Now()
	// A short-lived caller (the -b parent) must not delete the file before
	// the browser has had a chance to read it; a long-running one has
	// waited far longer already.
	return func() {
		time.Sleep(max(0, minGrace-time.Since(started)))
		removeNow()
	}
}
