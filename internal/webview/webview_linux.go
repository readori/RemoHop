package webview

// Linux: GTK 3 and WebKitGTK (webkit2gtk-4.1, else 4.0), loaded with dlopen
// through purego, and an AppIndicator (Ayatana or the original) for the
// tray when one is installed. Nothing here needs cgo, but purego makes the
// binary dynamically linked against the C library, which GTK needs anyway.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
)

type gtkLib struct {
	gtk, gobject, glib, webkit uintptr
	webkitName                 string

	initCheck        func(argc, argv uintptr) bool
	gtkMain          func()
	gtkMainQuit      func()
	windowNew        func(kind int32) uintptr
	setTitle         func(w uintptr, title string)
	setDefaultSize   func(w uintptr, width, height int32)
	setSizeRequest   func(w uintptr, width, height int32)
	containerAdd     func(c, child uintptr)
	showAll          func(w uintptr)
	hide             func(w uintptr)
	present          func(w uintptr)
	destroy          func(w uintptr)
	setDecorated     func(w uintptr, on bool)
	setKeepAbove     func(w uintptr, on bool)
	setSkipTaskbar   func(w uintptr, on bool)
	setSkipPager     func(w uintptr, on bool)
	setResizable     func(w uintptr, on bool)
	windowMove       func(w uintptr, x, y int32)
	screenDefault    func() uintptr
	screenWidth      func(s uintptr) int32
	menuNew          func() uintptr
	menuItemNew      func(label string) uintptr
	separatorNew     func() uintptr
	menuShellAppend  func(menu, item uintptr)
	setSensitive     func(w uintptr, on bool)
	signalConnect    func(instance uintptr, signal string, handler, data, destroy uintptr, flags int32) uint64
	idleAdd          func(fn, data uintptr) uint32
	setPrgname       func(name string)
	webViewNew       func() uintptr
	loadURI          func(v uintptr, uri string)
	getSettings      func(v uintptr) uintptr
	major, minor     func() uint32
	micro            func() uint32
	allFeatures      func() uintptr
	featureListLen   func(l uintptr) uint64
	featureListGet   func(l uintptr, i uint64) uintptr
	featureID        func(f uintptr) string
	setFeatureOn     func(s, f uintptr, on bool)
	haveFeatureAPI   bool
	indicatorNew     func(id, icon string, category int32) uintptr
	indicatorStatus  func(ind uintptr, status int32)
	indicatorMenu    func(ind, menu uintptr)
	indicatorTheme   func(ind uintptr, path string)
	indicatorTitle   func(ind uintptr, title string)
	indicatorSecond  func(ind, item uintptr)
	haveIndicator    bool
	haveIndicatorExt bool
}

var (
	libOnce sync.Once
	lib     *gtkLib
	libErr  error
)

func dlopenFirst(names ...string) (uintptr, string, error) {
	var last error
	for _, n := range names {
		h, err := purego.Dlopen(n, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			return h, n, nil
		}
		last = err
	}
	return 0, "", last
}

func has(h uintptr, sym string) bool {
	_, err := purego.Dlsym(h, sym)
	return err == nil
}

