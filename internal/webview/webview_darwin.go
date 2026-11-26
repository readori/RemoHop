package webview

// macOS: AppKit and WKWebView through the Objective-C runtime, called with
// purego (no cgo, so the build stays reproducible from any system and the
// release check that rebuilds the signed binary keeps working). The tray
// icon is an NSStatusItem.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}
type osVersion struct{ Major, Minor, Patch int }

var (
	loadOnce sync.Once
	loadErr  error

	dispatchAsyncF func(queue, ctx, fn uintptr)
	mainQueue      uintptr
	cbDispatch     uintptr
)

func sel(name string) objc.SEL   { return objc.RegisterName(name) }
func cls(name string) objc.ID    { return objc.ID(objc.GetClass(name)) }
func nsStr(s string) objc.ID     { return cls("NSString").Send(sel("stringWithUTF8String:"), s) }
func alloc(class string) objc.ID { return cls(class).Send(sel("alloc")) }

// goStr copies an NSString into a Go string.
func goStr(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := objc.Send[*byte](s, sel("UTF8String"))
	if p == nil {
		return ""
	}
	var b []byte
	for i := uintptr(0); ; i++ {
		c := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}

func load() error {
	loadOnce.Do(func() {
		for _, fw := range []string{
			"/System/Library/Frameworks/Foundation.framework/Foundation",
			"/System/Library/Frameworks/AppKit.framework/AppKit",
			"/System/Library/Frameworks/WebKit.framework/WebKit",
		} {
			if _, err := purego.Dlopen(fw, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
				loadErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
				return
			}
		}
		libSystem, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		purego.RegisterLibFunc(&dispatchAsyncF, libSystem, "dispatch_async_f")
		// dispatch_get_main_queue() is a macro for &_dispatch_main_q.
		if mainQueue, err = purego.Dlsym(libSystem, "_dispatch_main_q"); err != nil {
			loadErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
			return
		}
		if objc.GetClass("WKWebView") == 0 {
			loadErr = fmt.Errorf("%w: WebKit has no WKWebView", ErrUnavailable)
		}
	})
	return loadErr
}

func macOSVersion() osVersion {
	pi := cls("NSProcessInfo").Send(sel("processInfo"))
	return objc.Send[osVersion](pi, sel("operatingSystemVersion"))
}

func webKitVersion() string {
	b := cls("NSBundle").Send(sel("bundleWithIdentifier:"), nsStr("com.apple.WebKit"))
	if b == 0 {
		return ""
	}
	v := b.Send(sel("objectForInfoDictionaryKey:"), nsStr("CFBundleShortVersionString"))
	return goStr(v)
}

func probe() (Info, error) {
	if err := load(); err != nil {
		return Info{}, err
	}
	v := macOSVersion()
	ver := webKitVersion()
	return Info{Engine: "WKWebView", Version: strings.TrimSpace(fmt.Sprintf("%s (macOS %d.%d.%d)", ver, v.Major, v.Minor, v.Patch))}, nil
}

type macApp struct {
	cfg  Config
	nsap objc.ID

	qmu   sync.Mutex
	queue []func()

	windows map[objc.ID]*macWindow // by NSWindow
	views   map[objc.ID]*macWindow // by WKWebView
	tray    *Tray
	status  objc.ID
	target  objc.ID
	uiDel   objc.ID
	winDel  objc.ID
	setup   func(App)
	started bool
}

type macWindow struct {
	app    *macApp
	win    objc.ID
	view   objc.ID
	opts   Options
	closed bool
}

var theApp *macApp

const (
	styleTitled         = 1 << 0
	styleClosable       = 1 << 1
	styleMiniaturizable = 1 << 2
	styleResizable      = 1 << 3
	backingBuffered     = 2
	floatingLevel       = 3
)

var classesOnce sync.Once
var classesErr error

// registerClasses defines the delegate classes once per process.
func registerClasses() error {
	classesOnce.Do(func() {
		nsObject := objc.GetClass("NSObject")
		_, err := objc.RegisterClass("RemoHopAppDelegate", nsObject, nil, nil, []objc.MethodDef{
			{Cmd: sel("applicationDidFinishLaunching:"), Fn: func(self objc.ID, _ objc.SEL, note objc.ID) {
				a := theApp
				if a != nil && !a.started {
					a.started = true
					a.nsap.Send(sel("activateIgnoringOtherApps:"), true)
					a.setup(a)
				}
			}},
			{Cmd: sel("applicationShouldTerminateAfterLastWindowClosed:"), Fn: func(self objc.ID, _ objc.SEL, app objc.ID) bool {
				return false
			}},
			{Cmd: sel("applicationShouldHandleReopen:hasVisibleWindows:"), Fn: func(self objc.ID, _ objc.SEL, app objc.ID, visible bool) bool {
				if a := theApp; a != nil && a.cfg.OnReopen != nil {
					a.cfg.OnReopen()
				}
				return true
			}},
			{Cmd: sel("applicationShouldTerminate:"), Fn: func(self objc.ID, _ objc.SEL, app objc.ID) uint {
				// Quit from the menu or the Dock: the program decides.
				if a := theApp; a != nil && a.cfg.OnQuitRequest != nil {
					a.cfg.OnQuitRequest()
					return 0 // NSTerminateCancel
				}
				return 1 // NSTerminateNow
			}},
		})
		if err != nil {
			classesErr = err
			return
		}
		_, err = objc.RegisterClass("RemoHopWindowDelegate", nsObject, nil, nil, []objc.MethodDef{
			{Cmd: sel("windowShouldClose:"), Fn: func(self objc.ID, _ objc.SEL, win objc.ID) bool {
				w := theApp.windows[win]
				return w == nil || w.opts.OnClose == nil || w.opts.OnClose()
			}},
			{Cmd: sel("windowWillClose:"), Fn: func(self objc.ID, _ objc.SEL, note objc.ID) {
				win := note.Send(sel("object"))
				if w := theApp.windows[win]; w != nil {
					w.closed = true
					delete(theApp.windows, win)
					delete(theApp.views, w.view)
				}
			}},
		})
		if err != nil {
			classesErr = err
			return
		}
		_, err = objc.RegisterClass("RemoHopMenuTarget", nsObject, nil, nil, []objc.MethodDef{
			{Cmd: sel("remohopMenu:"), Fn: func(self objc.ID, _ objc.SEL, sender objc.ID) {
				a := theApp
				if a == nil || a.tray == nil {
					return
				}
				i := objc.Send[int](sender, sel("tag"))
				switch {
				case i == -1:
					if a.tray.OnActivate != nil {
						a.tray.OnActivate()
					}
				case i >= 0 && i < len(a.tray.Menu) && a.tray.Menu[i].OnClick != nil:
					a.tray.Menu[i].OnClick()
				}
			}},
		})
		if err != nil {
			classesErr = err
			return
		}
		// The page's file inputs need an open panel, and links that open a
		// new window go to the default browser.
		_, err = objc.RegisterClass("RemoHopUIDelegate", nsObject, nil, nil, []objc.MethodDef{
			{Cmd: sel("webView:runOpenPanelWithParameters:initiatedByFrame:completionHandler:"),
				Fn: func(self objc.ID, _ objc.SEL, view, params, frame objc.ID, done objc.Block) {
					panel := cls("NSOpenPanel").Send(sel("openPanel"))
					panel.Send(sel("setAllowsMultipleSelection:"), objc.Send[bool](params, sel("allowsMultipleSelection")))
					panel.Send(sel("setCanChooseDirectories:"), false)
					var urls objc.ID
					if objc.Send[int](panel, sel("runModal")) == 1 { // NSModalResponseOK
						urls = panel.Send(sel("URLs"))
					}
					callBlock(done, uintptr(urls))
				}},
			{Cmd: sel("webView:createWebViewWithConfiguration:forNavigationAction:windowFeatures:"),
				Fn: func(self objc.ID, _ objc.SEL, view, conf, action, features objc.ID) objc.ID {
					if u := action.Send(sel("request")).Send(sel("URL")); u != 0 {
						cls("NSWorkspace").Send(sel("sharedWorkspace")).Send(sel("openURL:"), u)
					}
					return 0
				}},
			// Downloads (macOS 11.3 and later): a response the view cannot
			// show, or a link with a download attribute, is saved into
			// ~/Downloads under a name that is not taken yet.
			{Cmd: sel("webView:decidePolicyForNavigationResponse:decisionHandler:"),
				Fn: func(self objc.ID, _ objc.SEL, view, resp objc.ID, done objc.Block) {
					policy := uintptr(1) // allow
					if !objc.Send[bool](resp, sel("canShowMIMEType")) || isAttachment(resp.Send(sel("response"))) {
						policy = 2 // download
					}
					callBlock(done, policy)
				}},
			{Cmd: sel("webView:decidePolicyForNavigationAction:decisionHandler:"),
				Fn: func(self objc.ID, _ objc.SEL, view, action objc.ID, done objc.Block) {
					policy := uintptr(1)
					if objc.Send[bool](action, sel("respondsToSelector:"), sel("shouldPerformDownload")) && objc.Send[bool](action, sel("shouldPerformDownload")) {
						policy = 2
					}
					callBlock(done, policy)
				}},
			{Cmd: sel("webView:navigationResponse:didBecomeDownload:"), Fn: func(self objc.ID, _ objc.SEL, view, resp, dl objc.ID) {
				dl.Send(sel("setDelegate:"), self)
			}},
			{Cmd: sel("webView:navigationAction:didBecomeDownload:"), Fn: func(self objc.ID, _ objc.SEL, view, action, dl objc.ID) {
				dl.Send(sel("setDelegate:"), self)
			}},
			{Cmd: sel("download:decideDestinationUsingResponse:suggestedFilename:completionHandler:"),
				Fn: func(self objc.ID, _ objc.SEL, dl, resp, name objc.ID, done objc.Block) {
					path := downloadPath(goStr(name))
					callBlock(done, uintptr(cls("NSURL").Send(sel("fileURLWithPath:"), nsStr(path))))
				}},
		})
		if err != nil {
			classesErr = err
			return
		}
		// A borderless window cannot become the key window, so the banner
		// would not take clicks; this subclass can.
		_, err = objc.RegisterClass("RemoHopOverlayWindow", objc.GetClass("NSWindow"), nil, nil, []objc.MethodDef{
			{Cmd: sel("canBecomeKeyWindow"), Fn: func(self objc.ID, _ objc.SEL) bool { return true }},
		})
		classesErr = err
	})
	return classesErr
}

func isAttachment(resp objc.ID) bool {
	if resp == 0 || !objc.Send[bool](resp, sel("respondsToSelector:"), sel("valueForHTTPHeaderField:")) {
		return false
	}
	v := goStr(resp.Send(sel("valueForHTTPHeaderField:"), nsStr("Content-Disposition")))
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "attachment")
}

