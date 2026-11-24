package quick

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/handoff"
)

// The size of the app-style window: the page is one narrow column.
const windowSize = "480,820"

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
	// browser is the program that will read the file: a browser's path,
	// or "" for the user's default browser. It decides where the file goes
	// (see handoff.Dir: sandboxed browsers cannot read hidden directories).
	browser string
	cmd     func(file string) *exec.Cmd
}

// appOpener opens the page in a Chromium-family browser's app mode: a
// window of its own without tabs or an address bar.
func appOpener(browser string) opener {
	return opener{name: "app window (" + filepath.Base(browser) + ")", browser: browser, cmd: func(file string) *exec.Cmd {
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
// a page actually connecting (seen), waiting up to wait for each. Each
// opener gets its own redirect file where its browser can read it. It
// returns a function that removes the files still on disk.
func showWindow(target, text string, ways []opener, wait time.Duration, seen func() bool, logf func(msg string, args ...any)) (cleanup func(), err error) {
	var removers []func()
	var mu sync.Mutex
	cleanup = func() {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range removers {
			r()
		}
		removers = nil
	}
	page := handoff.Page(target, text)
	err = errors.New("no way to open a browser")
	for _, w := range ways {
		dir, derr := handoff.Dir(w.browser)
		if derr != nil {
			err = derr
			continue
		}
		handoff.Sweep(dir)
		file, remove, werr := handoff.Write(dir, "quick", page)
		if werr != nil {
			logf("could not write the redirect file", "opener", w.name, "err", werr)
			err = werr
			continue
		}
		// The file has done its job once the browser has followed it.
		timer := time.AfterFunc(2*time.Minute, remove)
		mu.Lock()
		removers = append(removers, func() { timer.Stop(); remove() })
		mu.Unlock()
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
