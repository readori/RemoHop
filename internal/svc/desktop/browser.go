package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

const (
	// handoffLife is how long the redirect file stays on disk after the
	// browser was started; a cold browser may take a while to read it.
	handoffLife = 30 * time.Second
	// minGrace is the least time the file survives after the opener started.
	minGrace = 6 * time.Second
	// staleHandoff is the age after which a leftover redirect file (from a
	// process that was killed) is swept.
	staleHandoff = 5 * time.Minute
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
	dir, err := config.SubDir("run")
	if err != nil {
		env.Errorf("desktop: opening browser: %v; open the URL above manually", err)
		return
	}
	sweepHandoffs(dir)
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		env.Errorf("desktop: opening browser: %v; open the URL above manually", err)
		return
	}
	path := filepath.Join(dir, "open-"+hex.EncodeToString(rnd[:])+".html")
	// O_EXCL: never write through a pre-planted file or link.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		env.Errorf("desktop: opening browser: %v; open the URL above manually", err)
		return
	}
	_, werr := f.WriteString(redirectPage(url))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	remove := func() { os.Remove(path) }
	if werr != nil {
		remove()
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

// redirectPage is a self-contained page that forwards to url.
func redirectPage(url string) string {
	js, _ := json.Marshal(url) // escapes <, > and & as well
	return `<!doctype html><html><head><meta charset="utf-8"><title>RemoHop</title>` +
		`<meta http-equiv="refresh" content="0;url=` + html.EscapeString(url) + `">` +
		`<script>location.replace(` + string(js) + `)</script></head>` +
		`<body>Opening the remote desktop viewer...</body></html>`
}

// sweepHandoffs removes redirect files left behind by killed processes.
func sweepHandoffs(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "open-") || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleHandoff {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