func loadGTK() (*gtkLib, error) {
	libOnce.Do(func() {
		l := &gtkLib{}
		var err error
		if l.gtk, _, err = dlopenFirst("libgtk-3.so.0"); err != nil {
			libErr = fmt.Errorf("%w: GTK 3 is not installed (%v)", ErrUnavailable, err)
			return
		}
		if l.webkit, l.webkitName, err = dlopenFirst("libwebkit2gtk-4.1.so.0", "libwebkit2gtk-4.0.so.37"); err != nil {
			libErr = fmt.Errorf("%w: WebKitGTK is not installed (libwebkit2gtk-4.1 or 4.0; %v)", ErrUnavailable, err)
			return
		}
		if l.gobject, _, err = dlopenFirst("libgobject-2.0.so.0"); err != nil {
			libErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if l.glib, _, err = dlopenFirst("libglib-2.0.so.0"); err != nil {
			libErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		reg := func(fptr any, h uintptr, name string) {
			if err == nil && !has(h, name) {
				err = fmt.Errorf("%w: missing %s", ErrUnavailable, name)
				return
			}
			if err == nil {
				purego.RegisterLibFunc(fptr, h, name)
			}
		}
		reg(&l.initCheck, l.gtk, "gtk_init_check")
		reg(&l.gtkMain, l.gtk, "gtk_main")
		reg(&l.gtkMainQuit, l.gtk, "gtk_main_quit")
		reg(&l.windowNew, l.gtk, "gtk_window_new")
		reg(&l.setTitle, l.gtk, "gtk_window_set_title")
		reg(&l.setDefaultSize, l.gtk, "gtk_window_set_default_size")
		reg(&l.setSizeRequest, l.gtk, "gtk_widget_set_size_request")
		reg(&l.containerAdd, l.gtk, "gtk_container_add")
		reg(&l.showAll, l.gtk, "gtk_widget_show_all")
		reg(&l.hide, l.gtk, "gtk_widget_hide")
		reg(&l.present, l.gtk, "gtk_window_present")
		reg(&l.destroy, l.gtk, "gtk_widget_destroy")
		reg(&l.setDecorated, l.gtk, "gtk_window_set_decorated")
		reg(&l.setKeepAbove, l.gtk, "gtk_window_set_keep_above")
		reg(&l.setSkipTaskbar, l.gtk, "gtk_window_set_skip_taskbar_hint")
		reg(&l.setSkipPager, l.gtk, "gtk_window_set_skip_pager_hint")
		reg(&l.setResizable, l.gtk, "gtk_window_set_resizable")
		reg(&l.windowMove, l.gtk, "gtk_window_move")
		reg(&l.screenDefault, l.gtk, "gdk_screen_get_default")
		reg(&l.screenWidth, l.gtk, "gdk_screen_get_width")
		reg(&l.menuNew, l.gtk, "gtk_menu_new")
		reg(&l.menuItemNew, l.gtk, "gtk_menu_item_new_with_label")
		reg(&l.separatorNew, l.gtk, "gtk_separator_menu_item_new")
		reg(&l.menuShellAppend, l.gtk, "gtk_menu_shell_append")
		reg(&l.setSensitive, l.gtk, "gtk_widget_set_sensitive")
		reg(&l.signalConnect, l.gobject, "g_signal_connect_data")
		reg(&l.idleAdd, l.glib, "g_idle_add")
		reg(&l.setPrgname, l.glib, "g_set_prgname")
		reg(&l.webViewNew, l.webkit, "webkit_web_view_new")
		reg(&l.loadURI, l.webkit, "webkit_web_view_load_uri")
		reg(&l.getSettings, l.webkit, "webkit_web_view_get_settings")
		reg(&l.major, l.webkit, "webkit_get_major_version")
		reg(&l.minor, l.webkit, "webkit_get_minor_version")
		reg(&l.micro, l.webkit, "webkit_get_micro_version")
		if err != nil {
			libErr = err
			return
		}
		// WebKitGTK 2.42 and later list their web features; WebCodecs is one.
		if has(l.webkit, "webkit_settings_get_all_features") {
			reg(&l.allFeatures, l.webkit, "webkit_settings_get_all_features")
			reg(&l.featureListLen, l.webkit, "webkit_feature_list_get_length")
			reg(&l.featureListGet, l.webkit, "webkit_feature_list_get")
			reg(&l.featureID, l.webkit, "webkit_feature_get_identifier")
			reg(&l.setFeatureOn, l.webkit, "webkit_settings_set_feature_enabled")
			l.haveFeatureAPI = err == nil
			err = nil
		}
		if ind, _, ierr := dlopenFirst("libayatana-appindicator3.so.1", "libappindicator3.so.1"); ierr == nil {
			reg(&l.indicatorNew, ind, "app_indicator_new")
			reg(&l.indicatorStatus, ind, "app_indicator_set_status")
			reg(&l.indicatorMenu, ind, "app_indicator_set_menu")
			reg(&l.indicatorTheme, ind, "app_indicator_set_icon_theme_path")
			l.haveIndicator = err == nil
			err = nil
			if has(ind, "app_indicator_set_title") && has(ind, "app_indicator_set_secondary_activate_target") {
				reg(&l.indicatorTitle, ind, "app_indicator_set_title")
				reg(&l.indicatorSecond, ind, "app_indicator_set_secondary_activate_target")
				l.haveIndicatorExt = err == nil
				err = nil
			}
		}
		lib = l
	})
	return lib, libErr
}

func probe() (Info, error) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return Info{}, fmt.Errorf("%w: no graphical session (DISPLAY and WAYLAND_DISPLAY are unset)", ErrUnavailable)
	}
	l, err := loadGTK()
	if err != nil {
		return Info{}, err
	}
	return Info{Engine: "WebKitGTK", Version: fmt.Sprintf("%d.%d.%d", l.major(), l.minor(), l.micro())}, nil
}