// downloadPath is a free file name in ~/Downloads for a suggested name.
func downloadPath(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "/", "_"))
	if name == "." || name == "" || name == ".." {
		name = "download"
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, "Downloads")
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 2; i < 1000; i++ {
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, stem+" ("+strconv.Itoa(i)+")"+ext)
	}
	return filepath.Join(dir, stem+"-"+strconv.FormatInt(time.Now().UnixNano(), 36)+ext)
}

// callBlock calls a block that AppKit or WebKit gave us with one argument.
// A block is a struct whose fourth field (after isa and two 32-bit flags)
// is the function to call with the block itself first.
func callBlock(b objc.Block, arg uintptr) {
	if b == 0 {
		return
	}
	invoke := *(*uintptr)(unsafe.Add(*(*unsafe.Pointer)(unsafe.Pointer(&b)), 16))
	purego.SyscallN(invoke, uintptr(b), arg)
}

func run(cfg Config, setup func(App)) error {
	if err := load(); err != nil {
		return err
	}
	if err := registerClasses(); err != nil {
		return fmt.Errorf("webview: %v", err)
	}
	a := &macApp{cfg: cfg, windows: map[objc.ID]*macWindow{}, views: map[objc.ID]*macWindow{}, setup: setup}
	theApp = a
	cbDispatch = purego.NewCallback(func(ctx uintptr) uintptr {
		theApp.drain()
		return 0
	})
	pool := alloc("NSAutoreleasePool").Send(sel("init"))
	a.nsap = cls("NSApplication").Send(sel("sharedApplication"))
	a.nsap.Send(sel("setActivationPolicy:"), 0) // regular: Dock icon and menu bar
	a.nsap.Send(sel("setMainMenu:"), mainMenu())
	del := alloc("RemoHopAppDelegate").Send(sel("init"))
	a.nsap.Send(sel("setDelegate:"), del)
	a.winDel = alloc("RemoHopWindowDelegate").Send(sel("init"))
	a.uiDel = alloc("RemoHopUIDelegate").Send(sel("init"))
	a.target = alloc("RemoHopMenuTarget").Send(sel("init"))
	pool.Send(sel("drain"))
	a.nsap.Send(sel("run")) // setup runs in applicationDidFinishLaunching:
	if !a.started {
		return errors.New("webview: the application did not start")
	}
	return nil
}

