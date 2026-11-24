// Package handoff gives a browser a URL that carries an access token
// without putting the token on a command line.
//
// The local pages of `remohop web`, quick support and the desktop viewer
// are reached through URLs that contain an access token. The argument list
// of the program that opens a browser (xdg-open, open, the browser itself)
// can be read by every local user on Unix, so the browser is pointed at a
// private file (mode 0600 in a 0700 directory) that only redirects to the
// URL, and the file is removed soon after.
//
// The file must be somewhere the browser can read. Browsers packaged as a
// Snap or a Flatpak (Firefox and Chromium on Ubuntu are Snaps) run in a
// sandbox that cannot read hidden directories under the home directory,
// such as ~/.config, where RemoHop keeps its state. For those, the file goes
// into the sandbox's own private directory instead (see Dir).
package handoff

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// prefix starts the name of every handoff file, so Sweep can find stale ones.
const prefix = "open-"

// stale is how old a leftover handoff file must be before Sweep removes it.
const stale = 10 * time.Minute

// Page is a self-contained HTML page that forwards to url, showing text
// while it does.
func Page(url, text string) string {
	js, _ := json.Marshal(url) // escapes <, > and & as well
	return `<!doctype html><html><head><meta charset="utf-8"><title>RemoHop</title>` +
		`<meta http-equiv="refresh" content="0;url=` + html.EscapeString(url) + `">` +
		`<script>location.replace(` + string(js) + `)</script></head>` +
		`<body>` + html.EscapeString(text) + `</body></html>`
}

// Write creates a new handoff file in dir holding page and returns its path
// and a function that removes it. kind is part of the file name ("web",
// "quick", "desktop"), for people who look at the directory.
func Write(dir, kind, page string) (path string, remove func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", nil, err
	}
	name := prefix + kind + "-" + hex.EncodeToString(rnd[:]) + ".html"
	path = filepath.Join(dir, name)
	// O_EXCL: never write through a pre-planted file or link.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	_, werr := f.WriteString(page)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return "", nil, werr
	}
	return path, func() { os.Remove(path) }, nil
}

// Sweep removes handoff files in dir that a crashed or killed process left
// behind. Only regular files named like handoff files and older than a few
// minutes are touched.
func Sweep(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".html") || !e.Type().IsRegular() {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > stale {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// defaultDir is where handoff files go for browsers without a sandbox.
func defaultDir() (string, error) { return config.SubDir("run") }

// Dir is the directory for a handoff file that browser will open. browser
// is the path of the browser program, or empty for the user's default
// browser (opened through xdg-open, open or the shell). It is the RemoHop
// run directory, except for sandboxed (Snap or Flatpak) browsers on Linux,
// which get a directory inside their sandbox.
//
// REMOHOP_HANDOFF_DIR overrides the choice (for unusual sandboxes, and for
// tests that must not look at the real desktop's browser).
func Dir(browser string) (string, error) {
	if d := os.Getenv("REMOHOP_HANDOFF_DIR"); d != "" {
		return d, nil
	}
	if d := sandboxDir(browser); d != "" {
		return d, nil
	}
	return defaultDir()
}