// gtkApp is the App. All its fields are used on the UI thread only, except
// the dispatch queue.
type gtkApp struct {
	l   *gtkLib
	cfg Config

	qmu     sync.Mutex
	queue   []func()
	pending bool

	nextID  uintptr
	windows map[uintptr]*gtkWindow
	items   map[uintptr]func()

	indicator uintptr
	iconDir   string
}

type gtkWindow struct {
	app    *gtkApp
	id     uintptr
	win    uintptr
	view   uintptr
	opts   Options
	closed bool
}

// The C callbacks. purego callbacks are never freed, so there is one of
// each, and the data argument says which window or item it is for.
var (
	cbOnce                                         sync.Once
	cbIdle, cbDelete, cbDestroy, cbActivate, cbCtx uintptr
	theApp                                         *gtkApp
)

func callbacks() {
	cbOnce.Do(func() {
		cbIdle = purego.NewCallback(func(data uintptr) uintptr {
			theApp.drain()
			return 0 // G_SOURCE_REMOVE
		})
		cbDelete = purego.NewCallback(func(widget, event, data uintptr) uintptr {
			w := theApp.windows[data]
			if w == nil || w.opts.OnClose == nil || w.opts.OnClose() {
				return 0 // let GTK destroy it
			}
			return 1 // handled: keep the window
		})
		cbDestroy = purego.NewCallback(func(widget, data uintptr) uintptr {
			if w := theApp.windows[data]; w != nil {
				w.closed = true
				delete(theApp.windows, data)
			}
			return 0
		})
		cbActivate = purego.NewCallback(func(item, data uintptr) uintptr {
			if fn := theApp.items[data]; fn != nil {
				fn()
			}
			return 0
		})
		// The page's own context menu (back, reload) means nothing here.
		cbCtx = purego.NewCallback(func(view, menu, event, hit, data uintptr) uintptr { return 1 })
	})
}

func run(cfg Config, setup func(App)) error {
	if _, err := probe(); err != nil {
		return err
	}
	l := lib
	if cfg.AppID != "" {
		l.setPrgname(cfg.AppID)
	}
	if !l.initCheck(0, 0) {
		return fmt.Errorf("%w: GTK could not open the display", ErrUnavailable)
	}
	a := &gtkApp{l: l, cfg: cfg, windows: map[uintptr]*gtkWindow{}, items: map[uintptr]func(){}}
	theApp = a
	callbacks()
	setup(a)
	l.gtkMain()
	return nil
}

func (a *gtkApp) Dispatch(fn func()) {
	a.qmu.Lock()
	a.queue = append(a.queue, fn)
	schedule := !a.pending
	a.pending = true
	a.qmu.Unlock()
	if schedule {
		a.l.idleAdd(cbIdle, 0) // g_idle_add is safe from any thread
	}
}

func (a *gtkApp) drain() {
	a.qmu.Lock()
	q := a.queue
	a.queue, a.pending = nil, false
	a.qmu.Unlock()
	for _, fn := range q {
		fn()
	}
}

func (a *gtkApp) Quit() { a.Dispatch(a.l.gtkMainQuit) }

func (a *gtkApp) id() uintptr {
	a.nextID++
	return a.nextID
}

func (a *gtkApp) NewWindow(o Options) (Window, error) {
	l := a.l
	w := &gtkWindow{app: a, id: a.id(), opts: o}
	w.win = l.windowNew(0)
	if w.win == 0 {
		return nil, errors.New("webview: gtk_window_new failed")
	}
	l.setTitle(w.win, o.Title)
	width, height := o.Size()
	l.setDefaultSize(w.win, int32(width), int32(height))
	if o.MinWidth > 0 || o.MinHeight > 0 {
		l.setSizeRequest(w.win, int32(o.MinWidth), int32(o.MinHeight))
	}
	if o.Overlay {
		l.setDecorated(w.win, false)
		l.setKeepAbove(w.win, true)
		l.setSkipTaskbar(w.win, true)
		l.setSkipPager(w.win, true)
		l.setResizable(w.win, false)
		if s := l.screenDefault(); s != 0 {
			l.windowMove(w.win, (l.screenWidth(s)-int32(width))/2, 8)
		}
	}
	w.view = l.webViewNew()
	a.enableFeatures(w.view)
	l.signalConnect(w.view, "context-menu", cbCtx, w.id, 0, 0)
	l.containerAdd(w.win, w.view)
	l.signalConnect(w.win, "delete-event", cbDelete, w.id, 0, 0)
	l.signalConnect(w.win, "destroy", cbDestroy, w.id, 0, 0)
	a.windows[w.id] = w
	l.loadURI(w.view, o.URL)
	if !o.Hidden {
		l.showAll(w.win)
		l.present(w.win)
	}
	return w, nil
}

