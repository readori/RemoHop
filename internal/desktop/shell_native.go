package desktop

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nativeui"
)

// The tray icons: the RemoHop mark in colour (Linux) and as a template
// (macOS draws only its alpha channel, in the menu bar's colour).
//
//go:embed assets/tray.png
var trayPNG []byte

//go:embed assets/tray-template.png
var trayTemplatePNG []byte

// nativeShell is the UI in native windows: the main window, a window per
// popped-out host, the "being controlled" banner and the tray icon.
type nativeShell struct {
	app  *App
	srv  *Server
	log  *slog.Logger
	info nativeui.Info

	ui     nativeui.App
	main   nativeui.Window
	banner nativeui.Window
	tray   bool

	mu      sync.Mutex
	names   []string
	quitted bool
}

func runNative(ctx context.Context, app *App, srv *Server, o runOptions, log *slog.Logger) error {
	info, err := nativeui.Probe()
	if err != nil {
		return err
	}
	data, _ := config.SubDir("webview")
	if data == "" {
		data = filepath.Join(app.Dir(), "webview")
	}
	sh := &nativeShell{app: app, srv: srv, log: log, info: info}
	cfg := nativeui.Config{AppID: "remohop", DataDir: data, IconPNG: trayPNG, TemplatePNG: trayTemplatePNG,
		OnReopen: sh.Show, OnQuitRequest: sh.Quit}
	go func() {
		<-ctx.Done()
		sh.Quit()
	}()
	return nativeui.Run(cfg, func(ui nativeui.App) {
		sh.ui = ui
		sh.tray = ui.SetTray(sh.trayMenu()) == nil
		w, err := ui.NewWindow(nativeui.Options{Title: "RemoHop", URL: srv.URL(), Width: 960, Height: 640, MinWidth: 420, MinHeight: 520,
			Hidden: o.hidden && sh.tray, OnClose: sh.onMainClose})
		if err != nil {
			log.Error("could not open the window", "err", err)
			ui.Quit()
			return
		}
		sh.main = w
		srv.SetUI(sh)
		app.SetHooks(Hooks{
			Request:  func(Request) { sh.Show() },
			Sessions: sh.setBanner,
			Lang:     func(string) { ui.Dispatch(sh.refreshTray) },
		})
		log.Info("window open", "engine", info.Engine, "version", info.Version, "tray", sh.tray)
	})
}

func (sh *nativeShell) trayMenu() *nativeui.Tray {
	sh.mu.Lock()
	names := sh.names
	sh.mu.Unlock()
	tip := st(sh.app, "tip")
	if len(names) > 0 {
		tip = fmt.Sprintf(st(sh.app, "controlled"), strings.Join(names, ", "))
	}
	return &nativeui.Tray{Tooltip: tip, OnActivate: sh.Show, Menu: []nativeui.MenuItem{
		{Label: st(sh.app, "show"), OnClick: sh.Show},
		{},
		{Label: st(sh.app, "quit"), OnClick: sh.Quit},
	}}
}

func (sh *nativeShell) refreshTray() {
	if sh.tray {
		sh.ui.SetTray(sh.trayMenu())
	}
}

// onMainClose: with a tray the window goes there and the app keeps
// running (incoming connections stay possible); without one, closing it
// quits.
func (sh *nativeShell) onMainClose() bool {
	if sh.tray {
		sh.main.Hide()
		return false
	}
	sh.Quit()
	return false
}

// setBanner shows the "being controlled" banner while anyone is connected.
func (sh *nativeShell) setBanner(names []string) {
	sh.mu.Lock()
	sh.names = names
	sh.mu.Unlock()
	sh.ui.Dispatch(func() {
		sh.refreshTray()
		switch {
		case len(names) > 0 && sh.banner == nil:
			w, err := sh.ui.NewWindow(nativeui.Options{Title: st(sh.app, "banner"), URL: sh.srv.URL() + "overlay.html",
				Width: 420, Height: 56, Overlay: true, OnClose: func() bool { return false }})
			if err == nil {
				sh.banner = w
			}
		case len(names) == 0 && sh.banner != nil:
			sh.banner.Close()
			sh.banner = nil
		}
	})
}

func (sh *nativeShell) Show() {
	sh.ui.Dispatch(func() {
		if sh.main != nil {
			sh.main.Show()
		}
	})
}

func (sh *nativeShell) Hide() {
	sh.ui.Dispatch(func() {
		if sh.main != nil && sh.tray {
			sh.main.Hide()
		}
	})
}

func (sh *nativeShell) PopOut(url, title string) error {
	sh.ui.Dispatch(func() {
		if _, err := sh.ui.NewWindow(nativeui.Options{Title: title, URL: url, Width: 1100, Height: 760, MinWidth: 480, MinHeight: 400}); err != nil {
			sh.log.Error("could not open a window", "err", err)
		}
	})
	return nil
}

func (sh *nativeShell) Quit() {
	sh.mu.Lock()
	done := sh.quitted
	sh.quitted = true
	sh.mu.Unlock()
	if !done && sh.ui != nil {
		sh.ui.Quit()
	}
}

func (sh *nativeShell) Info() UIInfo {
	return UIInfo{Native: true, Engine: sh.info.Engine, Tray: sh.tray, Platform: platform()}
}
