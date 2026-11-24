package quick

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// The size of the app-style window: the page is one narrow column.
const windowSize = "480,820"

// redirectFile writes a private page that sends the browser to target and
// returns its path. The UI's URL contains the access token, and the
// argument list of the program that opens it can be read by every local
// user on Unix; so the browser is pointed at this file instead (mode 0600
// in the config directory), as `remohop web` does.
func redirectFile(target, text string) (path string, remove func(), err error) {
	dir, err := config.SubDir("run")
	if err != nil {
		return "", nil, err
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", nil, err
	}
	path = filepath.Join(dir, "open-quick-"+hex.EncodeToString(rnd[:])+".html")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, err
	}
	js, _ := json.Marshal(target)
	_, werr := f.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>RemoHop</title>` +
		`<meta http-equiv="refresh" content="0;url=` + html.EscapeString(target) + `">` +
		`<script>location.replace(` + string(js) + `)</script></head><body>` + html.EscapeString(text) + `</body></html>`)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return "", nil, werr
	}
	return path, func() { os.Remove(path) }, nil
}

// fileURL is the file: URL of an absolute path.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if len(p) > 0 && p[0] != '/' {
		p = "/" + p // C:/Users/... on Windows
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// opener starts something that shows the page at the given redirect file.
type opener struct {
	name string
	cmd  func(file string) *exec.Cmd
}

// appOpener opens the page in a Chromium-family browser's app mode: a
// window of its own without tabs or an address bar.
func appOpener(browser string) opener {
	return opener{name: "app window (" + filepath.Base(browser) + ")", cmd: func(file string) *exec.Cmd {
		return exec.Command(browser, "--app="+fileURL(file), "--window-size="+windowSize)
	}}
}

// defaultOpener opens the page in the user's default browser.
func defaultOpener() opener {
	return opener{name: "default browser", cmd: func(file string) *exec.Cmd {
		switch runtime.GOOS {
		case "darwin":
			return exec.Command("/usr/bin/open", file)
		case "windows":
			return exec.Command("rundll32", "url.dll,FileProtocolHandler", file)
		}
		return exec.Command("xdg-open", file)
	}}
}

// openers lists the ways to show the page, best first. appBrowser names a
// Chromium-family browser to use for the app window; empty means the
// system's (see systemAppBrowsers), and plain true skips app windows.
func openers(appBrowser string, plain bool) []opener {
	var out []opener
	if !plain {
		if appBrowser != "" {
			out = append(out, appOpener(appBrowser))
		} else {
			for _, b := range systemAppBrowsers() {
				if st, err := os.Stat(b); err == nil && st.Mode().IsRegular() {
					out = append(out, appOpener(b))
					break
				}
			}
		}
	}
	return append(out, defaultOpener())
}

// showWindow opens the page with each opener in turn until one results in
// a page actually connecting (seen), waiting up to wait for each. It
// returns a function that removes the redirect file.
func showWindow(target, text string, ways []opener, wait time.Duration, seen func() bool, logf func(msg string, args ...any)) (cleanup func(), err error) {
	file, remove, err := redirectFile(target, text)
	if err != nil {
		return func() {}, err
	}
	// The file has done its job once the browser has followed it.
	timer := time.AfterFunc(2*time.Minute, remove)
	cleanup = func() { timer.Stop(); remove() }
	err = errors.New("no way to open a browser")
	for _, w := range ways {
		cmd := w.cmd(file)
		if serr := cmd.Start(); serr != nil {
			logf("could not start", "opener", w.name, "err", serr)
			err = serr
			continue
		}
		go cmd.Wait()
		logf("opening the window", "opener", w.name)
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			if seen() {
				return cleanup, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		logf("no page connected", "opener", w.name, "after", wait)
		err = errors.New("the browser did not open the page")
	}
	return cleanup, err
}