// enableFeatures turns on WebCodecs where this WebKitGTK has it but does
// not enable it by default (2.42 to 2.46); later versions enable it.
func (a *gtkApp) enableFeatures(view uintptr) {
	l := a.l
	if !l.haveFeatureAPI {
		return
	}
	st := l.getSettings(view)
	fl := l.allFeatures()
	for i := uint64(0); i < l.featureListLen(fl); i++ {
		f := l.featureListGet(fl, i)
		switch l.featureID(f) {
		case "WebCodecsVideo", "WebCodecsAV1":
			l.setFeatureOn(st, f, true)
		}
	}
}

func (w *gtkWindow) Navigate(url string) {
	if !w.closed {
		w.app.l.loadURI(w.view, url)
	}
}

func (w *gtkWindow) SetTitle(t string) {
	if !w.closed {
		w.app.l.setTitle(w.win, t)
	}
}

func (w *gtkWindow) Show() {
	if !w.closed {
		w.app.l.showAll(w.win)
		w.app.l.present(w.win)
	}
}

func (w *gtkWindow) Hide() {
	if !w.closed {
		w.app.l.hide(w.win)
	}
}

func (w *gtkWindow) Close() {
	if !w.closed {
		w.app.l.destroy(w.win)
	}
}

func (a *gtkApp) SetTray(t *Tray) error {
	l := a.l
	if !l.haveIndicator {
		return errors.New("webview: no tray here (no AppIndicator library)")
	}
	if t == nil {
		if a.indicator != 0 {
			l.indicatorStatus(a.indicator, 0) // passive: hidden
		}
		return nil
	}
	if a.indicator == 0 {
		if len(a.cfg.IconPNG) == 0 {
			return errors.New("webview: no tray icon")
		}
		dir, err := os.MkdirTemp("", "remohop-tray-")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "remohop-tray.png"), a.cfg.IconPNG, 0o644); err != nil {
			return err
		}
		a.iconDir = dir
		id := a.cfg.AppID
		if id == "" {
			id = "webview"
		}
		a.indicator = l.indicatorNew(id, "remohop-tray", 0)
		if a.indicator == 0 {
			return errors.New("webview: app_indicator_new failed")
		}
		l.indicatorTheme(a.indicator, dir)
	}
	menu := l.menuNew()
	a.items = map[uintptr]func(){}
	var first uintptr
	if t.OnActivate != nil {
		// AppIndicators only have a menu; its first entry opens the window.
		item := l.menuItemNew(t.Tooltip)
		id := a.id()
		a.items[id] = t.OnActivate
		l.signalConnect(item, "activate", cbActivate, id, 0, 0)
		l.menuShellAppend(menu, item)
		l.menuShellAppend(menu, l.separatorNew())
		first = item
	}
	for _, m := range t.Menu {
		if m.Label == "" {
			l.menuShellAppend(menu, l.separatorNew())
			continue
		}
		item := l.menuItemNew(m.Label)
		if m.Disabled {
			l.setSensitive(item, false)
		} else if m.OnClick != nil {
			id := a.id()
			a.items[id] = m.OnClick
			l.signalConnect(item, "activate", cbActivate, id, 0, 0)
		}
		l.menuShellAppend(menu, item)
	}
	l.showAll(menu)
	l.indicatorMenu(a.indicator, menu)
	if l.haveIndicatorExt {
		l.indicatorTitle(a.indicator, t.Tooltip)
		if first != 0 {
			l.indicatorSecond(a.indicator, first) // middle click
		}
	}
	l.indicatorStatus(a.indicator, 1) // active
	return nil
}