// mainMenu is the menu bar: without an Edit menu, copy and paste
// shortcuts do nothing in a web view.
func mainMenu() objc.ID {
	item := func(title, action, key string) objc.ID {
		var act objc.SEL
		if action != "" {
			act = sel(action)
		}
		return alloc("NSMenuItem").Send(sel("initWithTitle:action:keyEquivalent:"), nsStr(title), act, nsStr(key))
	}
	submenu := func(title string, items ...objc.ID) objc.ID {
		m := alloc("NSMenu").Send(sel("initWithTitle:"), nsStr(title))
		for _, it := range items {
			m.Send(sel("addItem:"), it)
		}
		top := item(title, "", "")
		top.Send(sel("setSubmenu:"), m)
		return top
	}
	bar := alloc("NSMenu").Send(sel("init"))
	bar.Send(sel("addItem:"), submenu("RemoHop",
		item("Hide RemoHop", "hide:", "h"),
		cls("NSMenuItem").Send(sel("separatorItem")),
		item("Quit RemoHop", "terminate:", "q")))
	bar.Send(sel("addItem:"), submenu("Edit",
		item("Undo", "undo:", "z"), item("Redo", "redo:", "Z"),
		cls("NSMenuItem").Send(sel("separatorItem")),
		item("Cut", "cut:", "x"), item("Copy", "copy:", "c"), item("Paste", "paste:", "v"),
		item("Select All", "selectAll:", "a")))
	bar.Send(sel("addItem:"), submenu("Window",
		item("Minimize", "performMiniaturize:", "m"), item("Close", "performClose:", "w")))
	return bar
}

