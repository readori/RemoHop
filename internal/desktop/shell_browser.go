package desktop

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/handoff"
)

// browserShell is the fallback UI: the page in a browser. On Windows that
// is an app window of Microsoft Edge (part of the system), elsewhere the
// default browser. There is no tray, so the app ends a while after its
// last page closed, unless someone is connected or asking.
type browserShell struct {
	app        *App
	srv        *Server
	log        *slog.Logger
	appBrowser string
	plain      bool
	quit       chan struct{}
	once       sync.Once
}

func platform() string { return runtime.GOOS }

// edgePaths is where Microsoft Edge lives on Windows.
func edgePaths() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		if base := os.Getenv(env); base != "" {
			out = append(out, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	return out
}

// open shows target (a page address with its token) through a private
// redirect file, so the token is not on a command line.
func (b *browserShell) open(target string, width, height string) error {
	browser := b.appBrowser
	if browser == "" && !b.plain {
		for _, p := range edgePaths() {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
				browser = p
				break
			}
		}
	}
	dir, err := handoff.Dir(browser)
	if err != nil {
		return err
	}
	handoff.Sweep(dir)
	file, remove, err := handoff.Write(dir, "desktop", handoff.Page(target, "RemoHop"))
	if err != nil {
		return err
	}
	time.AfterFunc(2*time.Minute, remove)
	var cmd *exec.Cmd
	switch {
	case browser != "":
		u := "file:///" + filepath.ToSlash(file)
		if len(file) > 0 && file[0] == '/' {
			u = "file://" + file
		}
		cmd = exec.Command(browser, "--app="+u, "--window-size="+width+","+height)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("/usr/bin/open", file)
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", file)
	default:
		cmd = exec.Command("xdg-open", file)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func runBrowser(ctx context.Context, app *App, srv *Server, o runOptions, log *slog.Logger) error {
	b := &browserShell{app: app, srv: srv, log: log, appBrowser: o.appBrowser, plain: o.browser && o.appBrowser == "", quit: make(chan struct{})}
	srv.SetUI(b)
	app.SetHooks(Hooks{Request: func(Request) {
		// Without a native window the page is all there is: if none is
		// showing, open one.
		if v, _, _ := srv.Presence(); v == 0 {
			b.Show()
		}
	}})
	if !o.hidden {
		if err := b.open(srv.URL(), "1180", "720"); err != nil {
			return err
		}
	}
	first := time.Now()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.quit:
			return nil
		case <-t.C:
		}
		v, seen, gone := srv.Presence()
		st := app.State()
		busy := len(st.Incoming.Sessions) > 0 || len(st.Incoming.Requests) > 0
		switch {
		case v > 0 || busy:
		case !seen && time.Since(first) > 2*time.Minute && !o.hidden:
			log.Info("no window appeared; stopping")
			return nil
		case seen && time.Since(gone) > 60*time.Second && !st.Incoming.On:
			log.Info("the window is gone; stopping")
			return nil
		}
	}
}

func (b *browserShell) Show() {
	if err := b.open(b.srv.URL(), "1180", "720"); err != nil {
		b.log.Error("could not open a window", "err", err)
	}
}

func (b *browserShell) Hide() {}

func (b *browserShell) PopOut(url, title string) error {
	if err := b.open(url, "1180", "720"); err != nil {
		return errors.New("could not open a window")
	}
	return nil
}

func (b *browserShell) Quit() { b.once.Do(func() { close(b.quit) }) }

func (b *browserShell) Info() UIInfo { return UIInfo{Platform: platform()} }