func (a *macApp) Dispatch(fn func()) {
	a.qmu.Lock()
	a.queue = append(a.queue, fn)
	a.qmu.Unlock()
	dispatchAsyncF(mainQueue, 0, cbDispatch)
}

func (a *macApp) drain() {
	a.qmu.Lock()
	q := a.queue
	a.queue = nil
	a.qmu.Unlock()
	pool := alloc("NSAutoreleasePool").Send(sel("init"))
	defer pool.Send(sel("drain"))
	for _, fn := range q {
		fn()
	}
}

func (a *macApp) Quit() {
	a.Dispatch(func() {
		a.SetTray(nil)
		a.nsap.Send(sel("stop:"), 0)
		// stop: takes effect after the next event; post one.
		ev := cls("NSEvent").Send(sel("otherEventWithType:location:modifierFlags:timestamp:windowNumber:context:subtype:data1:data2:"),
			uint(15), nsPoint{}, uint(0), float64(0), 0, objc.ID(0), int16(0), 0, 0)
		a.nsap.Send(sel("postEvent:atStart:"), ev, true)
	})
}

func (a *macApp) NewWindow(o Options) (Window, error) {
	width, height := o.Size()
	if !o.Overlay {
		// Fit the screen's visible frame (minus menu bar and Dock).
		if scr := cls("NSScreen").Send(sel("mainScreen")); scr != 0 {
			f := objc.Send[nsRect](scr, sel("visibleFrame"))
			width, height, o.MinWidth, o.MinHeight = fitArea(width, height, o.MinWidth, o.MinHeight, int(f.Size.W), int(f.Size.H))
		}
	}
	rect := nsRect{Size: nsSize{float64(width), float64(height)}}
	class := "NSWindow"
	style := uint(styleTitled | styleClosable | styleMiniaturizable | styleResizable)
	if o.Overlay {
		class, style = "RemoHopOverlayWindow", 0 // borderless
	}
	win := alloc(class).Send(sel("initWithContentRect:styleMask:backing:defer:"), rect, style, uint(backingBuffered), false)
	if win == 0 {
		return nil, errors.New("webview: could not create a window")
	}
	win.Send(sel("setReleasedWhenClosed:"), false)
	win.Send(sel("setTitle:"), nsStr(o.Title))
	win.Send(sel("setDelegate:"), a.winDel)
	if o.MinWidth > 0 || o.MinHeight > 0 {
		win.Send(sel("setContentMinSize:"), nsSize{float64(o.MinWidth), float64(o.MinHeight)})
	}
	if o.Overlay {
		win.Send(sel("setLevel:"), floatingLevel)
		win.Send(sel("setCollectionBehavior:"), uint(1|16|64)) // all spaces, stationary, not in Cmd-` cycle
		win.Send(sel("setHidesOnDeactivate:"), false)
		// The bottom-right corner of the visible frame (above the Dock),
		// away from the menu bar and the app's window and tab bar.
		if scr := cls("NSScreen").Send(sel("mainScreen")); scr != 0 {
			f := objc.Send[nsRect](scr, sel("visibleFrame"))
			win.Send(sel("setFrameOrigin:"), nsPoint{f.Origin.X + f.Size.W - float64(width) - overlayMargin, f.Origin.Y + overlayMargin})
		}
	} else {
		win.Send(sel("center"))
	}
	conf := alloc("WKWebViewConfiguration").Send(sel("init"))
	view := alloc("WKWebView").Send(sel("initWithFrame:configuration:"), rect, conf)
	if view == 0 {
		return nil, fmt.Errorf("%w: could not create a WKWebView", ErrUnavailable)
	}
	view.Send(sel("setUIDelegate:"), a.uiDel)
	view.Send(sel("setNavigationDelegate:"), a.uiDel)
	win.Send(sel("setContentView:"), view)
	w := &macWindow{app: a, win: win, view: view, opts: o}
	a.windows[win] = w
	a.views[view] = w
	w.Navigate(o.URL)
	if !o.Hidden {
		w.Show()
	}
	return w, nil
}

func (w *macWindow) Navigate(url string) {
	if w.closed {
		return
	}
	u := cls("NSURL").Send(sel("URLWithString:"), nsStr(url))
	if u == 0 {
		return
	}
	w.view.Send(sel("loadRequest:"), cls("NSURLRequest").Send(sel("requestWithURL:"), u))
}

func (w *macWindow) SetTitle(t string) {
	if !w.closed {
		w.win.Send(sel("setTitle:"), nsStr(t))
	}
}

func (w *macWindow) Show() {
	if w.closed {
		return
	}
	if w.opts.Overlay {
		w.win.Send(sel("orderFrontRegardless"))
		return
	}
	if objc.Send[bool](w.win, sel("isMiniaturized")) {
		w.win.Send(sel("deminiaturize:"), 0)
	}
	w.win.Send(sel("makeKeyAndOrderFront:"), 0)
	w.app.nsap.Send(sel("activateIgnoringOtherApps:"), true)
}

func (w *macWindow) Hide() {
	if !w.closed {
		w.win.Send(sel("orderOut:"), 0)
	}
}

func (w *macWindow) Close() {
	if !w.closed {
		w.win.Send(sel("close"))
	}
}

func (a *macApp) SetTray(t *Tray) error {
	bar := cls("NSStatusBar").Send(sel("systemStatusBar"))
	if t == nil {
		if a.status != 0 {
			bar.Send(sel("removeStatusItem:"), a.status)
			a.status = 0
		}
		a.tray = nil
		return nil
	}
	a.tray = t
	if a.status == 0 {
		a.status = bar.Send(sel("statusItemWithLength:"), float64(-1)) // NSVariableStatusItemLength
		if a.status == 0 {
			return errors.New("webview: no status item")
		}
		a.status.Send(sel("retain"))
		png := a.cfg.TemplatePNG
		if len(png) == 0 {
			png = a.cfg.IconPNG
		}
		button := a.status.Send(sel("button"))
		if len(png) > 0 && button != 0 {
			data := cls("NSData").Send(sel("dataWithBytes:length:"), unsafe.Pointer(&png[0]), uint(len(png)))
			img := alloc("NSImage").Send(sel("initWithData:"), data)
			if img != 0 {
				img.Send(sel("setTemplate:"), true)
				img.Send(sel("setSize:"), nsSize{18, 18})
				button.Send(sel("setImage:"), img)
			}
		} else if button != 0 {
			button.Send(sel("setTitle:"), nsStr("RemoHop"))
		}
	}
	if button := a.status.Send(sel("button")); button != 0 {
		button.Send(sel("setToolTip:"), nsStr(t.Tooltip))
	}
	menu := alloc("NSMenu").Send(sel("init"))
	menu.Send(sel("setAutoenablesItems:"), false)
	add := func(title string, tag int, enabled bool) {
		it := alloc("NSMenuItem").Send(sel("initWithTitle:action:keyEquivalent:"), nsStr(title), sel("remohopMenu:"), nsStr(""))
		it.Send(sel("setTarget:"), a.target)
		it.Send(sel("setTag:"), tag)
		it.Send(sel("setEnabled:"), enabled)
		menu.Send(sel("addItem:"), it)
	}
	if t.OnActivate != nil {
		add(t.Tooltip, -1, true)
		menu.Send(sel("addItem:"), cls("NSMenuItem").Send(sel("separatorItem")))
	}
	for i, m := range t.Menu {
		if m.Label == "" {
			menu.Send(sel("addItem:"), cls("NSMenuItem").Send(sel("separatorItem")))
			continue
		}
		add(m.Label, i, !m.Disabled)
	}
	a.status.Send(sel("setMenu:"), menu)
	return nil
}
